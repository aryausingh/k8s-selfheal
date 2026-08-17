package actions

import (
	"context"
	"strings"
	"testing"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	"github.com/aryausingh/k8s-selfheal/internal/contracts"
)

const (
	testNamespace         = "default"
	testPodName           = "crasher"
	testDeploymentName    = "demo"
	busyboxImage          = "busybox:1.36"
	workloadContainerName = "workload"
)

func testScheme(t *testing.T) *runtime.Scheme {
	t.Helper()
	scheme := runtime.NewScheme()
	if err := corev1.AddToScheme(scheme); err != nil {
		t.Fatalf("adding corev1 to scheme: %v", err)
	}
	if err := appsv1.AddToScheme(scheme); err != nil {
		t.Fatalf("adding appsv1 to scheme: %v", err)
	}
	return scheme
}

// --- RestartPod ----------------------------------------------------------

func TestRestartPod_DeletesTheCrashingPod(t *testing.T) {
	pod := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: testPodName, Namespace: testNamespace}}
	c := fake.NewClientBuilder().WithScheme(testScheme(t)).WithObjects(pod).Build()
	event := contracts.DetectionEvent{PodName: testPodName, Namespace: testNamespace}

	if err := RestartPod(context.Background(), c, event); err != nil {
		t.Fatalf("RestartPod() error = %v, want nil", err)
	}

	err := c.Get(context.Background(), client.ObjectKey{Namespace: testNamespace, Name: testPodName}, &corev1.Pod{})
	if !apierrors.IsNotFound(err) {
		t.Errorf("expected pod to be deleted, Get() error = %v", err)
	}
}

func TestRestartPod_PodAlreadyGone_TreatedAsSuccess(t *testing.T) {
	// Goal is "no crash-looping pod under this name" — if it's already
	// gone (e.g. a concurrent reconcile beat us to it), that goal is
	// already met and returning an error would be misleading.
	c := fake.NewClientBuilder().WithScheme(testScheme(t)).Build()
	event := contracts.DetectionEvent{PodName: "already-gone", Namespace: testNamespace}

	if err := RestartPod(context.Background(), c, event); err != nil {
		t.Errorf("RestartPod() on a missing pod error = %v, want nil", err)
	}
}

// --- RolloutUndo -----------------------------------------------------------

func withRevision(rev string) map[string]string {
	return map[string]string{revisionAnnotation: rev}
}

func TestRolloutUndo_NoOwnerDeployment_ReturnsError(t *testing.T) {
	c := fake.NewClientBuilder().WithScheme(testScheme(t)).Build()
	event := contracts.DetectionEvent{Namespace: testNamespace} // OwnerDeployment left empty

	err := RolloutUndo(context.Background(), c, event)

	if err == nil || !strings.Contains(err.Error(), "no owner deployment") {
		t.Errorf("RolloutUndo() error = %v, want an error mentioning \"no owner deployment\"", err)
	}
}

func TestRolloutUndo_DeploymentMissing_ReturnsError(t *testing.T) {
	c := fake.NewClientBuilder().WithScheme(testScheme(t)).Build()
	event := contracts.DetectionEvent{Namespace: testNamespace, OwnerDeployment: "does-not-exist"}

	err := RolloutUndo(context.Background(), c, event)

	if err == nil || !strings.Contains(err.Error(), "fetching deployment") {
		t.Errorf("RolloutUndo() error = %v, want an error mentioning \"fetching deployment\"", err)
	}
}

