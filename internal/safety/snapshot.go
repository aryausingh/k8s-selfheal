package safety

import (
	"context"
	"fmt"

	appsv1 "k8s.io/api/apps/v1"
	apiEquality "k8s.io/apimachinery/pkg/api/equality"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

const deploymentRevisionAnnotation = "deployment.kubernetes.io/revision"

// DeploymentSnapshot is the exact pre-action Deployment state used for rollback.
type DeploymentSnapshot struct {
	Name      string
	Namespace string
	Revision  string
	Spec      appsv1.DeploymentSpec
}

// SnapshotStore captures and restores Deployment state.
type SnapshotStore interface {
	Capture(context.Context, types.NamespacedName) (DeploymentSnapshot, error)
	Restore(context.Context, DeploymentSnapshot) error
}

// KubernetesSnapshotStore uses a controller-runtime client so the same code can
// run against a fake client in unit tests and a real client during integration.
type KubernetesSnapshotStore struct {
	Client client.Client
}

func (s *KubernetesSnapshotStore) Capture(
	ctx context.Context,
	ref types.NamespacedName,
) (DeploymentSnapshot, error) {
	if s.Client == nil {
		return DeploymentSnapshot{}, fmt.Errorf("capture deployment snapshot: client is required")
	}

	deployment := &appsv1.Deployment{}
	if err := s.Client.Get(ctx, ref, deployment); err != nil {
		return DeploymentSnapshot{}, fmt.Errorf("read deployment %s/%s: %w", ref.Namespace, ref.Name, err)
	}

	return DeploymentSnapshot{
		Name:      deployment.Name,
		Namespace: deployment.Namespace,
		Revision:  deployment.Annotations[deploymentRevisionAnnotation],
		Spec:      *deployment.Spec.DeepCopy(),
	}, nil
}

// Restore is the sole rollback mechanism. It reapplies the exact captured spec;
// rollout_undo remains an Owner 1 remediation action, never a rollback path.
// When the live spec already matches, no update is sent, so restore is idempotent.
func (s *KubernetesSnapshotStore) Restore(ctx context.Context, snapshot DeploymentSnapshot) error {
	if s.Client == nil {
		return fmt.Errorf("restore deployment snapshot: client is required")
	}
	if snapshot.Name == "" || snapshot.Namespace == "" {
		return fmt.Errorf("restore deployment snapshot: name and namespace are required")
	}

	ref := types.NamespacedName{Name: snapshot.Name, Namespace: snapshot.Namespace}
	deployment := &appsv1.Deployment{}
	if err := s.Client.Get(ctx, ref, deployment); err != nil {
		return fmt.Errorf("read deployment for restore %s/%s: %w", ref.Namespace, ref.Name, err)
	}
	if apiEquality.Semantic.DeepEqual(deployment.Spec, snapshot.Spec) {
		return nil
	}

	deployment.Spec = *snapshot.Spec.DeepCopy()
	if err := s.Client.Update(ctx, deployment); err != nil {
		return fmt.Errorf("restore deployment %s/%s: %w", ref.Namespace, ref.Name, err)
	}
	return nil
}
