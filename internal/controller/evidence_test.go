package controller

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
)

// --- test doubles -------------------------------------------------------

type logTailCall struct {
	namespace string
	pod       string
	container string
	previous  bool
	tailLines int64
}

// stubLogFetcher answers previous=true and previous=false independently, so a
// test can pin exactly which container instance the collector asked for.
type stubLogFetcher struct {
	previous    string
	previousErr error
	current     string
	currentErr  error
	calls       []logTailCall
}

func (f *stubLogFetcher) Tail(
	_ context.Context,
	namespace, podName, containerName string,
	previous bool,
	tailLines int64,
) (string, error) {
	f.calls = append(f.calls, logTailCall{namespace, podName, containerName, previous, tailLines})
	if previous {
		return f.previous, f.previousErr
	}
	return f.current, f.currentErr
}

// stubEventLister returns canned Events per involved-object name and records
// which objects were queried.
type stubEventLister struct {
	byObject map[string][]corev1.Event
	err      error
	queried  []string
}

func (l *stubEventLister) ListFor(_ context.Context, _, objectName string) ([]corev1.Event, error) {
	l.queried = append(l.queried, objectName)
	if l.err != nil {
		return nil, l.err
	}
	return l.byObject[objectName], nil
}

// --- helpers ------------------------------------------------------------

var evidenceNow = time.Date(2026, 9, 5, 12, 0, 0, 0, time.UTC)

func testEvent(uid, reason, message, involvedName string, age time.Duration) corev1.Event {
	return corev1.Event{
		ObjectMeta:     metav1.ObjectMeta{Name: uid, Namespace: testNamespace, UID: types.UID(uid)},
		InvolvedObject: corev1.ObjectReference{Kind: "Pod", Name: involvedName, Namespace: testNamespace},
		Type:           corev1.EventTypeWarning,
		Reason:         reason,
		Message:        message,
		LastTimestamp:  metav1.NewTime(evidenceNow.Add(-age)),
	}
}

// collectorWith builds a collector pinned to evidenceNow so event-age
// filtering is deterministic.
func collectorWith(logs PodLogFetcher, events EventLister) *EvidenceCollector {
	return &EvidenceCollector{
		Logs:   logs,
		Events: events,
		Now:    func() time.Time { return evidenceNow },
	}
}

func joined(events []string) string { return strings.Join(events, "\n") }

// --- log collection -----------------------------------------------------

func TestEvidenceCollector_Nil_CollectsNothingAndDoesNotPanic(t *testing.T) {
	// A nil collector is the wiring-not-configured case. It must degrade to
	// "no evidence" (which escalates downstream), never crash the reconcile.
	var c *EvidenceCollector
	_, _, pod := ownedPod(crashingContainerStatus("main", 3))
	ctx, _ := newTestContext()

	logs, events := c.Collect(ctx, pod, "main", testDeploymentName)

	if logs != "" || events != nil {
		t.Errorf("nil collector returned evidence: logs=%q events=%v", logs, events)
	}
}

func TestEvidenceCollector_PrefersPreviousContainerInstance(t *testing.T) {
	// CrashLoopBackOff means the current instance is not running, so the logs
	// that explain the crash belong to the terminated one.
	fetcher := &stubLogFetcher{previous: "dial tcp: connection refused", current: "should not be used"}
	_, _, pod := ownedPod(crashingContainerStatus("main", 3))
	ctx, _ := newTestContext()

	logs, _ := collectorWith(fetcher, nil).Collect(ctx, pod, "main", testDeploymentName)

	if logs != "dial tcp: connection refused" {
		t.Errorf("logs = %q, want the previous instance's logs", logs)
	}
	if len(fetcher.calls) != 1 {
		t.Fatalf("expected exactly one log read, got %d", len(fetcher.calls))
	}
	if !fetcher.calls[0].previous {
		t.Error("first log read must ask for the previous container instance")
	}
	if fetcher.calls[0].tailLines != evidenceLogTailLines {
		t.Errorf("tailLines = %d, want the bounded %d", fetcher.calls[0].tailLines, evidenceLogTailLines)
	}
}

func TestEvidenceCollector_FallsBackToCurrentInstance(t *testing.T) {
	// A pod on its very first crash has no previous instance yet, and a
	// container that produced no output before dying returns empty rather
	// than an error — both must fall through to the current instance.
	for name, fetcher := range map[string]*stubLogFetcher{
		"previous errors":   {previousErr: fmt.Errorf("previous terminated container not found"), current: "panic: boom"},
		"previous is blank": {previous: "   \n", current: "panic: boom"},
	} {
		t.Run(name, func(t *testing.T) {
			_, _, pod := ownedPod(crashingContainerStatus("main", 3))
			ctx, _ := newTestContext()

			logs, _ := collectorWith(fetcher, nil).Collect(ctx, pod, "main", testDeploymentName)

			if logs != "panic: boom" {
				t.Errorf("logs = %q, want the current instance's logs", logs)
			}
			if len(fetcher.calls) != 2 || fetcher.calls[1].previous {
				t.Errorf("expected a second read with previous=false, got %+v", fetcher.calls)
			}
		})
	}
}

