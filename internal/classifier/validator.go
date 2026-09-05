package classifier

import (
	"fmt"
	"strings"
)

var allowedExecutableActions = map[string]struct{}{
	ActionRestartPod:  {},
	ActionRolloutUndo: {},
}

var allowedResponseActions = map[string]struct{}{
	ActionRestartPod:      {},
	ActionRolloutUndo:     {},
	ActionEscalateToHuman: {},
}

var allowedSubCauses = map[string]struct{}{
	"transient_failure": {},
	"bad_deploy":        {},
	"bad_config":        {},
	"application_panic": {},
	"oom_adjacent":      {},
	"unknown":           {},
}

// ValidateProposal validates the LLM's triage result against
// the actual incident that was sent to the classifier.
// ValidateProposal validates the LLM's triage result against
// the actual incident that was sent to the classifier.
func ValidateProposal(
	proposal Proposal,
	input IncidentInput,
) ValidationResult {
	normalizeProposal(&proposal)

	if _, ok := allowedSubCauses[proposal.SubCause]; !ok {
		return invalidResult(
			proposal,
			ReasonCodeUnsupportedSubCause,
			fmt.Sprintf(
				"unsupported sub-cause: %s",
				proposal.SubCause,
			),
		)
	}

	if _, ok := allowedResponseActions[proposal.RecommendedAction]; !ok {
		return invalidResult(
			proposal,
			ReasonCodeUnsupportedAction,
			fmt.Sprintf(
				"recommended action %s is not recognized",
				proposal.RecommendedAction,
			),
		)
	}

	if proposal.Target.Kind == "" {
		return invalidResult(
			proposal,
			ReasonCodeMissingTargetKind,
			"target kind is empty",
		)
	}

	if proposal.Target.Namespace == "" {
		return invalidResult(
			proposal,
			ReasonCodeMissingTargetNamespace,
			"target namespace is empty",
		)
	}

	if proposal.Target.Name == "" {
		return invalidResult(
			proposal,
			ReasonCodeMissingTargetName,
			"target name is empty",
		)
	}

	if proposal.Reasoning == "" {
		return invalidResult(
			proposal,
			ReasonCodeMissingReasoning,
			"reasoning is empty",
		)
	}

	// The model must not redirect the action into another namespace.
	if proposal.Target.Namespace != input.Namespace {
		return invalidResult(
			proposal,
			ReasonCodeNamespaceMismatch,
			fmt.Sprintf(
				"target namespace %s does not match incident namespace %s",
				proposal.Target.Namespace,
				input.Namespace,
			),
		)
	}

	if proposal.SafeForAutomation {
		return validateAutomatableProposal(
			proposal,
			input,
		)
	}

	return validateEscalationProposal(
		proposal,
		input,
	)
}

func validateAutomatableProposal(
	proposal Proposal,
	input IncidentInput,
) ValidationResult {
	if _, ok := allowedExecutableActions[proposal.RecommendedAction]; !ok {
		return invalidResult(
			proposal,
			ReasonCodeUnsafeExecutableAction,
			fmt.Sprintf(
				"action %s cannot be automatically executed",
				proposal.RecommendedAction,
			),
		)
	}

	if failure := validateSemanticConsistency(
		proposal,
		input,
	); failure.reason != "" {
		return invalidResult(
			proposal,
			failure.reasonCode,
			failure.reason,
		)
	}

	switch proposal.RecommendedAction {
	case ActionRestartPod:
		if proposal.SubCause != "transient_failure" {
			return invalidResult(
				proposal,
				ReasonCodeUnsafeExecutableAction,
				"restart_pod is only automatically allowed for a transient failure",
			)
		}

		if proposal.Target.Kind != "Pod" {
			return invalidResult(
				proposal,
				ReasonCodeWrongTargetKind,
				"restart_pod must target a Pod",
			)
		}

		if proposal.Target.Name != input.PodName {
			return invalidResult(
				proposal,
				ReasonCodeWrongTargetName,
				fmt.Sprintf(
					"restart target %s does not match detected pod %s",
					proposal.Target.Name,
					input.PodName,
				),
			)
		}

	case ActionRolloutUndo:
		if proposal.SubCause != "bad_deploy" {
			return invalidResult(
				proposal,
				ReasonCodeUnsafeExecutableAction,
				"rollout_undo is only automatically allowed for a bad deployment",
			)
		}

		if proposal.Target.Kind != "Deployment" {
			return invalidResult(
				proposal,
				ReasonCodeWrongTargetKind,
				"rollout_undo must target a Deployment",
			)
		}

		if strings.TrimSpace(input.OwnerDeployment) == "" {
			return invalidResult(
				proposal,
				ReasonCodeMissingOwnerDeployment,
				"incident does not contain an owner deployment",
			)
		}

		if proposal.Target.Name != input.OwnerDeployment {
			return invalidResult(
				proposal,
				ReasonCodeWrongTargetName,
				fmt.Sprintf(
					"rollout target %s does not match owner deployment %s",
					proposal.Target.Name,
					input.OwnerDeployment,
				),
			)
		}
	}

	return ValidationResult{
		Valid:    true,
		Decision: DecisionAutomate,
		Reason:   "proposal passed automation validation",
		Output:   proposal,
	}
}

