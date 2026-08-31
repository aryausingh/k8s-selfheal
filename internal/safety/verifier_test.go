//nolint:goconst // Repeated literals keep each verifier fixture self-contained.
package safety

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

type fakeClock struct {
	mu  sync.Mutex
	now time.Time
}

func (c *fakeClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *fakeClock) After(delay time.Duration) <-chan time.Time {
	c.mu.Lock()
	c.now = c.now.Add(delay)
	now := c.now
	c.mu.Unlock()

	ch := make(chan time.Time, 1)
	ch <- now
	return ch
}

func (c *fakeClock) Advance(delay time.Duration) {
	c.mu.Lock()
	c.now = c.now.Add(delay)
	c.mu.Unlock()
}

type sequenceResolver struct {
	mu       sync.Mutex
	pods     []*corev1.Pod
	position int
}

func (r *sequenceResolver) Resolve(context.Context, VerificationTarget) (*corev1.Pod, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if len(r.pods) == 0 {
		return nil, ErrDeploymentPodNotFound
	}
	position := r.position
	if position >= len(r.pods) {
		position = len(r.pods) - 1
	}
	r.position++
	return r.pods[position].DeepCopy(), nil
}

type sequenceReader struct {
	mu       sync.Mutex
	pods     []*corev1.Pod
	position int
}

func (r *sequenceReader) Get(
	_ context.Context,
	_ client.ObjectKey,
	object client.Object,
	_ ...client.GetOption,
) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if len(r.pods) == 0 {
		return fmt.Errorf("no pod observations configured")
	}
	position := r.position
	if position >= len(r.pods) {
		position = len(r.pods) - 1
	}
	r.position++

	pod, ok := object.(*corev1.Pod)
	if !ok {
		return fmt.Errorf("object is %T, want *corev1.Pod", object)
	}
	r.pods[position].DeepCopyInto(pod)
	return nil
}

func (r *sequenceReader) List(context.Context, client.ObjectList, ...client.ListOption) error {
	return fmt.Errorf("list is not used by scripted verifier tests")
}

func readyPod(restarts int32) *corev1.Pod {
	return observedPod("replacement-uid", true, restarts, 0)
}

func notReadyPod(restarts int32) *corev1.Pod {
	return observedPod("replacement-uid", false, restarts, 0)
}

func observedPod(
	uid types.UID,
	ready bool,
	appRestarts int32,
	sidecarRestarts int32,
) *corev1.Pod {
	condition := corev1.ConditionFalse
	if ready {
		condition = corev1.ConditionTrue
	}
	return &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: "replacement-pod", Namespace: "shop", UID: uid},
		Spec: corev1.PodSpec{Containers: []corev1.Container{
			{Name: "app"},
			{Name: "sidecar"},
		}},
		Status: corev1.PodStatus{
			Conditions: []corev1.PodCondition{{
				Type:   corev1.PodReady,
				Status: condition,
			}},
			ContainerStatuses: []corev1.ContainerStatus{
				{Name: "app", RestartCount: appRestarts},
				{Name: "sidecar", RestartCount: sidecarRestarts},
			},
		},
	}
}

func verificationTarget() VerificationTarget {
	return VerificationTarget{
		OriginalPod:     types.NamespacedName{Name: "original-pod", Namespace: "shop"},
		Deployment:      types.NamespacedName{Name: "checkout", Namespace: "shop"},
		ContainerName:   "app",
		RestartCount:    3,
		ActionStartedAt: time.Unix(1, 0),
	}
}

func newTestVerifier(
	readinessObservations []*corev1.Pod,
	stabilityObservations []*corev1.Pod,
) (*Verifier, *fakeClock) {
	clock := &fakeClock{now: time.Unix(1, 0)}
	return &Verifier{
		Reader:           &sequenceReader{pods: stabilityObservations},
		Resolver:         &sequenceResolver{pods: readinessObservations},
		Clock:            clock,
		ReadinessTimeout: InitialReadinessTimeout,
		Window:           StabilityWindow,
		PollInterval:     DefaultPollInterval,
	}, clock
}

func TestVerifierRecoversAfterContinuousWindow(t *testing.T) {
	verifier, clock := newTestVerifier([]*corev1.Pod{readyPod(3)}, []*corev1.Pod{readyPod(3)})
	result, err := verifier.Verify(context.Background(), verificationTarget())
	if err != nil {
		t.Fatalf("Verify() error = %v", err)
	}
	if !result.Recovered {
		t.Fatalf("Recovered = false, reason = %q", result.Reason)
	}
	if got := clock.Now(); got != time.Unix(1, 0).Add(StabilityWindow) {
		t.Fatalf("verification ended at %s, want 60-second stability window", got)
	}
}

