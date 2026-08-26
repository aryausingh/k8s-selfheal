package classifier

import (
	"strings"
	"testing"
)

func testIncident() IncidentInput {
	return IncidentInput{
		DetectionEvent: DetectionEvent{
			PodName:         "checkoutservice-abc123",
			Namespace:       "default",
			ContainerName:   "checkoutservice",
			RestartCount:    5,
			OwnerDeployment: "checkoutservice",
		},
		Logs: "test logs",
	}
}

// ------------------------------------------------------------
// VALID / SAFE PROPOSALS
// ------------------------------------------------------------

func TestTransientFailureCanBeAutomated(t *testing.T) {
	input := testIncident()
	input.Logs = "connection refused while contacting payment service"

	proposal := Proposal{
		SubCause:          "transient_failure",
		RecommendedAction: ActionRestartPod,
		Target: Target{
			Kind:      "Pod",
			Namespace: input.Namespace,
			Name:      input.PodName,
		},
		SafeForAutomation: true,
		Reasoning:         "temporary dependency failure",
	}

	result := ValidateProposal(proposal, input)

	if !result.Valid {
		t.Fatalf("expected valid result, got: %s", result.Reason)
	}

	if result.Decision != DecisionAutomate {
		t.Fatalf("expected automate, got: %s", result.Decision)
	}

	if result.Output.RecommendedAction != ActionRestartPod {
		t.Fatalf(
			"expected restart_pod, got: %s",
			result.Output.RecommendedAction,
		)
	}
}

func TestBadDeploymentCanUseRolloutUndo(t *testing.T) {
	input := testIncident()
	input.Logs = "failed to pull image: invalid image version"
	input.Events = []string{
		"Deployment rollout created new revision",
	}

	proposal := Proposal{
		SubCause:          "bad_deploy",
		RecommendedAction: ActionRolloutUndo,
		Target: Target{
			Kind:      "Deployment",
			Namespace: input.Namespace,
			Name:      input.OwnerDeployment,
		},
		SafeForAutomation: true,
		Reasoning:         "bad deployment revision",
	}

	result := ValidateProposal(proposal, input)

	if !result.Valid {
		t.Fatalf(
			"expected rollout proposal to be valid: %s",
			result.Reason,
		)
	}

	if result.Decision != DecisionAutomate {
		t.Fatalf(
			"expected automate, got: %s",
			result.Decision,
		)
	}

	if result.Output.RecommendedAction != ActionRolloutUndo {
		t.Fatalf(
			"expected rollout_undo, got: %s",
			result.Output.RecommendedAction,
		)
	}
}

func TestBadDeploymentWithoutRecentDeploymentEvidenceIsRejected(t *testing.T) {
	input := testIncident()
	input.Logs = "failed to pull image: invalid image version"

	proposal := Proposal{
		SubCause:          "bad_deploy",
		RecommendedAction: ActionRolloutUndo,
		Target: Target{
			Kind:      "Deployment",
			Namespace: input.Namespace,
			Name:      input.OwnerDeployment,
		},
		SafeForAutomation: true,
		Reasoning:         "bad deployment revision",
	}

	result := ValidateProposal(proposal, input)

	if result.Valid {
		t.Fatal("bad_deploy without recent deployment evidence should be rejected")
	}

	if result.Decision != DecisionFallbackMiss {
		t.Fatalf(
			"expected fallback_miss, got: %s",
			result.Decision,
		)
	}

	if !strings.Contains(result.Reason, "recent deployment or rollout evidence") {
		t.Fatalf(
			"expected missing recent deployment evidence reason, got: %s",
			result.Reason,
		)
	}
}

func TestBadDeploymentWithoutImageFailureEvidenceIsRejected(t *testing.T) {
	input := testIncident()
	input.Events = []string{
		"Deployment rollout created new revision",
	}

	proposal := Proposal{
		SubCause:          "bad_deploy",
		RecommendedAction: ActionRolloutUndo,
		Target: Target{
			Kind:      "Deployment",
			Namespace: input.Namespace,
			Name:      input.OwnerDeployment,
		},
		SafeForAutomation: true,
		Reasoning:         "bad deployment revision",
	}

	result := ValidateProposal(proposal, input)

	if result.Valid {
		t.Fatal("bad_deploy without image failure evidence should be rejected")
	}

	if result.Decision != DecisionFallbackMiss {
		t.Fatalf(
			"expected fallback_miss, got: %s",
			result.Decision,
		)
	}
}

