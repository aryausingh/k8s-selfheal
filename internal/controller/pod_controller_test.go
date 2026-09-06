package controller

import (
	"context"
	"strings"
	"sync"
	"testing"

	"github.com/go-logr/logr"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/log"

	"github.com/aryausingh/k8s-selfheal/internal/contracts"
)

// --- test scaffolding -------------------------------------------------
//
// recordingSink stands in as a controller-runtime logr.LogSink that
// captures every Info()/Error() call, including the structured key/value
// pairs (e.g. "event", <DetectionEvent>), so tests can assert precisely on
// what Reconcile decided rather than just that it ran. The detection-only
// tests in this file predate Task 4/6 landing (see
// remediation_wiring_test.go for the classify/escalate/dispatch behavior)
// and still rely on it for that reason.

type logCall struct {
	msg   string
	kvs   []any
	isErr bool
}

// recordingSink is shared between the synchronous logger.Info/Error calls
// Reconcile makes directly and the goroutine it dispatches for Task 4
// remediation, which logs its own outcome after Reconcile has already
// returned. Those two writers run concurrently from the test's point of
// view, so this needs a real lock, not just append-and-hope.
type recordingSink struct {
	mu    sync.Mutex
	calls []logCall
}

func (s *recordingSink) Init(logr.RuntimeInfo) {}
func (s *recordingSink) Enabled(int) bool      { return true }
func (s *recordingSink) Info(_ int, msg string, kvs ...any) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.calls = append(s.calls, logCall{msg: msg, kvs: kvs})
}
func (s *recordingSink) Error(_ error, msg string, kvs ...any) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.calls = append(s.calls, logCall{msg: msg, kvs: kvs, isErr: true})
}
func (s *recordingSink) WithValues(...any) logr.LogSink { return s }
func (s *recordingSink) WithName(string) logr.LogSink   { return s }

func (s *recordingSink) has(substr string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, c := range s.calls {
		if strings.Contains(c.msg, substr) {
			return true
		}
	}
	return false
}

// event returns the contracts.DetectionEvent logged alongside msg, if any.
func (s *recordingSink) event(msg string) (contracts.DetectionEvent, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, c := range s.calls {
		if c.msg != msg {
			continue
		}
		for i := 0; i+1 < len(c.kvs); i += 2 {
			if c.kvs[i] == "event" {
				if ev, ok := c.kvs[i+1].(contracts.DetectionEvent); ok {
					return ev, true
				}
			}
		}
	}
	return contracts.DetectionEvent{}, false
}

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

// newTestContext returns a context wired to a fresh recordingSink so the
// caller can inspect what Reconcile logged.
func newTestContext() (context.Context, *recordingSink) {
	sink := &recordingSink{}
	return log.IntoContext(context.Background(), logr.New(sink)), sink
}

func crashingContainerStatus(name string, restarts int32) corev1.ContainerStatus {
	return corev1.ContainerStatus{
		Name:         name,
		RestartCount: restarts,
		State: corev1.ContainerState{
			Waiting: &corev1.ContainerStateWaiting{Reason: "CrashLoopBackOff"},
		},
	}
}

func runningContainerStatus(name string) corev1.ContainerStatus {
	return corev1.ContainerStatus{
		Name:  name,
		State: corev1.ContainerState{Running: &corev1.ContainerStateRunning{}},
	}
}

const (
	detectedMsg = "DETECTED CrashLoopBackOff"
	skippedMsg  = "remediation already in flight for this deployment, skipping"

	testNamespace      = "default"
	testDeploymentName = "web"
	testReplicaSetName = "web-abc123"
	testPodName        = "web-abc123-xyz"
)

// ownedPod builds a Pod -> ReplicaSet -> Deployment owner-reference chain
// rooted at testPodName/testReplicaSetName/testDeploymentName, with the
// given container statuses on the pod. Shared by the tests that need
// OwnerDeployment resolution.
func ownedPod(statuses ...corev1.ContainerStatus) (*appsv1.Deployment, *appsv1.ReplicaSet, *corev1.Pod) {
	deploy := &appsv1.Deployment{ObjectMeta: metav1.ObjectMeta{Name: testDeploymentName, Namespace: testNamespace}}
	rs := &appsv1.ReplicaSet{
		ObjectMeta: metav1.ObjectMeta{
			Name: testReplicaSetName, Namespace: testNamespace,
			OwnerReferences: []metav1.OwnerReference{{Kind: kindDeployment, Name: testDeploymentName, Controller: boolPtr(true)}},
		},
	}
	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name: testPodName, Namespace: testNamespace,
			OwnerReferences: []metav1.OwnerReference{{Kind: kindReplicaSet, Name: testReplicaSetName, Controller: boolPtr(true)}},
		},
		Status: corev1.PodStatus{ContainerStatuses: statuses},
	}
	return deploy, rs, pod
}

