package controller

import (
	"context"
	"fmt"
	"io"
	"slices"
	"strings"
	"time"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"
)

// Evidence collection fills classifier.IncidentInput's Logs and Events —
// without them Subhashini's semantic guard rejects every sub-cause except
// "unknown", so the pipeline escalates every single incident and no
// remediation can ever run. A DetectionEvent on its own says a container is
// crash-looping; it does not say *why*, and "why" is the whole classification.
//
// Both reads are best-effort: a failure to collect evidence degrades the
// classification (less evidence -> escalate) but must never fail the
// reconcile. Escalation is the safe direction, so a missing log is a
// downgrade, not an error.

// Evidence bounds. The blob assembled here is sent verbatim to an LLM when
// CLASSIFIER_PROVIDER names a real provider, so it is capped on both axes: an
// unbounded crash log blows up token cost and latency, and an unbounded event
// list buries the one line that identifies the failure.
const (
	evidenceLogTailLines = 100
	evidenceLogMaxBytes  = 4096
	// A single log line can be arbitrarily long, so a line count alone does
	// not bound the read — this is the hard ceiling on bytes pulled off the
	// wire, deliberately above evidenceLogMaxBytes so truncateTail still has
	// something to trim from the front.
	evidenceLogReadCeiling = 4 * evidenceLogMaxBytes
	evidenceEventWindow    = 30 * time.Minute
	evidenceMaxEvents      = 25
)

// PodLogFetcher reads the tail of one container's log. It exists as a seam
// because pod logs come from the pods/log *subresource*, which a
// controller-runtime client.Client cannot serve at all — only a typed
// clientset can — and because a fake clientset is far more awkward in tests
// than a two-line stub.
type PodLogFetcher interface {
	Tail(ctx context.Context, namespace, podName, containerName string, previous bool, tailLines int64) (string, error)
}

// EventLister lists the Events recorded against one named object in a
// namespace.
type EventLister interface {
	ListFor(ctx context.Context, namespace, objectName string) ([]corev1.Event, error)
}

// EvidenceCollector assembles the Logs/Events evidence for one incident.
// A nil *EvidenceCollector collects nothing and is safe to call, which keeps
// the controller runnable (escalate-only) when evidence collection is not
// wired up.
type EvidenceCollector struct {
	Logs   PodLogFetcher
	Events EventLister

	// Now is injectable so event-age filtering is testable without sleeping.
	// nil means time.Now.
	Now func() time.Time
}

// Collect returns the log tail and formatted recent Events for a
// crash-looping pod. It never returns an error — see the package comment
// above on why evidence collection degrades rather than fails.
func (c *EvidenceCollector) Collect(
	ctx context.Context,
	pod *corev1.Pod,
	containerName string,
	ownerDeployment string,
) (string, []string) {
	if c == nil || pod == nil {
		return "", nil
	}
	return c.collectLogs(ctx, pod, containerName), c.collectEvents(ctx, pod, ownerDeployment)
}

func (c *EvidenceCollector) collectLogs(ctx context.Context, pod *corev1.Pod, containerName string) string {
	if c.Logs == nil {
		return ""
	}
	logger := log.FromContext(ctx)

	// previous=true first. CrashLoopBackOff means the current container
	// instance is not running — it is sitting in the back-off delay — so the
	// logs that explain the crash belong to the *terminated* instance. Asking
	// for the current one usually returns nothing, or an error saying the
	// container has not started.
	if out, err := c.Logs.Tail(ctx, pod.Namespace, pod.Name, containerName, true, evidenceLogTailLines); err == nil {
		if strings.TrimSpace(out) != "" {
			return truncateTail(out, evidenceLogMaxBytes)
		}
	} else {
		logger.V(1).Info("Could not read previous container logs, falling back to the current instance",
			"pod", pod.Name, "container", containerName, "error", err.Error())
	}

	out, err := c.Logs.Tail(ctx, pod.Namespace, pod.Name, containerName, false, evidenceLogTailLines)
	if err != nil {
		logger.Error(err, "Could not read container logs — classifying on Events alone",
			"pod", pod.Name, "container", containerName)
		return ""
	}
	return truncateTail(out, evidenceLogMaxBytes)
}

func (c *EvidenceCollector) collectEvents(ctx context.Context, pod *corev1.Pod, ownerDeployment string) []string {
	if c.Events == nil {
		return nil
	}
	logger := log.FromContext(ctx)
	now := c.now()

	// The Pod's own Events are not enough. Subhashini's bad_deploy guard calls
	// hasRecentDeploymentEvidence, which looks for rollout wording — "Scaled up
	// replica set", "new revision" — and Kubernetes records those against the
	// Deployment and its ReplicaSets, never against the Pod. Querying only the
	// Pod would make that guard permanently unsatisfiable.
	names := []string{pod.Name}
	for _, ref := range pod.OwnerReferences {
		if ref.Kind == kindReplicaSet {
			names = append(names, ref.Name)
		}
	}
	if ownerDeployment != "" {
		names = append(names, ownerDeployment)
	}

	seen := make(map[types.UID]struct{})
	collected := make([]corev1.Event, 0, evidenceMaxEvents)
	for _, name := range names {
		items, err := c.Events.ListFor(ctx, pod.Namespace, name)
		if err != nil {
			logger.Error(err, "Could not list Events for incident evidence",
				"namespace", pod.Namespace, "object", name)
			continue
		}
		for _, item := range items {
			if _, duplicate := seen[item.UID]; duplicate {
				continue
			}
			// Stale events are actively misleading, not merely useless: a
			// rollout from an hour ago would satisfy the bad_deploy
			// "recent deployment evidence" check for a crash that has
			// nothing to do with it. The guard's own check is a plain
			// substring match with no notion of time, so recency has to be
			// enforced here, at collection.
			if now.Sub(eventTime(item)) > evidenceEventWindow {
				continue
			}
			seen[item.UID] = struct{}{}
			collected = append(collected, item)
		}
	}

	slices.SortFunc(collected, func(a, b corev1.Event) int {
		return eventTime(a).Compare(eventTime(b))
	})
	if len(collected) > evidenceMaxEvents {
		// Keep the newest: the events closest to the crash are the ones that
		// explain it.
		collected = collected[len(collected)-evidenceMaxEvents:]
	}

	formatted := make([]string, 0, len(collected))
	for _, item := range collected {
		formatted = append(formatted, formatEvent(item, now))
	}
	return formatted
}