func validateEscalationProposal(
	proposal Proposal,
	input IncidentInput,
) ValidationResult {
	if proposal.RecommendedAction != ActionEscalateToHuman {
		return invalidResult(
			proposal,
			ReasonCodeEscalationRequired,
			"safe_for_automation=false requires escalate_to_human",
		)
	}

	// For escalation, target the affected pod so the human receives
	// the exact incident resource.
	if proposal.Target.Kind != "Pod" {
		return invalidResult(
			proposal,
			ReasonCodeWrongTargetKind,
			"an escalation must identify the affected Pod",
		)
	}

	if proposal.Target.Name != input.PodName {
		return invalidResult(
			proposal,
			ReasonCodeWrongTargetName,
			fmt.Sprintf(
				"escalation target %s does not match detected pod %s",
				proposal.Target.Name,
				input.PodName,
			),
		)
	}

	return ValidationResult{
		Valid:    true,
		Decision: DecisionEscalate,
		Reason:   "valid triage result; manual escalation required",
		Output:   proposal,
	}
}

func normalizeProposal(proposal *Proposal) {
	proposal.SubCause =
		strings.TrimSpace(proposal.SubCause)

	proposal.RecommendedAction =
		strings.TrimSpace(proposal.RecommendedAction)

	proposal.Target.Kind =
		strings.TrimSpace(proposal.Target.Kind)

	proposal.Target.Namespace =
		strings.TrimSpace(proposal.Target.Namespace)

	proposal.Target.Name =
		strings.TrimSpace(proposal.Target.Name)

	proposal.Reasoning =
		strings.TrimSpace(proposal.Reasoning)
}

func invalidResult(
	proposal Proposal,
	reasonCode string,
	reason string,
) ValidationResult {
	return ValidationResult{
		Valid:      false,
		Decision:   DecisionFallbackMiss,
		Reason:     "fallback miss: " + reason,
		ReasonCode: reasonCode,
		Output:     proposal,
	}
}

type semanticConsistencyFailure struct {
	reasonCode string
	reason     string
}

// validateSemanticConsistency checks whether the proposed
// sub-cause is supported by the actual incident evidence.
//
// It acts as a deterministic semantic guard between the LLM
// and the automation layer.
func validateSemanticConsistency(
	proposal Proposal,
	input IncidentInput,
) semanticConsistencyFailure {
	evidence := strings.ToLower(
		input.Logs + " " +
			strings.Join(input.Events, " "),
	)

	switch proposal.SubCause {

	case "transient_failure":
		if !containsAny(
			evidence,
			"connection refused",
			"failed to connect",
			"temporarily unavailable",
			"temporary network failure",
			"timeout",
			"network failure",
		) {
			return semanticConsistencyFailure{
				reasonCode: ReasonCodeSemanticGuardRejected,
				reason:     "semantic guard rejected transient_failure: incident evidence does not support a transient dependency or connectivity failure",
			}
		}

	case "bad_deploy":
		// The image-pull indicators below cannot co-occur with the only
		// failure this system detects. A pod that cannot pull its image never
		// reaches CrashLoopBackOff — it sits in ImagePullBackOff, which the
		// detector does not watch — so before "back-off restarting failed
		// container" was added here, bad_deploy was unsatisfiable in practice
		// and rollout_undo could never fire.
		//
		// That indicator alone is weak: every crash-looping pod produces it.
		// It is the hasRecentDeploymentEvidence check immediately below that
		// makes this discriminating — the crash must also coincide with a
		// rollout — and the caller is responsible for only supplying
		// genuinely recent events, since the check itself has no notion of
		// time (internal/controller/evidence.go enforces that window).
		if !containsAny(
			evidence,
			"imagepullbackoff",
			"failed to pull image",
			"invalid image",
			"invalid-version",
			"back-off pulling image",
			"back-off restarting failed container",
		) {
			return semanticConsistencyFailure{
				reasonCode: ReasonCodeSemanticGuardRejected,
				reason:     "semantic guard rejected bad_deploy: incident evidence does not support a deployment or image failure",
			}
		}

		if !hasRecentDeploymentEvidence(input) {
			return semanticConsistencyFailure{
				reasonCode: ReasonCodeMissingDeploymentEvidence,
				reason:     "semantic guard rejected bad_deploy: no recent deployment or rollout evidence",
			}
		}

	case "bad_config":
		if !containsAny(
			evidence,
			"configmap",
			"missing environment",
			"missing env",
			"required environment variable",
			"invalid configuration",
		) {
			return semanticConsistencyFailure{
				reasonCode: ReasonCodeSemanticGuardRejected,
				reason:     "semantic guard rejected bad_config: incident evidence does not support a configuration failure",
			}
		}

	case "oom_adjacent":
		if !containsAny(
			evidence,
			"oomkilled",
			"out of memory",
			"exceeded available memory",
			"exit code 137",
		) {
			return semanticConsistencyFailure{
				reasonCode: ReasonCodeSemanticGuardRejected,
				reason:     "semantic guard rejected oom_adjacent: incident evidence does not support an out-of-memory failure",
			}
		}

	case "application_panic":
		if !containsAny(
			evidence,
			"panic",
			"goroutine",
			"fatal error",
		) {
			return semanticConsistencyFailure{
				reasonCode: ReasonCodeSemanticGuardRejected,
				reason:     "semantic guard rejected application_panic: incident evidence does not support an application panic",
			}
		}

	case "unknown":
		// Unknown is always allowed because insufficient evidence
		// should safely result in escalation rather than automation.
		return semanticConsistencyFailure{}
	}

	return semanticConsistencyFailure{}
}

func containsAny(
	evidence string,
	indicators ...string,
) bool {
	for _, indicator := range indicators {
		if strings.Contains(
			evidence,
			indicator,
		) {
			return true
		}
	}

	return false
}

func hasRecentDeploymentEvidence(input IncidentInput) bool {
	evidence := strings.ToLower(
		input.Logs + " " +
			strings.Join(input.Events, " "),
	)

	return containsAny(
		evidence,
		"deployment updated",
		"deployment rollout",
		"rollout",
		"new revision",
		"revision",
		"replica set created",
		"replicaset created",
		"scaled up replica set",
		"new image deployed",
		"deployment changed",
	)
}