func TestBadDeploymentWithoutEvidenceIsRejected(t *testing.T) {
	input := testIncident()

	proposal := Proposal{
		SubCause:          "bad_deploy",
		RecommendedAction: ActionRolloutUndo,
		Target: Target{
			Kind:      "Deployment",
			Namespace: input.Namespace,
			Name:      input.OwnerDeployment,
		},
		SafeForAutomation: true,
		Reasoning:         "bad deployment revision",
	}

	result := ValidateProposal(proposal, input)

	if result.Valid {
		t.Fatal("bad_deploy without evidence should be rejected")
	}

	if result.Decision != DecisionFallbackMiss {
		t.Fatalf(
			"expected fallback_miss, got: %s",
			result.Decision,
		)
	}
}

func TestOOMIsValidEscalation(t *testing.T) {
	input := testIncident()

	proposal := Proposal{
		SubCause:          "oom_adjacent",
		RecommendedAction: ActionEscalateToHuman,
		Target: Target{
			Kind:      "Pod",
			Namespace: input.Namespace,
			Name:      input.PodName,
		},
		SafeForAutomation: false,
		Reasoning:         "memory limits require investigation",
	}

	result := ValidateProposal(proposal, input)

	if !result.Valid {
		t.Fatalf(
			"expected valid escalation, got: %s",
			result.Reason,
		)
	}

	if result.Decision != DecisionEscalate {
		t.Fatalf(
			"expected escalate, got: %s",
			result.Decision,
		)
	}
}

func TestBadConfigIsValidEscalation(t *testing.T) {
	input := testIncident()

	proposal := Proposal{
		SubCause:          "bad_config",
		RecommendedAction: ActionEscalateToHuman,
		Target: Target{
			Kind:      "Pod",
			Namespace: input.Namespace,
			Name:      input.PodName,
		},
		SafeForAutomation: false,
		Reasoning:         "required configuration is missing",
	}

	result := ValidateProposal(proposal, input)

	if !result.Valid {
		t.Fatalf(
			"expected valid escalation, got: %s",
			result.Reason,
		)
	}

	if result.Decision != DecisionEscalate {
		t.Fatalf(
			"expected escalate, got: %s",
			result.Decision,
		)
	}
}

func TestApplicationPanicIsValidEscalation(t *testing.T) {
	input := testIncident()

	proposal := Proposal{
		SubCause:          "application_panic",
		RecommendedAction: ActionEscalateToHuman,
		Target: Target{
			Kind:      "Pod",
			Namespace: input.Namespace,
			Name:      input.PodName,
		},
		SafeForAutomation: false,
		Reasoning:         "application panic requires investigation",
	}

	result := ValidateProposal(proposal, input)

	if !result.Valid {
		t.Fatalf(
			"expected valid escalation, got: %s",
			result.Reason,
		)
	}

	if result.Decision != DecisionEscalate {
		t.Fatalf(
			"expected escalate, got: %s",
			result.Decision,
		)
	}
}

func TestUnknownIsValidEscalation(t *testing.T) {
	input := testIncident()

	proposal := Proposal{
		SubCause:          "unknown",
		RecommendedAction: ActionEscalateToHuman,
		Target: Target{
			Kind:      "Pod",
			Namespace: input.Namespace,
			Name:      input.PodName,
		},
		SafeForAutomation: false,
		Reasoning:         "insufficient evidence",
	}

	result := ValidateProposal(proposal, input)

	if !result.Valid {
		t.Fatalf(
			"expected valid escalation, got: %s",
			result.Reason,
		)
	}

	if result.Decision != DecisionEscalate {
		t.Fatalf(
			"expected escalate, got: %s",
			result.Decision,
		)
	}
}

// ------------------------------------------------------------
// UNSAFE PROPOSALS MUST BE REJECTED
// ------------------------------------------------------------

