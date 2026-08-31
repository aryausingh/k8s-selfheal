package controller

import (
	"context"
	"fmt"
	"sync"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/runtime"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"

	"github.com/aryausingh/k8s-selfheal/internal/classifier"
	"github.com/aryausingh/k8s-selfheal/internal/contracts"
	"github.com/aryausingh/k8s-selfheal/internal/safety"
)

// PodReconciler watches core Pods and detects CrashLoopBackOff.
type PodReconciler struct {
	client.Client
	Scheme *runtime.Scheme

	// ManagerCtx is the manager-lifetime context (from ctrl.SetupSignalHandler
	// in cmd/main.go), not Reconcile's own ctx. Remediation dispatched into a
	// goroutine (Task 4) must be called with this context — Reconcile's ctx
	// is cancelled the moment that single reconcile returns, which would cut
	// off a still-running remediation (up to ~90s: readiness + stability
	// window) almost immediately.
	ManagerCtx context.Context

	// Classifier resolves a DetectionEvent into Subhashini's Proposal via her
	// ClassifyIncident seam. nil is treated as a misconfiguration, not a
	// silent no-op: Reconcile escalates by default rather than automating
	// blind (see the nil check in Reconcile).
	Classifier classifier.IncidentClassifier

	// Actions maps a RemediationAction's Name() ("restart_pod",
	// "rollout_undo") to the concrete action to inject into Ananya's
	// safety.Service. Reconcile selects an entry by matching against
	// proposal.RecommendedAction — Remediate() itself never decides which
	// action to run (confirmed with Ananya).
	Actions map[string]safety.RemediationAction

	// Snapshots, Verifier, Audit, and Clock are Owner 2's Service
	// dependencies. They're shared across calls because none of them hold
	// per-remediation mutable state; a fresh *safety.Service is built per
	// call with only Action varying, rather than mutating a shared Service
	// mid-flight (per Ananya's review note).
	Snapshots safety.SnapshotStore
	Verifier  safety.PodVerifier
	Audit     safety.AuditWriter
	Clock     safety.Clock

	// inFlight tracks Deployments currently undergoing remediation, keyed by
	// "namespace/OwnerDeployment" — deliberately NOT pod UID. RestartPod
	// deletes the crash-looping pod and the ReplicaSet controller creates a
	// replacement with a brand-new UID; a UID-keyed guard would not recognize
	// that replacement re-crashing as the same in-flight remediation, which
	// defeats the guard's entire purpose. sync.Map is used rather than a
	// mutex+map because this access pattern — many concurrent presence
	// checks (LoadOrStore) per reconcile, comparatively rare writes, keys
	// that come and go rather than accumulate — is exactly what sync.Map is
	// documented to optimize for, and it needs no separate lock to get
	// right under concurrent reconciles (controller-runtime runs
	// MaxConcurrentReconciles workers by default).
	inFlight sync.Map // key: string ("namespace/deployment"), value: struct{}
}

// inFlightKey builds the guard key for a namespace/Deployment pair.
func inFlightKey(namespace, ownerDeployment string) string {
	return namespace + "/" + ownerDeployment
}

// tryStartRemediation marks (namespace, ownerDeployment) as in-flight if it
// is not already. It reports false if remediation is already in progress
// for this Deployment, in which case the caller must skip and not act.
func (r *PodReconciler) tryStartRemediation(namespace, ownerDeployment string) bool {
	_, alreadyInFlight := r.inFlight.LoadOrStore(inFlightKey(namespace, ownerDeployment), struct{}{})
	return !alreadyInFlight
}

// finishRemediation clears the in-flight marker for (namespace,
// ownerDeployment). Must be called exactly once per successful
// tryStartRemediation, on every exit path — success, rollback, or error —
// or the guard leaks and permanently blocks future remediation for that
// Deployment.
func (r *PodReconciler) finishRemediation(namespace, ownerDeployment string) {
	r.inFlight.Delete(inFlightKey(namespace, ownerDeployment))
}

// +kubebuilder:rbac:groups="",resources=pods,verbs=get;list;watch;delete
// +kubebuilder:rbac:groups="",resources=events,verbs=create;patch
// +kubebuilder:rbac:groups=apps,resources=replicasets,verbs=get;list
// +kubebuilder:rbac:groups=apps,resources=deployments,verbs=get;list;watch;update;patch