func TestRolloutUndo_NoPriorRevision_ReturnsError(t *testing.T) {
	selector := map[string]string{"app": testDeploymentName}
	deploy := &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{Name: testDeploymentName, Namespace: testNamespace, Annotations: withRevision("1")},
		Spec:       appsv1.DeploymentSpec{Selector: &metav1.LabelSelector{MatchLabels: selector}},
	}
	// Only the current revision's ReplicaSet exists — nothing strictly
	// below revision 1 for RolloutUndo to fall back to.
	rs := &appsv1.ReplicaSet{
		ObjectMeta: metav1.ObjectMeta{Name: "demo-rs1", Namespace: testNamespace, Labels: selector, Annotations: withRevision("1")},
	}
	c := fake.NewClientBuilder().WithScheme(testScheme(t)).WithObjects(deploy, rs).Build()
	event := contracts.DetectionEvent{Namespace: testNamespace, OwnerDeployment: testDeploymentName}

	err := RolloutUndo(context.Background(), c, event)

	if err == nil || !strings.Contains(err.Error(), "no prior revision available") {
		t.Errorf("RolloutUndo() error = %v, want \"no prior revision available\"", err)
	}
}

func TestRolloutUndo_RevertsToImmediatelyPrecedingRevision(t *testing.T) {
	selector := map[string]string{"app": testDeploymentName}
	goodTemplateRev2 := corev1.PodTemplateSpec{
		Spec: corev1.PodSpec{Containers: []corev1.Container{{Name: workloadContainerName, Image: busyboxImage, Command: []string{"sh", "-c", "sleep 3600"}}}},
	}
	oldestTemplateRev1 := corev1.PodTemplateSpec{
		Spec: corev1.PodSpec{Containers: []corev1.Container{{Name: workloadContainerName, Image: busyboxImage, Command: []string{"sh", "-c", "echo rev1-should-not-be-picked; exit 1"}}}},
	}
	badTemplateRev3 := corev1.PodTemplateSpec{
		Spec: corev1.PodSpec{Containers: []corev1.Container{{Name: workloadContainerName, Image: busyboxImage, Command: []string{"sh", "-c", "sleep 1; exit 1"}}}},
	}

	deploy := &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{Name: testDeploymentName, Namespace: testNamespace, Annotations: withRevision("3")},
		Spec: appsv1.DeploymentSpec{
			Selector: &metav1.LabelSelector{MatchLabels: selector},
			Template: badTemplateRev3, // current (broken) state
		},
	}
	rs1 := &appsv1.ReplicaSet{
		ObjectMeta: metav1.ObjectMeta{Name: "demo-rs1", Namespace: testNamespace, Labels: selector, Annotations: withRevision("1")},
		Spec:       appsv1.ReplicaSetSpec{Template: oldestTemplateRev1},
	}
	rs2 := &appsv1.ReplicaSet{
		ObjectMeta: metav1.ObjectMeta{Name: "demo-rs2", Namespace: testNamespace, Labels: selector, Annotations: withRevision("2")},
		Spec:       appsv1.ReplicaSetSpec{Template: goodTemplateRev2},
	}
	rs3 := &appsv1.ReplicaSet{
		ObjectMeta: metav1.ObjectMeta{Name: "demo-rs3", Namespace: testNamespace, Labels: selector, Annotations: withRevision("3")},
		Spec:       appsv1.ReplicaSetSpec{Template: badTemplateRev3},
	}
	c := fake.NewClientBuilder().WithScheme(testScheme(t)).WithObjects(deploy, rs1, rs2, rs3).Build()
	event := contracts.DetectionEvent{Namespace: testNamespace, OwnerDeployment: testDeploymentName}

	if err := RolloutUndo(context.Background(), c, event); err != nil {
		t.Fatalf("RolloutUndo() error = %v, want nil", err)
	}

	var got appsv1.Deployment
	if err := c.Get(context.Background(), client.ObjectKey{Namespace: testNamespace, Name: testDeploymentName}, &got); err != nil {
		t.Fatalf("re-fetching deployment: %v", err)
	}
	gotCmd := got.Spec.Template.Spec.Containers[0].Command
	wantCmd := goodTemplateRev2.Spec.Containers[0].Command
	if !equalStrSlice(gotCmd, wantCmd) {
		t.Errorf("template reverted to command %v, want the immediately preceding revision's command %v (must not jump straight to the oldest revision)", gotCmd, wantCmd)
	}
}

func equalStrSlice(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