func TestUnsafeProposalCannotRequestRestart(t *testing.T) {
	input := testIncident()

	proposal := Proposal{
		SubCause:          "oom_adjacent",
		RecommendedAction: ActionRestartPod,
		Target: Target{
			Kind:      "Pod",
			Namespace: input.Namespace,
			Name:      input.PodName,
		},
		SafeForAutomation: false,
		Reasoning:         "restart anyway",
	}

	result := ValidateProposal(proposal, input)

	if result.Valid {
		t.Fatal(
			"safe_for_automation=false must not allow restart_pod",
		)
	}

	if !strings.Contains(result.Reason, "fallback miss") {
		t.Fatal("expected fallback miss")
	}
}

func TestSafeTransientFailureCannotUseRolloutUndo(t *testing.T) {
	input := testIncident()

	proposal := Proposal{
		SubCause:          "transient_failure",
		RecommendedAction: ActionRolloutUndo,
		Target: Target{
			Kind:      "Deployment",
			Namespace: input.Namespace,
			Name:      input.OwnerDeployment,
		},
		SafeForAutomation: true,
		Reasoning:         "incorrect action",
	}

	result := ValidateProposal(proposal, input)

	if result.Valid {
		t.Fatal(
			"transient failure must not automatically use rollout_undo",
		)
	}
}

func TestBadDeploymentCannotRestartPod(t *testing.T) {
	input := testIncident()
	input.Logs = "failed to pull image: invalid image version"
	input.Events = []string{
		"Deployment rollout created new revision",
	}

	proposal := Proposal{
		SubCause:          "bad_deploy",
		RecommendedAction: ActionRestartPod,
		Target: Target{
			Kind:      "Pod",
			Namespace: input.Namespace,
			Name:      input.PodName,
		},
		SafeForAutomation: true,
		Reasoning:         "incorrect action",
	}

	result := ValidateProposal(proposal, input)

	if result.Valid {
		t.Fatal(
			"bad deployment must not automatically use restart_pod",
		)
	}
}

func TestDangerousActionIsRejected(t *testing.T) {
	input := testIncident()

	proposal := Proposal{
		SubCause:          "transient_failure",
		RecommendedAction: "delete_namespace",
		Target: Target{
			Kind:      "Pod",
			Namespace: input.Namespace,
			Name:      input.PodName,
		},
		SafeForAutomation: true,
		Reasoning:         "unsafe test",
	}

	result := ValidateProposal(proposal, input)

	if result.Valid {
		t.Fatal("dangerous action should be rejected")
	}
}

func TestBadConfigCannotBeAutomated(t *testing.T) {
	input := testIncident()

	proposal := Proposal{
		SubCause:          "bad_config",
		RecommendedAction: ActionRestartPod,
		Target: Target{
			Kind:      "Pod",
			Namespace: input.Namespace,
			Name:      input.PodName,
		},
		SafeForAutomation: true,
		Reasoning:         "unsafe configuration change",
	}

	result := ValidateProposal(proposal, input)

	if result.Valid {
		t.Fatal("bad_config must not be automatically remediated")
	}
}

func TestOOMCannotBeAutomated(t *testing.T) {
	input := testIncident()

	proposal := Proposal{
		SubCause:          "oom_adjacent",
		RecommendedAction: ActionRestartPod,
		Target: Target{
			Kind:      "Pod",
			Namespace: input.Namespace,
			Name:      input.PodName,
		},
		SafeForAutomation: true,
		Reasoning:         "unsafe OOM remediation",
	}

	result := ValidateProposal(proposal, input)

	if result.Valid {
		t.Fatal("OOM must not be automatically remediated")
	}
}

// ------------------------------------------------------------
// TARGET VALIDATION
// ------------------------------------------------------------

func TestWrongPodTargetIsRejected(t *testing.T) {
	input := testIncident()

	proposal := Proposal{
		SubCause:          "transient_failure",
		RecommendedAction: ActionRestartPod,
		Target: Target{
			Kind:      "Pod",
			Namespace: input.Namespace,
			Name:      "another-pod",
		},
		SafeForAutomation: true,
		Reasoning:         "wrong target test",
	}

	result := ValidateProposal(proposal, input)

	if result.Valid {
		t.Fatal("wrong pod target should be rejected")
	}
}

