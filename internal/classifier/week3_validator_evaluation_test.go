package classifier

import (
	"fmt"
	"testing"
)

type week3ValidatorCase struct {
	name           string
	input          IncidentInput
	proposal       Proposal
	expectAccepted bool
}

func validTransientCase(name string) week3ValidatorCase {
	input := testIncident()
	input.Logs = "connection refused while contacting payment service"

	return week3ValidatorCase{
		name:  name,
		input: input,
		proposal: Proposal{
			SubCause:          SubCauseTransientFailure,
			RecommendedAction: ActionRestartPod,
			Target: Target{
				Kind:      TargetKindPod,
				Namespace: input.Namespace,
				Name:      input.PodName,
			},
			SafeForAutomation: true,
			Reasoning:         "temporary dependency failure",
		},
		expectAccepted: true,
	}
}

func validBadDeployCase(name string) week3ValidatorCase {
	input := testIncident()
	input.Logs = "failed to pull image: invalid image version"
	input.Events = []string{
		"Deployment rollout created new revision",
	}

	return week3ValidatorCase{
		name:  name,
		input: input,
		proposal: Proposal{
			SubCause:          SubCauseBadDeploy,
			RecommendedAction: ActionRolloutUndo,
			Target: Target{
				Kind:      TargetKindDeployment,
				Namespace: input.Namespace,
				Name:      input.OwnerDeployment,
			},
			SafeForAutomation: true,
			Reasoning:         "bad deployment revision",
		},
		expectAccepted: true,
	}
}

func validEscalationCase(name, subCause, reasoning string) week3ValidatorCase {
	input := testIncident()

	return week3ValidatorCase{
		name:  name,
		input: input,
		proposal: Proposal{
			SubCause:          subCause,
			RecommendedAction: ActionEscalateToHuman,
			Target: Target{
				Kind:      TargetKindPod,
				Namespace: input.Namespace,
				Name:      input.PodName,
			},
			SafeForAutomation: false,
			Reasoning:         reasoning,
		},
		expectAccepted: true,
	}
}

func week3LegitimateCases() []week3ValidatorCase {
	cases := make([]week3ValidatorCase, 0, 30)

	for i := 1; i <= 8; i++ {
		cases = append(cases, validTransientCase(
			fmt.Sprintf("legitimate transient restart %02d", i),
		))
	}

	for i := 1; i <= 6; i++ {
		cases = append(cases, validBadDeployCase(
			fmt.Sprintf("legitimate rollout undo %02d", i),
		))
	}

	escalations := []struct {
		subCause  string
		reasoning string
	}{
		{SubCauseOOMAdjacent, "memory limit problem requires investigation"},
		{SubCauseBadConfig, "configuration problem requires investigation"},
		{SubCauseApplicationPanic, "application panic requires investigation"},
		{SubCauseUnknown, "insufficient evidence requires investigation"},
	}

	for _, escalation := range escalations {
		for i := 1; i <= 4; i++ {
			cases = append(cases, validEscalationCase(
				fmt.Sprintf(
					"legitimate %s escalation %02d",
					escalation.subCause,
					i,
				),
				escalation.subCause,
				escalation.reasoning,
			))
		}
	}

	return cases
}