func (c *EvidenceCollector) now() time.Time {
	if c.Now != nil {
		return c.Now()
	}
	return time.Now()
}

// formatEvent renders one Event as a single evidence line. The age prefix is
// there for the LLM's benefit — "Scaled up replica set" means something very
// different 8 seconds before a crash than 20 minutes before it — and the
// involved object is named because evidence for a Pod, its ReplicaSet, and its
// Deployment all land in the same flat list.
func formatEvent(event corev1.Event, now time.Time) string {
	// Clamped because clock skew between the node that recorded the Event and
	// this process can put an Event microseconds into the future, and
	// "-1s ago" reads as a bug to anyone reading the evidence.
	age := max(now.Sub(eventTime(event)).Truncate(time.Second), 0)
	return fmt.Sprintf("%s ago: %s %s (%s/%s): %s",
		age, event.Type, event.Reason,
		event.InvolvedObject.Kind, event.InvolvedObject.Name,
		strings.TrimSpace(event.Message))
}

// eventTime picks the most meaningful timestamp an Event carries. Events
// written by the legacy core/v1 recorder populate LastTimestamp; the newer
// events.k8s.io path populates EventTime and leaves LastTimestamp zero. Both
// kinds arrive here, so both have to be handled.
func eventTime(event corev1.Event) time.Time {
	if !event.LastTimestamp.IsZero() {
		return event.LastTimestamp.Time
	}
	if !event.EventTime.IsZero() {
		return event.EventTime.Time
	}
	if !event.FirstTimestamp.IsZero() {
		return event.FirstTimestamp.Time
	}
	return event.CreationTimestamp.Time
}

// truncateTail keeps the last maxBytes of s. The tail is kept rather than the
// head because a crash's cause — the panic, the connection error, the exit
// message — is written immediately before the process dies.
func truncateTail(s string, maxBytes int) string {
	if len(s) <= maxBytes {
		return s
	}
	return "...[truncated]...\n" + s[len(s)-maxBytes:]
}

// ClientsetLogFetcher reads pod logs through the pods/log subresource using a
// typed clientset. controller-runtime's client.Client deliberately has no
// support for this: pods/log streams bytes rather than returning an object, so
// it falls outside the typed-object API that client.Client models.
type ClientsetLogFetcher struct {
	Client kubernetes.Interface
}

func (f *ClientsetLogFetcher) Tail(
	ctx context.Context,
	namespace, podName, containerName string,
	previous bool,
	tailLines int64,
) (string, error) {
	if f == nil || f.Client == nil {
		return "", fmt.Errorf("read pod logs: clientset is required")
	}

	stream, err := f.Client.CoreV1().Pods(namespace).GetLogs(podName, &corev1.PodLogOptions{
		Container: containerName,
		Previous:  previous,
		TailLines: &tailLines,
	}).Stream(ctx)
	if err != nil {
		return "", fmt.Errorf("open log stream for %s/%s: %w", namespace, podName, err)
	}
	defer func() { _ = stream.Close() }()

	body, err := io.ReadAll(io.LimitReader(stream, evidenceLogReadCeiling))
	if err != nil {
		return "", fmt.Errorf("read log stream for %s/%s: %w", namespace, podName, err)
	}
	return string(body), nil
}

// eventInvolvedObjectNameField is the field the API server indexes Events by.
// It is a server-side index only: a *cached* controller-runtime client would
// reject this selector unless an index were registered for it on the manager's
// cache, and registering one would mean watching and caching every Event in
// the cluster. Reading through the manager's uncached API reader avoids both.
const eventInvolvedObjectNameField = "involvedObject.name"

// APIReaderEventLister lists Events straight from the API server, bypassing
// the manager's cache — see eventInvolvedObjectNameField for why.
type APIReaderEventLister struct {
	Reader client.Reader
}

func (l *APIReaderEventLister) ListFor(ctx context.Context, namespace, objectName string) ([]corev1.Event, error) {
	if l == nil || l.Reader == nil {
		return nil, fmt.Errorf("list events: reader is required")
	}

	var list corev1.EventList
	if err := l.Reader.List(ctx, &list,
		client.InNamespace(namespace),
		client.MatchingFields{eventInvolvedObjectNameField: objectName},
	); err != nil {
		return nil, fmt.Errorf("list events for %s/%s: %w", namespace, objectName, err)
	}
	return list.Items, nil
}