func TestRestartPodCannotTargetDeployment(t *testing.T) {
	input := testIncident()

	proposal := Proposal{
		SubCause:          "transient_failure",
		RecommendedAction: ActionRestartPod,
		Target: Target{
			Kind:      "Deployment",
			Namespace: input.Namespace,
			Name:      input.OwnerDeployment,
		},
		SafeForAutomation: true,
		Reasoning:         "wrong target kind",
	}

	result := ValidateProposal(proposal, input)

	if result.Valid {
		t.Fatal("restart_pod must target a Pod")
	}
}

func TestRolloutUndoCannotTargetPod(t *testing.T) {
	input := testIncident()
	input.Logs = "failed to pull image: invalid image version"
	input.Events = []string{
		"Deployment rollout created new revision",
	}

	proposal := Proposal{
		SubCause:          "bad_deploy",
		RecommendedAction: ActionRolloutUndo,
		Target: Target{
			Kind:      "Pod",
			Namespace: input.Namespace,
			Name:      input.PodName,
		},
		SafeForAutomation: true,
		Reasoning:         "wrong target kind",
	}

	result := ValidateProposal(proposal, input)

	if result.Valid {
		t.Fatal("rollout_undo must target a Deployment")
	}
}

func TestWrongNamespaceIsRejected(t *testing.T) {
	input := testIncident()

	proposal := Proposal{
		SubCause:          "transient_failure",
		RecommendedAction: ActionRestartPod,
		Target: Target{
			Kind:      "Pod",
			Namespace: "other-namespace",
			Name:      input.PodName,
		},
		SafeForAutomation: true,
		Reasoning:         "wrong namespace",
	}

	result := ValidateProposal(proposal, input)

	if result.Valid {
		t.Fatal("wrong namespace should be rejected")
	}
}

func TestWrongDeploymentIsRejected(t *testing.T) {
	input := testIncident()
	input.Logs = "failed to pull image: invalid image version"
	input.Events = []string{
		"Deployment rollout created new revision",
	}

	proposal := Proposal{
		SubCause:          "bad_deploy",
		RecommendedAction: ActionRolloutUndo,
		Target: Target{
			Kind:      "Deployment",
			Namespace: input.Namespace,
			Name:      "wrong-deployment",
		},
		SafeForAutomation: true,
		Reasoning:         "wrong deployment",
	}

	result := ValidateProposal(proposal, input)

	if result.Valid {
		t.Fatal("wrong deployment should be rejected")
	}
}

// ------------------------------------------------------------
// MISSING / MALFORMED PROPOSALS
// ------------------------------------------------------------

func TestEmptySubCauseIsRejected(t *testing.T) {
	input := testIncident()

	proposal := Proposal{
		RecommendedAction: ActionRestartPod,
		Target: Target{
			Kind:      "Pod",
			Namespace: input.Namespace,
			Name:      input.PodName,
		},
		SafeForAutomation: true,
		Reasoning:         "missing sub-cause",
	}

	result := ValidateProposal(proposal, input)

	if result.Valid {
		t.Fatal("empty sub-cause should be rejected")
	}
}

func TestEmptyActionIsRejected(t *testing.T) {
	input := testIncident()

	proposal := Proposal{
		SubCause: "transient_failure",
		Target: Target{
			Kind:      "Pod",
			Namespace: input.Namespace,
			Name:      input.PodName,
		},
		SafeForAutomation: true,
		Reasoning:         "missing action",
	}

	result := ValidateProposal(proposal, input)

	if result.Valid {
		t.Fatal("empty action should be rejected")
	}
}

func TestEmptyTargetKindIsRejected(t *testing.T) {
	input := testIncident()

	proposal := Proposal{
		SubCause:          "transient_failure",
		RecommendedAction: ActionRestartPod,
		Target: Target{
			Namespace: input.Namespace,
			Name:      input.PodName,
		},
		SafeForAutomation: true,
		Reasoning:         "missing target kind",
	}

	result := ValidateProposal(proposal, input)

	if result.Valid {
		t.Fatal("empty target kind should be rejected")
	}
}

