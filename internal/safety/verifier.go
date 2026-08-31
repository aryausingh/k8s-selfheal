package safety

import (
	"context"
	"errors"
	"fmt"
	"time"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

const (
	InitialReadinessTimeout = 30 * time.Second
	StabilityWindow         = 60 * time.Second
	DefaultPollInterval     = 5 * time.Second
)

// VerificationTarget contains the frozen event data needed by the verifier and
// the time at which the injected remediation action started.
type VerificationTarget struct {
	OriginalPod     types.NamespacedName
	Deployment      types.NamespacedName
	ContainerName   string
	RestartCount    int32
	ActionStartedAt time.Time
}

// VerificationResult records whether the readiness and stability phases passed.
type VerificationResult struct {
	Recovered    bool
	Reason       string
	RestartCount int32
	PodRef       types.NamespacedName
}

// PodResolver finds the current Pod produced by the target Deployment.
type PodResolver interface {
	Resolve(context.Context, VerificationTarget) (*corev1.Pod, error)
}

// Verifier polls Kubernetes Pod status for the Week 2 readiness and stability rules.
type Verifier struct {
	Reader           client.Reader
	Resolver         PodResolver
	Clock            Clock
	ReadinessTimeout time.Duration
	Window           time.Duration
	PollInterval     time.Duration
}

func NewVerifier(reader client.Reader) *Verifier {
	return &Verifier{
		Reader:           reader,
		Resolver:         &DeploymentPodResolver{Reader: reader},
		Clock:            RealClock{},
		ReadinessTimeout: InitialReadinessTimeout,
		Window:           StabilityWindow,
		PollInterval:     DefaultPollInterval,
	}
}

// Verify allows a bounded startup period, then requires one exact Pod to remain
// Ready with a stable per-container restart count for the complete window.
func (v *Verifier) Verify(ctx context.Context, target VerificationTarget) (VerificationResult, error) {
	if err := v.validate(target); err != nil {
		return VerificationResult{}, err
	}

	readinessDeadline := v.Clock.Now().Add(v.ReadinessTimeout)
	var stablePod *corev1.Pod
	var baseline int32

	for {
		pod, err := v.Resolver.Resolve(ctx, target)
		if err != nil && !errors.Is(err, ErrDeploymentPodNotFound) {
			return VerificationResult{}, fmt.Errorf("resolve deployment pod: %w", err)
		}
		if err == nil {
			restartCount, found := containerRestartCount(pod, target.ContainerName)
			if podReady(pod) && found {
				stablePod = pod.DeepCopy()
				baseline = restartCount
				break
			}
		}

		now := v.Clock.Now()
		if !now.Before(readinessDeadline) {
			return VerificationResult{
				Reason: "pod did not become Ready before initial readiness timeout",
			}, nil
		}
		if err := v.wait(ctx, minDuration(v.PollInterval, readinessDeadline.Sub(now))); err != nil {
			return VerificationResult{}, err
		}
	}

	stableRef := types.NamespacedName{Name: stablePod.Name, Namespace: stablePod.Namespace}
	stableUID := stablePod.UID
	deadline := v.Clock.Now().Add(v.Window)
	current := stablePod
	for {
		restartCount, found := containerRestartCount(current, target.ContainerName)
		if !found {
			return failedVerification(current, restartCount, "container status disappeared during stability window"), nil
		}
		if current.UID != stableUID || current.Name != stableRef.Name || current.Namespace != stableRef.Namespace {
			return failedVerification(current, restartCount, "verified pod identity changed during stability window"), nil
		}
		if !podReady(current) {
			return failedVerification(current, restartCount, "pod lost Ready during stability window"), nil
		}
		if restartCount > baseline {
			return failedVerification(current, restartCount, "restart count increased during stability window"), nil
		}

		now := v.Clock.Now()
		if !now.Before(deadline) {
			return VerificationResult{
				Recovered:    true,
				Reason:       "pod remained Ready with stable per-container restart count",
				RestartCount: restartCount,
				PodRef:       stableRef,
			}, nil
		}
		if err := v.wait(ctx, minDuration(v.PollInterval, deadline.Sub(now))); err != nil {
			return VerificationResult{}, err
		}

		current = &corev1.Pod{}
		if err := v.Reader.Get(ctx, stableRef, current); err != nil {
			return VerificationResult{
				Reason: "verified pod disappeared during stability window",
				PodRef: stableRef,
			}, nil
		}
	}
}

func (v *Verifier) validate(target VerificationTarget) error {
	if v.Reader == nil {
		return fmt.Errorf("verify pod: Kubernetes reader is required")
	}
	if v.Resolver == nil {
		return fmt.Errorf("verify pod: resolver is required")
	}
	if v.Clock == nil {
		return fmt.Errorf("verify pod: clock is required")
	}
	if target.OriginalPod.Name == "" || target.OriginalPod.Namespace == "" {
		return fmt.Errorf("verify pod: original pod name and namespace are required")
	}
	if target.Deployment.Name == "" || target.Deployment.Namespace == "" {
		return fmt.Errorf("verify pod: deployment name and namespace are required")
	}
	if target.ContainerName == "" {
		return fmt.Errorf("verify pod: container name is required")
	}
	if target.RestartCount < 0 {
		return fmt.Errorf("verify pod: restart count cannot be negative")
	}
	if target.ActionStartedAt.IsZero() {
		return fmt.Errorf("verify pod: action start time is required")
	}
	if v.ReadinessTimeout <= 0 {
		return fmt.Errorf("verify pod: readiness timeout must be positive")
	}
	if v.Window <= 0 {
		return fmt.Errorf("verify pod: stability window must be positive")
	}
	if v.PollInterval <= 0 {
		return fmt.Errorf("verify pod: poll interval must be positive")
	}
	return nil
}

func (v *Verifier) wait(ctx context.Context, delay time.Duration) error {
	select {
	case <-ctx.Done():
		return fmt.Errorf("verify pod: %w", ctx.Err())
	case <-v.Clock.After(delay):
		return nil
	}
}

func failedVerification(pod *corev1.Pod, restartCount int32, reason string) VerificationResult {
	return VerificationResult{
		Reason:       reason,
		RestartCount: restartCount,
		PodRef:       types.NamespacedName{Name: pod.Name, Namespace: pod.Namespace},
	}
}

func minDuration(left, right time.Duration) time.Duration {
	if left < right {
		return left
	}
	return right
}

func podReady(pod *corev1.Pod) bool {
	for _, condition := range pod.Status.Conditions {
		if condition.Type == corev1.PodReady {
			return condition.Status == corev1.ConditionTrue
		}
	}
	return false
}

func containerRestartCount(pod *corev1.Pod, containerName string) (int32, bool) {
	for _, status := range pod.Status.ContainerStatuses {
		if status.Name == containerName {
			return status.RestartCount, true
		}
	}
	return 0, false
}