func (r *PodReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	logger := log.FromContext(ctx)

	if req.Namespace == "kube-system" || req.Namespace == "kube-node-lease" || req.Namespace == "local-path-storage" {
		return ctrl.Result{}, nil
	}

	var pod corev1.Pod
	if err := r.Get(ctx, req.NamespacedName, &pod); err != nil {
		// Pod is gone since the event fired — nothing to reconcile.
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}

	for _, cs := range pod.Status.ContainerStatuses {
		if cs.State.Waiting != nil && cs.State.Waiting.Reason == "CrashLoopBackOff" {
			deploymentName, err := r.ownerDeploymentName(ctx, &pod)
			if err != nil {
				logger.Error(err, "could not resolve owner deployment", "pod", pod.Name)
			}

			event := contracts.DetectionEvent{
				PodName:         pod.Name,
				Namespace:       pod.Namespace,
				ContainerName:   cs.Name,
				RestartCount:    cs.RestartCount,
				OwnerDeployment: deploymentName,
				Timestamp:       time.Now(),
			}

			logger.Info("DETECTED CrashLoopBackOff", "event", event)

			if !r.tryStartRemediation(event.Namespace, event.OwnerDeployment) {
				logger.Info("remediation already in flight for this deployment, skipping",
					"namespace", event.Namespace, "deployment", event.OwnerDeployment)
				break
			}
			if r.Classifier == nil {
				logger.Error(fmt.Errorf("PodReconciler.Classifier is not configured"),
					"cannot classify incident — escalating by default rather than automating blind",
					"namespace", event.Namespace, "pod", event.PodName)
				r.finishRemediation(event.Namespace, event.OwnerDeployment)
				break
			}

			// Task 6: classify, then gate on safe_for_automation.
			//
			// Logs/Events are left empty here — this controller doesn't collect
			// pod logs or Kubernetes Events yet (that needs pods/log RBAC plus
			// an Events watch, neither of which exist in this repo). That's a
			// known, deliberate gap, not an oversight: Subhashini's semantic
			// guard requires supporting evidence for every sub-cause except
			// "unknown", so with no evidence the classifier always falls back
			// to escalate_to_human instead of guessing — the fail-safe
			// direction. Wiring real evidence collection is follow-up work.
			incident := classifier.IncidentInput{
				DetectionEvent: classifier.DetectionEvent{
					PodName:         event.PodName,
					Namespace:       event.Namespace,
					ContainerName:   event.ContainerName,
					RestartCount:    event.RestartCount,
					OwnerDeployment: event.OwnerDeployment,
					Timestamp:       event.Timestamp,
				},
			}
			classification := r.Classifier.ClassifyIncident(ctx, incident)
			proposal := classification.Proposal

			// classifyErr is always nil at this call site: ClassifyIncident
			// never returns an error — a failed or invalid classification is
			// already converted into a safe escalate_to_human Proposal
			// internally (classification.FallbackUsed records that this
			// happened). ShouldEscalate's classifyErr parameter models a
			// transport-style failure this API doesn't expose; it stays in
			// the signature in case a future classifier implementation does.
			if ShouldEscalate(proposal.SafeForAutomation, nil) {
				logger.Info("ESCALATED — not safe for automation",
					"namespace", event.Namespace, "pod", event.PodName,
					"subCause", proposal.SubCause,
					"recommendedAction", proposal.RecommendedAction,
					"reasoning", proposal.Reasoning,
					"fallbackUsed", classification.FallbackUsed,
					"fallbackReason", classification.FallbackReason)
				r.finishRemediation(event.Namespace, event.OwnerDeployment)
				break
			}

			// Task 4: resolve which RemediationAction the proposal selected.
			// Arya picks the action and injects it into a per-call Service —
			// Remediate() itself never decides which action to run — matching
			// proposal.RecommendedAction against each action's Name().
			action, ok := r.Actions[proposal.RecommendedAction]
			if !ok {
				logger.Error(fmt.Errorf("no remediation action registered for %q", proposal.RecommendedAction),
					"classifier recommended an action with no matching implementation — escalating instead",
					"namespace", event.Namespace, "pod", event.PodName)
				r.finishRemediation(event.Namespace, event.OwnerDeployment)
				break
			}

			service := &safety.Service{
				Snapshots: r.Snapshots,
				Verifier:  r.Verifier,
				Action:    action,
				Audit:     r.Audit,
				Clock:     r.Clock,
			}

			// Dispatched into a goroutine against ManagerCtx, not this
			// Reconcile call's ctx: Remediate() can run up to ~90s (30s
			// readiness + 60s stability window, per Ananya's verifier
			// constants), and Reconcile's ctx is cancelled the instant this
			// call returns — that would cut a still-running remediation off
			// almost immediately. The guard is released via defer inside the
			// goroutine so it stays held for the whole Remediate() lifetime,
			// per Ananya's review note, not released early as before.
			go func() {
				defer r.finishRemediation(event.Namespace, event.OwnerDeployment)
				outcome, err := service.Remediate(r.ManagerCtx, event)
				if err != nil {
					logger.Error(err, "remediation failed",
						"namespace", event.Namespace, "deployment", event.OwnerDeployment,
						"action", action.Name())
					return
				}
				logger.Info("remediation finished",
					"namespace", event.Namespace, "deployment", event.OwnerDeployment,
					"action", action.Name(), "result", outcome.Result, "mttr", outcome.MTTR)
			}()
			break
		}
	}
	return ctrl.Result{}, nil
}

func (r *PodReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&corev1.Pod{}).
		Complete(r)
}

// Owner-reference Kind values, shared with tests in this package.
const (
	kindReplicaSet = "ReplicaSet"
	kindDeployment = "Deployment"
)

func (r *PodReconciler) ownerDeploymentName(ctx context.Context, pod *corev1.Pod) (string, error) {
	for _, ref := range pod.OwnerReferences {
		if ref.Kind != kindReplicaSet {
			continue
		}
		var rs appsv1.ReplicaSet
		if err := r.Get(ctx, client.ObjectKey{Namespace: pod.Namespace, Name: ref.Name}, &rs); err != nil {
			return "", err
		}
		for _, rsRef := range rs.OwnerReferences {
			if rsRef.Kind == kindDeployment {
				return rsRef.Name, nil
			}
		}
	}
	return "", nil
}
