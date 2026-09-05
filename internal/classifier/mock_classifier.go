package classifier

import (
	"context"
	"strings"
)

// MockClassifier is retained for local unit tests.
type MockClassifier struct{}

func (MockClassifier) ProviderName() string {
	return ProviderMock
}

func (MockClassifier) ModelName() string {
	return ProviderMock
}

func (MockClassifier) Classify(
	ctx context.Context,
	input IncidentInput,
) (Proposal, error) {
	select {
	case <-ctx.Done():
		return Proposal{}, ctx.Err()
	default:
	}

	evidence := strings.ToLower(
		input.Logs + " " +
			strings.Join(input.Events, " "),
	)

	switch {
	case strings.Contains(evidence, "oomkilled"),
		strings.Contains(evidence, "out of memory"):

		return escalationProposal(
			input,
			"oom_adjacent",
			"The evidence indicates an out-of-memory termination that requires resource investigation.",
		), nil

	case strings.Contains(evidence, "configmap"),
		strings.Contains(evidence, "missing environment"),
		strings.Contains(evidence, "missing env"),
		strings.Contains(evidence, "invalid configuration"):

		return escalationProposal(
			input,
			"bad_config",
			"The failure requires a configuration change and is not safe for automatic remediation.",
		), nil

	case strings.Contains(evidence, "imagepullbackoff"),
		strings.Contains(evidence, "invalid image"),
		strings.Contains(evidence, "failed to pull image"):

		if strings.TrimSpace(input.OwnerDeployment) == "" {
			return escalationProposal(
				input,
				"bad_deploy",
				"A bad deployment is suspected, but the owning Deployment could not be identified.",
			), nil
		}

		return Proposal{
			SubCause:          "bad_deploy",
			RecommendedAction: ActionRolloutUndo,
			Target: Target{
				Kind:      "Deployment",
				Namespace: input.Namespace,
				Name:      input.OwnerDeployment,
			},
			SafeForAutomation: true,
			Reasoning:         "The events indicate that a bad deployment introduced an invalid container image.",
		}, nil

	// Check transient indicators before checking for the word "panic".
	// Your sample contains both "panic" and "failed to connect".
	case strings.Contains(evidence, "connection refused"),
		strings.Contains(evidence, "failed to connect"),
		strings.Contains(evidence, "temporarily unavailable"),
		strings.Contains(evidence, "timeout"):

		return Proposal{
			SubCause:          "transient_failure",
			RecommendedAction: ActionRestartPod,
			Target: Target{
				Kind:      "Pod",
				Namespace: input.Namespace,
				Name:      input.PodName,
			},
			SafeForAutomation: true,
			Reasoning:         "The evidence indicates a transient dependency or connectivity failure.",
		}, nil

	case strings.Contains(evidence, "panic"):
		return escalationProposal(
			input,
			"application_panic",
			"The application panic may require a code-level fix and is not automatically remediated.",
		), nil

	// A crash loop that began right after a rollout, with nothing above
	// explaining it: the new revision is the suspect, so undo it. This is
	// checked last among the identified causes because the indicator —
	// "back-off restarting failed container" — is present on every
	// crash-looping pod; it only means bad_deploy in combination with recent
	// rollout evidence, and only once the more specific causes above have
	// been ruled out.
	//
	// Recency is the caller's responsibility: hasRecentDeploymentEvidence is
	// a substring match with no notion of time, so it is only as honest as
	// the event window the collector applies
	// (internal/controller/evidence.go).
	case strings.Contains(evidence, "back-off restarting failed container") &&
		hasRecentDeploymentEvidence(input):

		if strings.TrimSpace(input.OwnerDeployment) == "" {
			return escalationProposal(
				input,
				"bad_deploy",
				"The crash loop began after a rollout, but the owning Deployment could not be identified.",
			), nil
		}

		return Proposal{
			SubCause:          "bad_deploy",
			RecommendedAction: ActionRolloutUndo,
			Target: Target{
				Kind:      "Deployment",
				Namespace: input.Namespace,
				Name:      input.OwnerDeployment,
			},
			SafeForAutomation: true,
			Reasoning:         "The container began crash-looping immediately after a rollout, and no other cause is supported by the evidence.",
		}, nil

	default:
		return escalationProposal(
			input,
			"unknown",
			"The available logs and events do not provide enough evidence for safe automation.",
		), nil
	}
}

func escalationProposal(
	input IncidentInput,
	subCause string,
	reasoning string,
) Proposal {
	return Proposal{
		SubCause:          subCause,
		RecommendedAction: ActionEscalateToHuman,
		Target: Target{
			Kind:      "Pod",
			Namespace: input.Namespace,
			Name:      input.PodName,
		},
		SafeForAutomation: false,
		Reasoning:         reasoning,
	}
}
