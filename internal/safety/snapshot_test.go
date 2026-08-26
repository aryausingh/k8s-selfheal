//nolint:goconst // Repeated literals keep each snapshot fixture self-contained.
package safety

import (
	"context"
	"testing"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

type countingClient struct {
	client.Client
	updates int
}

func (c *countingClient) Update(ctx context.Context, object client.Object, options ...client.UpdateOption) error {
	c.updates++
	return c.Client.Update(ctx, object, options...)
}

func TestSnapshotCaptureAndIdempotentRestore(t *testing.T) {
	scheme := runtime.NewScheme()
	if err := appsv1.AddToScheme(scheme); err != nil {
		t.Fatalf("add apps scheme: %v", err)
	}

	replicas := int32(2) // CREATES FAKE SNAPSHOT DATA
	original := &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "checkout",
			Namespace: "shop",
			Annotations: map[string]string{
				deploymentRevisionAnnotation: "7",
			},
		},
		Spec: appsv1.DeploymentSpec{
			Replicas: &replicas,
			Selector: &metav1.LabelSelector{MatchLabels: map[string]string{"app": "checkout"}},
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{Labels: map[string]string{"app": "checkout"}},
				Spec: corev1.PodSpec{Containers: []corev1.Container{{
					Name:  "app",
					Image: "checkout:v1",
				}}},
			},
		},
	}

	baseClient := fake.NewClientBuilder().WithScheme(scheme).WithObjects(original).Build()
	trackedClient := &countingClient{Client: baseClient}
	store := &KubernetesSnapshotStore{Client: trackedClient}
	ref := types.NamespacedName{Name: "checkout", Namespace: "shop"}

	snapshot, err := store.Capture(context.Background(), ref)
	if err != nil {
		t.Fatalf("Capture() error = %v", err)
	}
	if snapshot.Revision != "7" {
		t.Fatalf("Revision = %q, want 7", snapshot.Revision)
	}

	changed := &appsv1.Deployment{}
	if err := baseClient.Get(context.Background(), ref, changed); err != nil {
		t.Fatalf("get Deployment: %v", err)
	} // SNAPSHOT DATA CHANGED
	changed.Spec.Template.Spec.Containers[0].Image = "checkout:broken"
	if err := baseClient.Update(context.Background(), changed); err != nil {
		t.Fatalf("update Deployment: %v", err)
	}

	if err := store.Restore(context.Background(), snapshot); err != nil {
		t.Fatalf("first Restore() error = %v", err)
	}
	if err := store.Restore(context.Background(), snapshot); err != nil {
		t.Fatalf("second Restore() error = %v", err)
	}

	restored := &appsv1.Deployment{}
	if err := baseClient.Get(context.Background(), ref, restored); err != nil {
		t.Fatalf("get restored Deployment: %v", err)
	}
	if got := restored.Spec.Template.Spec.Containers[0].Image; got != "checkout:v1" {
		t.Fatalf("restored image = %q, want checkout:v1", got)
	}
	if trackedClient.updates != 1 {
		t.Fatalf("restore updates = %d, want 1", trackedClient.updates)
	}
}

func TestRestoreRejectsIncompleteSnapshot(t *testing.T) {
	store := &KubernetesSnapshotStore{Client: fake.NewClientBuilder().Build()}
	if err := store.Restore(context.Background(), DeploymentSnapshot{}); err == nil {
		t.Fatal("Restore() accepted an incomplete snapshot")
	}
}
