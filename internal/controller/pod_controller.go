package controller

import (
	"context"
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
			// TODO: once Owner 2's safety layer exists, replace this log
			// line with: owner2.Remediate(ctx, event)
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

func (r *PodReconciler) ownerDeploymentName(ctx context.Context, pod *corev1.Pod) (string, error) {
	for _, ref := range pod.OwnerReferences {
		if ref.Kind != "ReplicaSet" {
			continue
		}
		var rs appsv1.ReplicaSet
		if err := r.Get(ctx, client.ObjectKey{Namespace: pod.Namespace, Name: ref.Name}, &rs); err != nil {
			return "", err
		}
		for _, rsRef := range rs.OwnerReferences {
			if rsRef.Kind == "Deployment" {
				return rsRef.Name, nil
			}
		}
	}
	return "", nil
}