func TestEmptyTargetNamespaceIsRejected(t *testing.T) {
	input := testIncident()

	proposal := Proposal{
		SubCause:          "transient_failure",
		RecommendedAction: ActionRestartPod,
		Target: Target{
			Kind: "Pod",
			Name: input.PodName,
		},
		SafeForAutomation: true,
		Reasoning:         "missing namespace",
	}

	result := ValidateProposal(proposal, input)

	if result.Valid {
		t.Fatal("empty target namespace should be rejected")
	}
}

func TestEmptyTargetNameIsRejected(t *testing.T) {
	input := testIncident()

	proposal := Proposal{
		SubCause:          "transient_failure",
		RecommendedAction: ActionRestartPod,
		Target: Target{
			Kind:      "Pod",
			Namespace: input.Namespace,
		},
		SafeForAutomation: true,
		Reasoning:         "missing target name",
	}

	result := ValidateProposal(proposal, input)

	if result.Valid {
		t.Fatal("empty target name should be rejected")
	}
}

func TestEmptyReasoningIsRejected(t *testing.T) {
	input := testIncident()

	proposal := Proposal{
		SubCause:          "transient_failure",
		RecommendedAction: ActionRestartPod,
		Target: Target{
			Kind:      "Pod",
			Namespace: input.Namespace,
			Name:      input.PodName,
		},
		SafeForAutomation: true,
		Reasoning:         "",
	}

	result := ValidateProposal(proposal, input)

	if result.Valid {
		t.Fatal("empty reasoning should be rejected")
	}
}

// ------------------------------------------------------------
// ESCALATION TARGET VALIDATION
// ------------------------------------------------------------

func TestEscalationMustTargetPod(t *testing.T) {
	input := testIncident()

	proposal := Proposal{
		SubCause:          "bad_config",
		RecommendedAction: ActionEscalateToHuman,
		Target: Target{
			Kind:      "Deployment",
			Namespace: input.Namespace,
			Name:      input.OwnerDeployment,
		},
		SafeForAutomation: false,
		Reasoning:         "configuration problem",
	}

	result := ValidateProposal(proposal, input)

	if result.Valid {
		t.Fatal("escalation must target the affected Pod")
	}
}

func TestEscalationMustTargetDetectedPod(t *testing.T) {
	input := testIncident()

	proposal := Proposal{
		SubCause:          "oom_adjacent",
		RecommendedAction: ActionEscalateToHuman,
		Target: Target{
			Kind:      "Pod",
			Namespace: input.Namespace,
			Name:      "different-pod",
		},
		SafeForAutomation: false,
		Reasoning:         "OOM problem",
	}

	result := ValidateProposal(proposal, input)

	if result.Valid {
		t.Fatal("escalation must target the detected Pod")
	}
}

// ------------------------------------------------------------
// SAFE_FOR_AUTOMATION CONSISTENCY
// ------------------------------------------------------------

func TestFalseSafeForAutomationRequiresEscalation(t *testing.T) {
	input := testIncident()

	proposal := Proposal{
		SubCause:          "transient_failure",
		RecommendedAction: ActionRestartPod,
		Target: Target{
			Kind:      "Pod",
			Namespace: input.Namespace,
			Name:      input.PodName,
		},
		SafeForAutomation: false,
		Reasoning:         "not safe",
	}

	result := ValidateProposal(proposal, input)

	if result.Valid {
		t.Fatal(
			"safe_for_automation=false with restart_pod must be rejected",
		)
	}

	if !strings.Contains(
		result.Reason,
		"requires escalate_to_human",
	) {
		t.Fatalf(
			"expected escalation requirement, got: %s",
			result.Reason,
		)
	}
}

