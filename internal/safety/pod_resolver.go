package safety

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"slices"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// ErrDeploymentPodNotFound means the Deployment has no eligible original or
// post-action replacement Pod yet. The verifier treats this as not Ready and
// keeps polling until the initial readiness timeout.
var ErrDeploymentPodNotFound = errors.New("deployment pod not found")

// DeploymentPodResolver follows Deployment -> ReplicaSet -> Pod ownership so
// label-compatible Pods owned by another workload cannot satisfy verification.
type DeploymentPodResolver struct {
	Reader client.Reader
}

func (r *DeploymentPodResolver) Resolve(
	ctx context.Context,
	target VerificationTarget,
) (*corev1.Pod, error) {
	if r.Reader == nil {
		return nil, fmt.Errorf("resolve deployment pod: Kubernetes reader is required")
	}

	deployment := &appsv1.Deployment{}
	if err := r.Reader.Get(ctx, target.Deployment, deployment); err != nil {
		return nil, fmt.Errorf(
			"read deployment %s/%s: %w",
			target.Deployment.Namespace,
			target.Deployment.Name,
			err,
		)
	}
	if deployment.Spec.Selector == nil {
		return nil, fmt.Errorf("resolve deployment pod: deployment selector is required")
	}

	selector, err := metav1.LabelSelectorAsSelector(deployment.Spec.Selector)
	if err != nil {
		return nil, fmt.Errorf("resolve deployment pod: invalid deployment selector: %w", err)
	}

	replicaSets := &appsv1.ReplicaSetList{}
	if err := r.Reader.List(
		ctx,
		replicaSets,
		client.InNamespace(target.Deployment.Namespace),
		client.MatchingLabelsSelector{Selector: selector},
	); err != nil {
		return nil, fmt.Errorf("list deployment ReplicaSets: %w", err)
	}
	ownedReplicaSets := make(map[string]struct{}, len(replicaSets.Items))
	for index := range replicaSets.Items {
		replicaSet := &replicaSets.Items[index]
		if metav1.IsControlledBy(replicaSet, deployment) {
			ownedReplicaSets[string(replicaSet.UID)] = struct{}{}
		}
	}

	pods := &corev1.PodList{}
	if err := r.Reader.List(
		ctx,
		pods,
		client.InNamespace(target.Deployment.Namespace),
		client.MatchingLabelsSelector{Selector: selector},
	); err != nil {
		return nil, fmt.Errorf("list deployment Pods: %w", err)
	}

	candidates := make([]*corev1.Pod, 0, len(pods.Items))
	for index := range pods.Items {
		pod := &pods.Items[index]
		if !eligibleDeploymentPod(pod, ownedReplicaSets, target) {
			continue
		}
		candidates = append(candidates, pod)
	}
	if len(candidates) == 0 {
		return nil, fmt.Errorf(
			"%w for deployment %s/%s",
			ErrDeploymentPodNotFound,
			target.Deployment.Namespace,
			target.Deployment.Name,
		)
	}

	// Both Week 2 actions create rollout Pods. The newest post-action replacement
	// is the deterministic candidate; the original Pod can never satisfy recovery.
	slices.SortFunc(candidates, func(left, right *corev1.Pod) int {
		if !left.CreationTimestamp.Equal(&right.CreationTimestamp) {
			if left.CreationTimestamp.After(right.CreationTimestamp.Time) {
				return -1
			}
			return 1
		}
		return cmp.Compare(right.Name, left.Name)
	})
	return candidates[0].DeepCopy(), nil
}

func eligibleDeploymentPod(
	pod *corev1.Pod,
	ownedReplicaSets map[string]struct{},
	target VerificationTarget,
) bool {
	if pod.DeletionTimestamp != nil || pod.Namespace != target.Deployment.Namespace {
		return false
	}
	controller := metav1.GetControllerOfNoCopy(pod)
	if controller == nil {
		return false
	}
	if _, ok := ownedReplicaSets[string(controller.UID)]; !ok {
		return false
	}
	if !podSpecHasContainer(pod, target.ContainerName) {
		return false
	}
	if pod.Name == target.OriginalPod.Name {
		return false
	}
	return !pod.CreationTimestamp.Time.Before(actionSecond(target.ActionStartedAt))
}

// actionSecond truncates the action start to the precision Kubernetes actually
// reports Pod creation at.
//
// metav1.Time serializes as RFC3339 with *second* granularity, so every
// CreationTimestamp read back from the API server has a zero sub-second part,
// while ActionStartedAt carries full monotonic precision. Comparing them
// directly rejects any replacement created in the same wall-clock second the
// action started — and since RestartPod deletes a Pod and the ReplicaSet
// controller replaces it within milliseconds, that is the normal case, not an
// edge case. The replacement was then ineligible for the entire readiness
// timeout, so a genuinely recovered workload was reported as rolled_back and
// restart_pod could never reach OutcomeRecovered.
//
// One second is the floor on what the API can distinguish, so this is a
// tolerance rather than a fix for a comparison that was merely off. Widening
// it that far is safe here because the original Pod is already excluded by
// name above, and every other candidate must be controller-owned by a
// ReplicaSet of the target Deployment.
func actionSecond(actionStartedAt time.Time) time.Time {
	return actionStartedAt.Truncate(time.Second)
}

func podSpecHasContainer(pod *corev1.Pod, containerName string) bool {
	for _, container := range pod.Spec.Containers {
		if container.Name == containerName {
			return true
		}
	}
	return false
}