func TestVerifierAllowsInitialReadinessThenStartsFreshStabilityWindow(t *testing.T) {
	verifier, clock := newTestVerifier(
		[]*corev1.Pod{notReadyPod(0), notReadyPod(0), readyPod(0)},
		[]*corev1.Pod{readyPod(0)},
	)
	result, err := verifier.Verify(context.Background(), verificationTarget())
	if err != nil {
		t.Fatalf("Verify() error = %v", err)
	}
	if !result.Recovered {
		t.Fatalf("Recovered = false, reason = %q", result.Reason)
	}
	if got := clock.Now(); got != time.Unix(1, 0).Add(10*time.Second+StabilityWindow) {
		t.Fatalf("verification ended at %s, want 10s readiness + 60s stability", got)
	}
}

func TestVerifierFailsWhenPodCrashesAtSecond45OfStability(t *testing.T) {
	observations := make([]*corev1.Pod, 0, 9)
	for range 8 {
		observations = append(observations, readyPod(3))
	}
	observations = append(observations, notReadyPod(4))

	verifier, clock := newTestVerifier([]*corev1.Pod{readyPod(3)}, observations)
	result, err := verifier.Verify(context.Background(), verificationTarget())
	if err != nil {
		t.Fatalf("Verify() error = %v", err)
	}
	if result.Recovered {
		t.Fatal("Recovered = true after second-45 crash")
	}
	if got := clock.Now(); got != time.Unix(1, 0).Add(45*time.Second) {
		t.Fatalf("failure observed at %s, want second 45 of stability", got)
	}
}

func TestVerifierTimesOutForPendingPodAfterThirtySeconds(t *testing.T) {
	verifier, clock := newTestVerifier([]*corev1.Pod{notReadyPod(3)}, []*corev1.Pod{readyPod(3)})
	result, err := verifier.Verify(context.Background(), verificationTarget())
	if err != nil {
		t.Fatalf("Verify() error = %v", err)
	}
	if result.Recovered {
		t.Fatal("Pending pod unexpectedly recovered")
	}
	if got := clock.Now(); got != time.Unix(1, 0).Add(InitialReadinessTimeout) {
		t.Fatalf("timeout occurred at %s, want 30 seconds", got)
	}
}

func TestVerifierFailsForFlappingPod(t *testing.T) {
	verifier, clock := newTestVerifier([]*corev1.Pod{readyPod(3)}, []*corev1.Pod{notReadyPod(3)})
	result, err := verifier.Verify(context.Background(), verificationTarget())
	if err != nil {
		t.Fatalf("Verify() error = %v", err)
	}
	if result.Recovered {
		t.Fatal("flapping pod unexpectedly recovered")
	}
	if got := clock.Now(); got != time.Unix(1, 0).Add(DefaultPollInterval) {
		t.Fatalf("flapping failure observed at %s, want first stability poll", got)
	}
}

func TestVerifierKeysRestartCountToNamedContainer(t *testing.T) {
	ready := observedPod("replacement-uid", true, 3, 0)
	sidecarRestarted := observedPod("replacement-uid", true, 3, 20)
	verifier, _ := newTestVerifier([]*corev1.Pod{ready}, []*corev1.Pod{sidecarRestarted})

	result, err := verifier.Verify(context.Background(), verificationTarget())
	if err != nil {
		t.Fatalf("Verify() error = %v", err)
	}
	if !result.Recovered {
		t.Fatalf("named app container was stable, reason = %q", result.Reason)
	}
}

func TestVerifierFailsWhenNamedContainerRestartCountIncreases(t *testing.T) {
	// The source event baseline is 3, but the replacement Pod's counter resets
	// to 0. A 0 -> 1 increase must still fail; comparing to 3 would miss it.
	ready := observedPod("replacement-uid", true, 0, 0)
	appRestarted := observedPod("replacement-uid", true, 1, 0)
	verifier, clock := newTestVerifier([]*corev1.Pod{ready}, []*corev1.Pod{appRestarted})

	result, err := verifier.Verify(context.Background(), verificationTarget())
	if err != nil {
		t.Fatalf("Verify() error = %v", err)
	}
	if result.Recovered {
		t.Fatal("named app container restart unexpectedly recovered")
	}
	if result.Reason != "restart count increased during stability window" {
		t.Fatalf("reason = %q, want named-container restart failure", result.Reason)
	}
	if got := clock.Now(); got != time.Unix(1, 0).Add(DefaultPollInterval) {
		t.Fatalf("restart failure observed at %s, want first stability poll", got)
	}
}

func TestVerifierFailsIfLockedPodIdentityChanges(t *testing.T) {
	recreatedWithSameName := observedPod("different-uid", true, 3, 0)
	verifier, _ := newTestVerifier(
		[]*corev1.Pod{readyPod(3)},
		[]*corev1.Pod{recreatedWithSameName},
	)

	result, err := verifier.Verify(context.Background(), verificationTarget())
	if err != nil {
		t.Fatalf("Verify() error = %v", err)
	}
	if result.Recovered {
		t.Fatal("verification switched to a different Pod UID")
	}
}

var _ client.Reader = (*sequenceReader)(nil)
var _ PodResolver = (*sequenceResolver)(nil)
