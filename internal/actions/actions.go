package actions

import (
	"context"
	"fmt"
	"slices"
	"strconv"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/aryausingh/k8s-selfheal/internal/contracts"
)

const revisionAnnotation = "deployment.kubernetes.io/revision"

// RestartPod deletes the crash-looping pod. The Deployment's ReplicaSet
// controller — built into Kubernetes, not your code — notices a missing
// replica and creates a new pod from the current template.
func RestartPod(ctx context.Context, c client.Client, event contracts.DetectionEvent) error {
	pod := &corev1.Pod{}
	if err := c.Get(ctx, client.ObjectKey{Namespace: event.Namespace, Name: event.PodName}, pod); err != nil {
		return client.IgnoreNotFound(err) // already gone — goal already achieved
	}
	if err := c.Delete(ctx, pod); err != nil {
		return client.IgnoreNotFound(err)
	}
	return nil
}

// RolloutUndo reverts the owning Deployment's pod template to its
// immediately preceding revision — same effect as
// `kubectl rollout undo deployment/<name>`.
func RolloutUndo(ctx context.Context, c client.Client, event contracts.DetectionEvent) error {
	if event.OwnerDeployment == "" {
		return fmt.Errorf("rollout undo: event has no owner deployment")
	}

	var deploy appsv1.Deployment
	if err := c.Get(ctx, client.ObjectKey{Namespace: event.Namespace, Name: event.OwnerDeployment}, &deploy); err != nil {
		return fmt.Errorf("rollout undo: fetching deployment: %w", err)
	}

	var rsList appsv1.ReplicaSetList
	if err := c.List(ctx, &rsList,
		client.InNamespace(event.Namespace),
		client.MatchingLabels(deploy.Spec.Selector.MatchLabels),
	); err != nil {
		return fmt.Errorf("rollout undo: listing replicasets: %w", err)
	}

	currentRevision, _ := strconv.Atoi(deploy.Annotations[revisionAnnotation])

	type candidate struct {
		rs       appsv1.ReplicaSet
		revision int
	}
	var prior []candidate
	for _, rs := range rsList.Items {
		revStr, ok := rs.Annotations[revisionAnnotation]
		if !ok {
			continue
		}
		rev, err := strconv.Atoi(revStr)
		if err != nil || rev >= currentRevision {
			continue
		}
		prior = append(prior, candidate{rs, rev})
	}
	if len(prior) == 0 {
		return fmt.Errorf("rollout undo: no prior revision available")
	}

	slices.SortFunc(prior, func(a, b candidate) int { return b.revision - a.revision })
	target := prior[0].rs

	deploy.Spec.Template = target.Spec.Template
	return c.Update(ctx, &deploy)
}