func TestEvidenceCollector_BothLogReadsFail_DegradesToEventsOnly(t *testing.T) {
	// Losing logs must not fail the reconcile — it just means the classifier
	// decides on events alone, and with weaker evidence escalates.
	fetcher := &stubLogFetcher{
		previousErr: fmt.Errorf("no previous instance"),
		currentErr:  fmt.Errorf("container is not running"),
	}
	lister := &stubEventLister{byObject: map[string][]corev1.Event{
		testPodName: {testEvent("e1", "BackOff", "Back-off restarting failed container", testPodName, time.Second)},
	}}
	_, _, pod := ownedPod(crashingContainerStatus("main", 3))
	ctx, sink := newTestContext()

	logs, events := collectorWith(fetcher, lister).Collect(ctx, pod, "main", testDeploymentName)

	if logs != "" {
		t.Errorf("logs = %q, want empty after both reads failed", logs)
	}
	if len(events) != 1 {
		t.Errorf("events must still be collected when logs fail, got %v", events)
	}
	if !sink.has("Could not read container logs") {
		t.Error("a total log-read failure must be logged, not swallowed silently")
	}
}

func TestEvidenceCollector_TruncatesLongLogsKeepingTheTail(t *testing.T) {
	// The cause of a crash is written immediately before the process dies, so
	// an over-long log must lose its head, not its tail.
	const marker = "FATAL: the actual cause"
	fetcher := &stubLogFetcher{previous: strings.Repeat("noise\n", evidenceLogMaxBytes) + marker}
	_, _, pod := ownedPod(crashingContainerStatus("main", 3))
	ctx, _ := newTestContext()

	logs, _ := collectorWith(fetcher, nil).Collect(ctx, pod, "main", testDeploymentName)

	if !strings.HasSuffix(logs, marker) {
		t.Error("truncation dropped the tail, where the cause of the crash lives")
	}
	if !strings.HasPrefix(logs, "...[truncated]...") {
		t.Error("truncated logs must say so, or the classifier reads a partial log as a complete one")
	}
	if len(logs) > evidenceLogMaxBytes+len("...[truncated]...\n") {
		t.Errorf("truncated length %d exceeds the cap", len(logs))
	}
}

// --- event collection ---------------------------------------------------

func TestEvidenceCollector_QueriesPodReplicaSetAndDeployment(t *testing.T) {
	// Rollout wording ("Scaled up replica set") is recorded against the
	// Deployment and ReplicaSet, never the Pod. Querying only the Pod would
	// make Subhashini's bad_deploy evidence check unsatisfiable.
	lister := &stubEventLister{byObject: map[string][]corev1.Event{
		testPodName:        {testEvent("e1", "BackOff", "Back-off restarting failed container", testPodName, 5*time.Second)},
		testReplicaSetName: {testEvent("e2", "SuccessfulCreate", "Created pod: "+testPodName, testReplicaSetName, 20*time.Second)},
		testDeploymentName: {testEvent("e3", "ScalingReplicaSet", "Scaled up replica set "+testReplicaSetName+" to 1", testDeploymentName, 30*time.Second)},
	}}
	_, _, pod := ownedPod(crashingContainerStatus("main", 3))
	ctx, _ := newTestContext()

	_, events := collectorWith(nil, lister).Collect(ctx, pod, "main", testDeploymentName)

	for _, want := range []string{testPodName, testReplicaSetName, testDeploymentName} {
		found := false
		for _, q := range lister.queried {
			if q == want {
				found = true
			}
		}
		if !found {
			t.Errorf("collector never queried Events for %q (queried: %v)", want, lister.queried)
		}
	}
	if !strings.Contains(strings.ToLower(joined(events)), "scaled up replica set") {
		t.Error("the rollout evidence the bad_deploy guard looks for did not reach the evidence list")
	}
}

