package controller

import (
	"context"
	"sync"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/runtime"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"

	"github.com/aryausingh/k8s-selfheal/internal/contracts"
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
				logger.Error(err, "could not resolve owner deployment, skipping remediation", "pod", pod.Name)
				break
			}
			if deploymentName == "" {
				logger.Info("no owner deployment resolved, skipping remediation", "pod", pod.Name)
				break
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
			// TODO(Task 6 — blocked on Subhashini's classifier package
			// landing in the repo): the gate itself is implemented and
			// tested (ShouldEscalate, escalation.go); only the call that
			// produces its inputs is missing. Once her package lands:
			//
			//   proposal, err := classifier.Classify(ctx, event)
			//   if ShouldEscalate(proposal.SafeForAutomation, err) {
			//       // record/escalate for human review, do not remediate
			//       r.finishRemediation(event.Namespace, event.OwnerDeployment)
			//       break
			//   }
			//
			// TODO(Task 4 — blocked on Owner 2's Remediate() landing in the
			// repo): replace this placeholder with the real call, e.g.:
			//
			//   go func() {
			//       defer r.finishRemediation(event.Namespace, event.OwnerDeployment)
			//       outcome, err := service.Remediate(r.ManagerCtx, event)
			//       ...
			//   }()
			//
			// Whether that call should run synchronously inside Reconcile
			// (blocking this worker for up to InitialReadinessTimeout +
			// StabilityWindow) or be dispatched async as sketched above is a
			// coordination question for Ananya, not something to decide
			// unilaterally here — see the Task 3/4 notes in the walkthrough.
			// For now there is nothing to hold the guard open for, so it is
			// released immediately to avoid leaking a permanently-blocked
			// entry.
			r.finishRemediation(event.Namespace, event.OwnerDeployment)
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
