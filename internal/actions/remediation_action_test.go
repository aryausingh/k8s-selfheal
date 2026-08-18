package actions

import (
	"context"
	"testing"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	"github.com/aryausingh/k8s-selfheal/internal/contracts"
)

func TestRestartPodAction_Name(t *testing.T) {
	if got := (&RestartPodAction{}).Name(); got != "restart_pod" {
		t.Errorf("Name() = %q, want %q", got, "restart_pod")
	}
}

func TestRolloutUndoAction_Name(t *testing.T) {
	if got := (&RolloutUndoAction{}).Name(); got != "rollout_undo" {
		t.Errorf("Name() = %q, want %q", got, "rollout_undo")
	}
}

func TestRestartPodAction_Execute_DelegatesToRestartPod(t *testing.T) {
	pod := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: testPodName, Namespace: testNamespace}}
	c := fake.NewClientBuilder().WithScheme(testScheme(t)).WithObjects(pod).Build()
	action := &RestartPodAction{Client: c}
	event := contracts.DetectionEvent{PodName: testPodName, Namespace: testNamespace}

	if err := action.Execute(context.Background(), event); err != nil {
		t.Fatalf("Execute() error = %v, want nil", err)
	}

	err := c.Get(context.Background(), client.ObjectKey{Namespace: testNamespace, Name: testPodName}, &corev1.Pod{})
	if !apierrors.IsNotFound(err) {
		t.Errorf("expected pod to be deleted, Get() error = %v", err)
	}
}

func TestRolloutUndoAction_Execute_DelegatesToRolloutUndo(t *testing.T) {
	c := fake.NewClientBuilder().WithScheme(testScheme(t)).Build()
	action := &RolloutUndoAction{Client: c}
	event := contracts.DetectionEvent{Namespace: testNamespace} // no OwnerDeployment

	err := action.Execute(context.Background(), event)
	if err == nil {
		t.Fatal("Execute() error = nil, want error for missing owner deployment")
	}
}