func boolPtr(b bool) *bool { return &b }

// --- Reconcile: namespace guard ----------------------------------------

func TestReconcile_NamespaceGuard_SkipsExcludedNamespaces(t *testing.T) {
	const podName = "crasher"
	for _, ns := range []string{"kube-system", "kube-node-lease", "local-path-storage"} {
		t.Run(ns, func(t *testing.T) {
			pod := &corev1.Pod{
				ObjectMeta: metav1.ObjectMeta{Name: podName, Namespace: ns},
				Status:     corev1.PodStatus{ContainerStatuses: []corev1.ContainerStatus{crashingContainerStatus("main", 5)}},
			}
			r := &PodReconciler{Client: fake.NewClientBuilder().WithScheme(testScheme(t)).WithObjects(pod).Build()}
			ctx, sink := newTestContext()

			_, err := r.Reconcile(ctx, ctrl.Request{NamespacedName: types.NamespacedName{Namespace: ns, Name: podName}})

			if err != nil {
				t.Fatalf("expected no error, got %v", err)
			}
			// The pod IS crash-looping — if the guard didn't fire first, this
			// would detect. Its absence proves the namespace check runs
			// before anything else, not just that this namespace is quiet.
			if sink.has(detectedMsg) {
				t.Errorf("namespace guard did not prevent detection in excluded namespace %q", ns)
			}
		})
	}
}

// --- Reconcile: detection logic -----------------------------------------

func TestReconcile_HealthyPod_NoDetection(t *testing.T) {
	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: "healthy", Namespace: testNamespace},
		Status:     corev1.PodStatus{ContainerStatuses: []corev1.ContainerStatus{runningContainerStatus("main")}},
	}
	r := &PodReconciler{Client: fake.NewClientBuilder().WithScheme(testScheme(t)).WithObjects(pod).Build()}
	ctx, sink := newTestContext()

	_, err := r.Reconcile(ctx, ctrl.Request{NamespacedName: types.NamespacedName{Namespace: testNamespace, Name: "healthy"}})

	if err != nil {
		t.Fatalf("expected no error, got %v", err)
	}
	if sink.has(detectedMsg) {
		t.Error("expected no detection for a healthy pod")
	}
}

func TestReconcile_MultiContainerLoop_DetectsNonFirstContainer(t *testing.T) {
	// Sidecar (index 0) is healthy; the actual app container (index 1) is
	// crash-looping. If Reconcile only inspected ContainerStatuses[0], as a
	// naive implementation might, this would be missed entirely.
	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: "multi", Namespace: testNamespace},
		Status: corev1.PodStatus{ContainerStatuses: []corev1.ContainerStatus{
			runningContainerStatus("sidecar"),
			crashingContainerStatus("app", 7),
		}},
	}
	r := &PodReconciler{Client: fake.NewClientBuilder().WithScheme(testScheme(t)).WithObjects(pod).Build()}
	ctx, sink := newTestContext()

	_, err := r.Reconcile(ctx, ctrl.Request{NamespacedName: types.NamespacedName{Namespace: testNamespace, Name: "multi"}})

	if err != nil {
		t.Fatalf("expected no error, got %v", err)
	}
	ev, ok := sink.event(detectedMsg)
	if !ok {
		t.Fatal("expected detection of the crash-looping second container")
	}
	if ev.ContainerName != "app" {
		t.Errorf("ContainerName = %q, want %q (must report the crashing container, not the pod as a whole)", ev.ContainerName, "app")
	}
	if ev.RestartCount != 7 {
		t.Errorf("RestartCount = %d, want 7 (must be the crashing container's count)", ev.RestartCount)
	}
}