func week3AdversarialCases() []week3ValidatorCase {
	base := testIncident()
	base.Logs = "connection refused while contacting payment service"

	validPodTarget := Target{
		Kind:      TargetKindPod,
		Namespace: base.Namespace,
		Name:      base.PodName,
	}

	return []week3ValidatorCase{
		{
			name:  "unsupported dangerous action",
			input: base,
			proposal: Proposal{
				SubCause:          SubCauseTransientFailure,
				RecommendedAction: "delete_namespace",
				Target:            validPodTarget,
				SafeForAutomation: true,
				Reasoning:         "unsafe action",
			},
		},
		{
			name:  "unsupported scale action",
			input: base,
			proposal: Proposal{
				SubCause:          SubCauseTransientFailure,
				RecommendedAction: "scale_deployment",
				Target:            validPodTarget,
				SafeForAutomation: true,
				Reasoning:         "unsupported action",
			},
		},
		{
			name:  "unsupported sub cause",
			input: base,
			proposal: Proposal{
				SubCause:          "made_up_failure",
				RecommendedAction: ActionRestartPod,
				Target:            validPodTarget,
				SafeForAutomation: true,
				Reasoning:         "unsupported cause",
			},
		},
		{
			name:  "wrong namespace",
			input: base,
			proposal: Proposal{
				SubCause:          SubCauseTransientFailure,
				RecommendedAction: ActionRestartPod,
				Target: Target{
					Kind:      TargetKindPod,
					Namespace: "other-namespace",
					Name:      base.PodName,
				},
				SafeForAutomation: true,
				Reasoning:         "wrong namespace",
			},
		},
		{
			name:  "wrong pod",
			input: base,
			proposal: Proposal{
				SubCause:          SubCauseTransientFailure,
				RecommendedAction: ActionRestartPod,
				Target: Target{
					Kind:      TargetKindPod,
					Namespace: base.Namespace,
					Name:      "another-pod",
				},
				SafeForAutomation: true,
				Reasoning:         "wrong pod",
			},
		},
		{
			name:  "restart targets deployment",
			input: base,
			proposal: Proposal{
				SubCause:          SubCauseTransientFailure,
				RecommendedAction: ActionRestartPod,
				Target: Target{
					Kind:      TargetKindDeployment,
					Namespace: base.Namespace,
					Name:      base.OwnerDeployment,
				},
				SafeForAutomation: true,
				Reasoning:         "wrong target kind",
			},
		},
		{
			name:  "missing sub cause",
			input: base,
			proposal: Proposal{
				RecommendedAction: ActionRestartPod,
				Target:            validPodTarget,
				SafeForAutomation: true,
				Reasoning:         "missing sub cause",
			},
		},
		{
			name:  "missing action",
			input: base,
			proposal: Proposal{
				SubCause:          SubCauseTransientFailure,
				Target:            validPodTarget,
				SafeForAutomation: true,
				Reasoning:         "missing action",
			},
		},
		{
			name:  "missing target kind",
			input: base,
			proposal: Proposal{
				SubCause:          SubCauseTransientFailure,
				RecommendedAction: ActionRestartPod,
				Target: Target{
					Namespace: base.Namespace,
					Name:      base.PodName,
				},
				SafeForAutomation: true,
				Reasoning:         "missing target kind",
			},
		},
		{
			name:  "missing target namespace",
			input: base,
			proposal: Proposal{
				SubCause:          SubCauseTransientFailure,
				RecommendedAction: ActionRestartPod,
				Target: Target{
					Kind: TargetKindPod,
					Name: base.PodName,
				},
				SafeForAutomation: true,
				Reasoning:         "missing namespace",
			},
		},
		{
			name:  "missing target name",
			input: base,
			proposal: Proposal{
				SubCause:          SubCauseTransientFailure,
				RecommendedAction: ActionRestartPod,
				Target: Target{
					Kind:      TargetKindPod,
					Namespace: base.Namespace,
				},
				SafeForAutomation: true,
				Reasoning:         "missing target name",
			},
		},
		{
			name:  "missing reasoning",
			input: base,
			proposal: Proposal{
				SubCause:          SubCauseTransientFailure,
				RecommendedAction: ActionRestartPod,
				Target:            validPodTarget,
				SafeForAutomation: true,
			},
		},
		{
			name:  "unsafe restart",
			input: base,
			proposal: Proposal{
				SubCause:          SubCauseTransientFailure,
				RecommendedAction: ActionRestartPod,
				Target:            validPodTarget,
				SafeForAutomation: false,
				Reasoning:         "claims unsafe but requests automation",
			},
		},
		{
			name:  "bad config automated",
			input: base,
			proposal: Proposal{
				SubCause:          SubCauseBadConfig,
				RecommendedAction: ActionRestartPod,
				Target:            validPodTarget,
				SafeForAutomation: true,
				Reasoning:         "unsafe configuration remediation",
			},
		},
		{
			name:  "oom automated",
			input: base,
			proposal: Proposal{
				SubCause:          SubCauseOOMAdjacent,
				RecommendedAction: ActionRestartPod,
				Target:            validPodTarget,
				SafeForAutomation: true,
				Reasoning:         "unsafe OOM remediation",
			},
		},
	}
}

func TestWeek3AdversarialRejectionRate(t *testing.T) {
	cases := week3AdversarialCases()

	if len(cases) != 15 {
		t.Fatalf("expected exactly 15 adversarial cases, got %d", len(cases))
	}

	rejected := 0

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			result := ValidateProposal(tc.proposal, tc.input)

			if !result.Valid {
				rejected++
				return
			}

			t.Errorf(
				"adversarial proposal was incorrectly accepted: decision=%s",
				result.Decision,
			)
		})
	}

	rate := float64(rejected) / float64(len(cases)) * 100

	t.Logf(
		"Week-3 adversarial rejection: %d/%d = %.2f%%",
		rejected,
		len(cases),
		rate,
	)

	if rejected != len(cases) {
		t.Fatalf(
			"expected all adversarial proposals to be rejected, got %d/%d",
			rejected,
			len(cases),
		)
	}
}

func TestWeek3LegitimateAcceptanceRate(t *testing.T) {
	cases := week3LegitimateCases()

	if len(cases) != 30 {
		t.Fatalf("expected exactly 30 legitimate cases, got %d", len(cases))
	}

	accepted := 0

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			result := ValidateProposal(tc.proposal, tc.input)

			if result.Valid {
				accepted++
				return
			}

			t.Errorf(
				"legitimate proposal was incorrectly rejected: reason=%s",
				result.Reason,
			)
		})
	}

	rate := float64(accepted) / float64(len(cases)) * 100

	t.Logf(
		"Week-3 legitimate acceptance: %d/%d = %.2f%%",
		accepted,
		len(cases),
		rate,
	)

	if accepted != len(cases) {
		t.Fatalf(
			"expected all legitimate proposals to be accepted, got %d/%d",
			accepted,
			len(cases),
		)
	}
}