func TestValidationReasonCodes(t *testing.T) {
	transientInput := testIncident()
	transientInput.Logs = "connection refused while contacting payment service"

	unknownInput := testIncident()
	unknownInput.Logs = "container exited unexpectedly"

	badDeployMissingRolloutInput := testIncident()
	badDeployMissingRolloutInput.Logs = "failed to pull image: invalid image version"

	validPodTarget := Target{
		Kind:      "Pod",
		Namespace: transientInput.Namespace,
		Name:      transientInput.PodName,
	}

	validDeploymentTarget := Target{
		Kind:      "Deployment",
		Namespace: transientInput.Namespace,
		Name:      transientInput.OwnerDeployment,
	}

	cases := []struct {
		name           string
		input          IncidentInput
		proposal       Proposal
		wantValid      bool
		wantDecision   string
		wantReasonCode string
	}{
		{
			name:  "unsupported action",
			input: transientInput,
			proposal: Proposal{
				SubCause:          "transient_failure",
				RecommendedAction: "delete_namespace",
				Target:            validPodTarget,
				SafeForAutomation: true,
				Reasoning:         "unsupported action",
			},
			wantValid:      false,
			wantDecision:   DecisionFallbackMiss,
			wantReasonCode: ReasonCodeUnsupportedAction,
		},
		{
			name:  "wrong namespace",
			input: transientInput,
			proposal: Proposal{
				SubCause:          "transient_failure",
				RecommendedAction: ActionRestartPod,
				Target: Target{
					Kind:      "Pod",
					Namespace: "other-namespace",
					Name:      transientInput.PodName,
				},
				SafeForAutomation: true,
				Reasoning:         "wrong namespace",
			},
			wantValid:      false,
			wantDecision:   DecisionFallbackMiss,
			wantReasonCode: ReasonCodeNamespaceMismatch,
		},
		{
			name:  "missing target name",
			input: transientInput,
			proposal: Proposal{
				SubCause:          "transient_failure",
				RecommendedAction: ActionRestartPod,
				Target: Target{
					Kind:      "Pod",
					Namespace: transientInput.Namespace,
				},
				SafeForAutomation: true,
				Reasoning:         "missing target name",
			},
			wantValid:      false,
			wantDecision:   DecisionFallbackMiss,
			wantReasonCode: ReasonCodeMissingTargetName,
		},
		{
			name:  "bad deploy missing rollout evidence",
			input: badDeployMissingRolloutInput,
			proposal: Proposal{
				SubCause:          "bad_deploy",
				RecommendedAction: ActionRolloutUndo,
				Target:            validDeploymentTarget,
				SafeForAutomation: true,
				Reasoning:         "bad deployment",
			},
			wantValid:      false,
			wantDecision:   DecisionFallbackMiss,
			wantReasonCode: ReasonCodeMissingDeploymentEvidence,
		},
		{
			name:  "semantic mismatch",
			input: unknownInput,
			proposal: Proposal{
				SubCause:          "transient_failure",
				RecommendedAction: ActionRestartPod,
				Target:            validPodTarget,
				SafeForAutomation: true,
				Reasoning:         "transient failure",
			},
			wantValid:      false,
			wantDecision:   DecisionFallbackMiss,
			wantReasonCode: ReasonCodeSemanticGuardRejected,
		},
		{
			name:  "valid automate",
			input: transientInput,
			proposal: Proposal{
				SubCause:          "transient_failure",
				RecommendedAction: ActionRestartPod,
				Target:            validPodTarget,
				SafeForAutomation: true,
				Reasoning:         "transient failure",
			},
			wantValid:      true,
			wantDecision:   DecisionAutomate,
			wantReasonCode: ReasonCodeNone,
		},
		{
			name:  "valid escalation",
			input: unknownInput,
			proposal: Proposal{
				SubCause:          "unknown",
				RecommendedAction: ActionEscalateToHuman,
				Target: Target{
					Kind:      "Pod",
					Namespace: unknownInput.Namespace,
					Name:      unknownInput.PodName,
				},
				SafeForAutomation: false,
				Reasoning:         "not enough evidence",
			},
			wantValid:      true,
			wantDecision:   DecisionEscalate,
			wantReasonCode: ReasonCodeNone,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			result := ValidateProposal(
				tc.proposal,
				tc.input,
			)

			if result.Valid != tc.wantValid {
				t.Fatalf(
					"Valid: got %v, expected %v. Reason: %s",
					result.Valid,
					tc.wantValid,
					result.Reason,
				)
			}

			if result.Decision != tc.wantDecision {
				t.Fatalf(
					"Decision: got %q, expected %q",
					result.Decision,
					tc.wantDecision,
				)
			}

			if result.ReasonCode != tc.wantReasonCode {
				t.Fatalf(
					"ReasonCode: got %q, expected %q. Reason: %s",
					result.ReasonCode,
					tc.wantReasonCode,
					result.Reason,
				)
			}
		})
	}
}