func TestEvidenceCollector_DropsStaleEvents(t *testing.T) {
	// A rollout from an hour ago is not evidence for a crash happening now —
	// and the guard's own check is a plain substring match with no notion of
	// time, so this window is the only thing keeping it honest.
	lister := &stubEventLister{byObject: map[string][]corev1.Event{
		testPodName: {
			testEvent("fresh", "BackOff", "Back-off restarting failed container", testPodName, time.Minute),
			testEvent("stale", "ScalingReplicaSet", "Scaled up replica set old to 1", testPodName, evidenceEventWindow+time.Minute),
		},
	}}
	_, _, pod := ownedPod(crashingContainerStatus("main", 3))
	ctx, _ := newTestContext()

	_, events := collectorWith(nil, lister).Collect(ctx, pod, "main", testDeploymentName)

	all := joined(events)
	if !strings.Contains(all, "Back-off restarting") {
		t.Error("a fresh event was dropped")
	}
	if strings.Contains(all, "Scaled up replica set old") {
		t.Error("an event older than the window was included — stale rollout evidence can wrongly justify a rollout_undo")
	}
}

func TestEvidenceCollector_OverCap_KeepsTheNewest(t *testing.T) {
	noisy := make([]corev1.Event, 0, evidenceMaxEvents+10)
	for i := range evidenceMaxEvents + 10 {
		// Larger i == older, so "e0" is the newest.
		noisy = append(noisy, testEvent(fmt.Sprintf("e%d", i), "BackOff", fmt.Sprintf("event-%d", i), testPodName, time.Duration(i)*time.Second))
	}
	lister := &stubEventLister{byObject: map[string][]corev1.Event{testPodName: noisy}}
	_, _, pod := ownedPod(crashingContainerStatus("main", 3))
	ctx, _ := newTestContext()

	_, events := collectorWith(nil, lister).Collect(ctx, pod, "main", testDeploymentName)

	if len(events) != evidenceMaxEvents {
		t.Fatalf("collected %d events, want the cap of %d", len(events), evidenceMaxEvents)
	}
	all := joined(events)
	if !strings.Contains(all, "event-0") {
		t.Error("the newest event was dropped — the events closest to the crash are the ones that explain it")
	}
	if strings.Contains(all, fmt.Sprintf("event-%d", evidenceMaxEvents+9)) {
		t.Error("the oldest event survived the cap")
	}
}

func TestEvidenceCollector_DeduplicatesAcrossQueriedObjects(t *testing.T) {
	shared := testEvent("same-uid", "BackOff", "Back-off restarting failed container", testPodName, time.Second)
	lister := &stubEventLister{byObject: map[string][]corev1.Event{
		testPodName:        {shared},
		testDeploymentName: {shared},
	}}
	_, _, pod := ownedPod(crashingContainerStatus("main", 3))
	ctx, _ := newTestContext()

	_, events := collectorWith(nil, lister).Collect(ctx, pod, "main", testDeploymentName)

	if len(events) != 1 {
		t.Errorf("got %d events, want 1 — the same Event UID must not be listed twice", len(events))
	}
}

func TestEvidenceCollector_EventListFailure_IsNotFatal(t *testing.T) {
	lister := &stubEventLister{err: fmt.Errorf("forbidden: events is forbidden")}
	_, _, pod := ownedPod(crashingContainerStatus("main", 3))
	ctx, sink := newTestContext()

	_, events := collectorWith(&stubLogFetcher{previous: "boom"}, lister).Collect(ctx, pod, "main", testDeploymentName)

	if len(events) != 0 {
		t.Errorf("expected no events after a list failure, got %v", events)
	}
	if !sink.has("Could not list Events") {
		t.Error("an Event list failure must be logged — silently classifying on partial evidence hides a missing RBAC rule")
	}
}

// --- formatting ---------------------------------------------------------

func TestFormatEvent_CarriesAgeTypeReasonObjectAndMessage(t *testing.T) {
	event := testEvent("e1", "BackOff", "Back-off restarting failed container workload", testPodName, 90*time.Second)

	got := formatEvent(event, evidenceNow)

	for _, want := range []string{"1m30s ago", "Warning", "BackOff", "Pod/" + testPodName, "Back-off restarting failed container workload"} {
		if !strings.Contains(got, want) {
			t.Errorf("formatted event %q is missing %q", got, want)
		}
	}
}

func TestEventTime_FallsBackAcrossRecorderVersions(t *testing.T) {
	// The legacy core/v1 recorder sets LastTimestamp; the events.k8s.io path
	// sets EventTime and leaves LastTimestamp zero. Both reach this code.
	stamp := evidenceNow.Add(-time.Minute)

	legacy := corev1.Event{LastTimestamp: metav1.NewTime(stamp)}
	modern := corev1.Event{EventTime: metav1.NewMicroTime(stamp)}
	created := corev1.Event{ObjectMeta: metav1.ObjectMeta{CreationTimestamp: metav1.NewTime(stamp)}}

	for name, event := range map[string]corev1.Event{"legacy": legacy, "modern": modern, "creation only": created} {
		if got := eventTime(event); !got.Equal(stamp) {
			t.Errorf("%s: eventTime = %v, want %v", name, got, stamp)
		}
	}
}