func TestReconcile_ResolvesOwnerDeployment(t *testing.T) {
	deploy, rs, pod := ownedPod(crashingContainerStatus("main", 3))
	r := &PodReconciler{Client: fake.NewClientBuilder().WithScheme(testScheme(t)).WithObjects(deploy, rs, pod).Build()}
	ctx, sink := newTestContext()

	_, err := r.Reconcile(ctx, ctrl.Request{NamespacedName: types.NamespacedName{Namespace: testNamespace, Name: testPodName}})

	if err != nil {
		t.Fatalf("expected no error, got %v", err)
	}
	ev, ok := sink.event(detectedMsg)
	if !ok {
		t.Fatal("expected detection")
	}
	if ev.OwnerDeployment != testDeploymentName {
		t.Errorf("OwnerDeployment = %q, want %q", ev.OwnerDeployment, testDeploymentName)
	}
	if ev.PodName != testPodName || ev.Namespace != testNamespace {
		t.Errorf("unexpected PodName/Namespace on event: %+v", ev)
	}
}

func TestReconcile_PodGone_NoError(t *testing.T) {
	r := &PodReconciler{Client: fake.NewClientBuilder().WithScheme(testScheme(t)).Build()}
	ctx, _ := newTestContext()

	res, err := r.Reconcile(ctx, ctrl.Request{NamespacedName: types.NamespacedName{Namespace: testNamespace, Name: "gone"}})

	if err != nil {
		t.Fatalf("expected NotFound to be swallowed via client.IgnoreNotFound, got %v", err)
	}
	if res != (ctrl.Result{}) {
		t.Errorf("expected zero-value Result, got %+v", res)
	}
}

// --- Reconcile: in-flight guard integration -----------------------------

func TestReconcile_InFlightGuard_SkipsAlreadyRemediatingDeployment(t *testing.T) {
	deploy, rs, pod := ownedPod(crashingContainerStatus("main", 3))
	r := &PodReconciler{Client: fake.NewClientBuilder().WithScheme(testScheme(t)).WithObjects(deploy, rs, pod).Build()}
	// Simulate remediation already in progress for this Deployment, exactly
	// as if an earlier reconcile for a sibling pod under the same Deployment
	// started it.
	r.inFlight.Store(inFlightKey(testNamespace, testDeploymentName), struct{}{})
	ctx, sink := newTestContext()

	_, err := r.Reconcile(ctx, ctrl.Request{NamespacedName: types.NamespacedName{Namespace: testNamespace, Name: testPodName}})

	if err != nil {
		t.Fatalf("expected no error, got %v", err)
	}
	if !sink.has(skippedMsg) {
		t.Error("expected the in-flight guard to log a skip")
	}
	if _, stillInFlight := r.inFlight.Load(inFlightKey(testNamespace, testDeploymentName)); !stillInFlight {
		t.Error("the guard entry belongs to the earlier remediation and must survive a skipped reconcile — it must only be cleared by whoever started it")
	}
}

// --- guard methods in isolation (Task 3) --------------------------------

func TestInFlightGuard_StartFinishStart(t *testing.T) {
	r := &PodReconciler{}

	if !r.tryStartRemediation("ns1", "dep1") {
		t.Fatal("first call for a fresh key must succeed")
	}
	if r.tryStartRemediation("ns1", "dep1") {
		t.Fatal("second call for the same key while in-flight must be rejected")
	}
	if !r.tryStartRemediation("ns1", "dep2") {
		t.Fatal("a different deployment in the same namespace must not be blocked by an unrelated in-flight entry")
	}

	r.finishRemediation("ns1", "dep1")
	if !r.tryStartRemediation("ns1", "dep1") {
		t.Fatal("after finishRemediation, the key must be acquirable again")
	}
}

func TestInFlightGuard_KeyedByNamespaceAndDeployment_NotPodUID(t *testing.T) {
	// The locked design explicitly rejects a UID-keyed guard: RestartPod
	// deletes the pod and the ReplicaSet controller recreates it with a new
	// UID, so a UID key would fail to recognize the replacement re-crashing
	// as the same remediation. This test pins the key format itself rather
	// than any pod identity.
	if got, want := inFlightKey("ns", "dep"), "ns/dep"; got != want {
		t.Errorf("inFlightKey(%q, %q) = %q, want %q", "ns", "dep", got, want)
	}
}
