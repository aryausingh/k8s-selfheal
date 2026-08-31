package classifier

import (
	"testing"
	"time"
)

func TestLabelledEvaluationSet(t *testing.T) {
	input := IncidentInput{
		DetectionEvent: DetectionEvent{
			PodName:         "checkoutservice-abc123",
			Namespace:       "default",
			ContainerName:   "checkoutservice",
			RestartCount:    5,
			OwnerDeployment: "checkoutservice",
			Timestamp:       time.Now(),
		},
		Logs: `
panic: failed to connect to payment service
goroutine 1 [running]:
main.startApplication()
`,
		Events: []string{
			"Back-off restarting failed container",
			"Container terminated with exit code 2",
		},
	}

	correctPod := Target{
		Kind:      "Pod",
		Namespace: "default",
		Name:      "checkoutservice-abc123",
	}

	correctDeployment := Target{
		Kind:      "Deployment",
		Namespace: "default",
		Name:      "checkoutservice",
	}

	badDeploymentInput := input
	badDeploymentInput.Logs = `
failed to pull image "checkoutservice:invalid-version"
`
	badDeploymentInput.Events = []string{
		"Deployment rollout created new revision",
		"Back-off pulling image",
	}

	type evaluationCase struct {
		name         string
		input        *IncidentInput
		proposal     Proposal
		wantValid    bool
		wantDecision string
	}

	cases := []evaluationCase{
		// 1. Correct automatic restart
		{
			name: "correct transient restart",
			proposal: Proposal{
				SubCause:          "transient_failure",
				RecommendedAction: "restart_pod",
				Target:            correctPod,
				SafeForAutomation: true,
				Reasoning:         "The failure appears temporary.",
			},
			wantValid:    true,
			wantDecision: "automate",
		},

		// 2. Correct deployment undo
		{
			name:  "correct bad deployment undo",
			input: &badDeploymentInput,
			proposal: Proposal{
				SubCause:          "bad_deploy",
				RecommendedAction: "rollout_undo",
				Target:            correctDeployment,
				SafeForAutomation: true,
				Reasoning:         "The latest deployment caused the failure.",
			},
			wantValid:    true,
			wantDecision: "automate",
		},

		// 3. Correct OOM escalation
		{
			name: "correct OOM escalation",
			proposal: Proposal{
				SubCause:          "oom_adjacent",
				RecommendedAction: "escalate_to_human",
				Target:            correctPod,
				SafeForAutomation: false,
				Reasoning:         "Memory configuration requires human review.",
			},
			wantValid:    true,
			wantDecision: "escalate",
		},

		// 4. Correct bad configuration escalation
		{
			name: "correct configuration escalation",
			proposal: Proposal{
				SubCause:          "bad_config",
				RecommendedAction: "escalate_to_human",
				Target:            correctPod,
				SafeForAutomation: false,
				Reasoning:         "Configuration changes require human review.",
			},
			wantValid:    true,
			wantDecision: "escalate",
		},

		// 5. Correct application panic escalation
		{
			name: "correct application panic escalation",
			proposal: Proposal{
				SubCause:          "application_panic",
				RecommendedAction: "escalate_to_human",
				Target:            correctPod,
				SafeForAutomation: false,
				Reasoning:         "The application panic requires investigation.",
			},
			wantValid:    true,
			wantDecision: "escalate",
		},

		// 6. Correct unknown-cause escalation
		{
			name: "correct unknown escalation",
			proposal: Proposal{
				SubCause:          "unknown",
				RecommendedAction: "escalate_to_human",
				Target:            correctPod,
				SafeForAutomation: false,
				Reasoning:         "The cause cannot be determined safely.",
			},
			wantValid:    true,
			wantDecision: "escalate",
		},

		// 7. Wrong namespace
		{
			name: "wrong namespace",
			proposal: Proposal{
				SubCause:          "transient_failure",
				RecommendedAction: "restart_pod",
				Target: Target{
					Kind:      "Pod",
					Namespace: "production",
					Name:      "checkoutservice-abc123",
				},
				SafeForAutomation: true,
				Reasoning:         "Temporary failure.",
			},
			wantValid:    false,
			wantDecision: "fallback_miss",
		},

		// 8. Wrong pod name
		{
			name: "wrong pod target",
			proposal: Proposal{
				SubCause:          "transient_failure",
				RecommendedAction: "restart_pod",
				Target: Target{
					Kind:      "Pod",
					Namespace: "default",
					Name:      "paymentservice-xyz789",
				},
				SafeForAutomation: true,
				Reasoning:         "Temporary failure.",
			},
			wantValid:    false,
			wantDecision: "fallback_miss",
		},

		// 9. restart_pod targeting a Deployment
		{
			name: "restart action with deployment target",
			proposal: Proposal{
				SubCause:          "transient_failure",
				RecommendedAction: "restart_pod",
				Target:            correctDeployment,
				SafeForAutomation: true,
				Reasoning:         "Temporary failure.",
			},
			wantValid:    false,
			wantDecision: "fallback_miss",
		},

		// 10. Wrong deployment name
		{
			name:  "wrong deployment target",
			input: &badDeploymentInput,
			proposal: Proposal{
				SubCause:          "bad_deploy",
				RecommendedAction: "rollout_undo",
				Target: Target{
					Kind:      "Deployment",
					Namespace: "default",
					Name:      "paymentservice",
				},
				SafeForAutomation: true,
				Reasoning:         "The deployment caused the failure.",
			},
			wantValid:    false,
			wantDecision: "fallback_miss",
		},

		// 11. rollout_undo targeting a Pod
		{
			name:  "rollout action with pod target",
			input: &badDeploymentInput,
			proposal: Proposal{
				SubCause:          "bad_deploy",
				RecommendedAction: "rollout_undo",
				Target:            correctPod,
				SafeForAutomation: true,
				Reasoning:         "The deployment caused the failure.",
			},
			wantValid:    false,
			wantDecision: "fallback_miss",
		},

		// 12. Dangerous action
		{
			name: "dangerous namespace deletion",
			proposal: Proposal{
				SubCause:          "transient_failure",
				RecommendedAction: "delete_namespace",
				Target:            correctPod,
				SafeForAutomation: true,
				Reasoning:         "Delete the namespace.",
			},
			wantValid:    false,
			wantDecision: "fallback_miss",
		},

		// 13. Unsupported action
		{
			name: "unsupported scale action",
			proposal: Proposal{
				SubCause:          "transient_failure",
				RecommendedAction: "scale_deployment",
				Target:            correctDeployment,
				SafeForAutomation: true,
				Reasoning:         "Scale the deployment.",
			},
			wantValid:    false,
			wantDecision: "fallback_miss",
		},

		// 14. OOM incorrectly automated
		{
			name: "OOM incorrectly automated",
			proposal: Proposal{
				SubCause:          "oom_adjacent",
				RecommendedAction: "restart_pod",
				Target:            correctPod,
				SafeForAutomation: true,
				Reasoning:         "Restart the pod.",
			},
			wantValid:    false,
			wantDecision: "fallback_miss",
		},

		// 15. Configuration error incorrectly automated
		{
			name: "configuration error incorrectly automated",
			proposal: Proposal{
				SubCause:          "bad_config",
				RecommendedAction: "restart_pod",
				Target:            correctPod,
				SafeForAutomation: true,
				Reasoning:         "Restart the pod.",
			},
			wantValid:    false,
			wantDecision: "fallback_miss",
		},

		// 16. Unknown cause incorrectly automated
		{
			name: "unknown cause incorrectly automated",
			proposal: Proposal{
				SubCause:          "unknown",
				RecommendedAction: "restart_pod",
				Target:            correctPod,
				SafeForAutomation: true,
				Reasoning:         "Restart even though the cause is unknown.",
			},
			wantValid:    false,
			wantDecision: "fallback_miss",
		},

		// 17. Empty target name
		{
			name: "empty target name",
			proposal: Proposal{
				SubCause:          "transient_failure",
				RecommendedAction: "restart_pod",
				Target: Target{
					Kind:      "Pod",
					Namespace: "default",
					Name:      "",
				},
				SafeForAutomation: true,
				Reasoning:         "Temporary failure.",
			},
			wantValid:    false,
			wantDecision: "fallback_miss",
		},

		// 18. Empty target namespace
		{
			name: "empty target namespace",
			proposal: Proposal{
				SubCause:          "transient_failure",
				RecommendedAction: "restart_pod",
				Target: Target{
					Kind:      "Pod",
					Namespace: "",
					Name:      "checkoutservice-abc123",
				},
				SafeForAutomation: true,
				Reasoning:         "Temporary failure.",
			},
			wantValid:    false,
			wantDecision: "fallback_miss",
		},

		// 19. Empty reasoning
		{
			name: "empty reasoning",
			proposal: Proposal{
				SubCause:          "transient_failure",
				RecommendedAction: "restart_pod",
				Target:            correctPod,
				SafeForAutomation: true,
				Reasoning:         "",
			},
			wantValid:    false,
			wantDecision: "fallback_miss",
		},

		// 20. Empty sub-cause
		{
			name: "empty sub-cause",
			proposal: Proposal{
				SubCause:          "",
				RecommendedAction: "restart_pod",
				Target:            correctPod,
				SafeForAutomation: true,
				Reasoning:         "The sub-cause is missing.",
			},
			wantValid:    false,
			wantDecision: "fallback_miss",
		},
	}

	correctDecisions := 0
	falseAccepts := 0
	falseRejects := 0
	expectedAutomations := 0
	expectedNonAutomations := 0

	for _, tc := range cases {
		caseInput := input
		if tc.input != nil {
			caseInput = *tc.input
		}

		result := ValidateProposal(tc.proposal, caseInput)

		passed := result.Valid == tc.wantValid &&
			result.Decision == tc.wantDecision

		if passed {
			correctDecisions++
		}

		expectedAutomation := tc.wantDecision == "automate"
		actualAutomation := result.Decision == "automate"

		if expectedAutomation {
			expectedAutomations++
		} else {
			expectedNonAutomations++
		}

		// Validator allowed automation when it should not have.
		if actualAutomation && !expectedAutomation {
			falseAccepts++
		}

		// Validator blocked automation when it should have allowed it.
		if !actualAutomation && expectedAutomation {
			falseRejects++
		}

		t.Run(tc.name, func(t *testing.T) {
			if result.Valid != tc.wantValid {
				t.Errorf(
					"Valid: got %v, expected %v. Reason: %s",
					result.Valid,
					tc.wantValid,
					result.Reason,
				)
			}

			if result.Decision != tc.wantDecision {
				t.Errorf(
					"Decision: got %q, expected %q. Reason: %s",
					result.Decision,
					tc.wantDecision,
					result.Reason,
				)
			}
		})
	}

	total := len(cases)

	accuracy := float64(correctDecisions) /
		float64(total) * 100

	falseAcceptanceRate := 0.0
	if expectedNonAutomations > 0 {
		falseAcceptanceRate =
			float64(falseAccepts) /
				float64(expectedNonAutomations) * 100
	}

	falseRejectionRate := 0.0
	if expectedAutomations > 0 {
		falseRejectionRate =
			float64(falseRejects) /
				float64(expectedAutomations) * 100
	}

	t.Logf("Total labelled cases: %d", total)
	t.Logf("Correct decisions: %d", correctDecisions)
	t.Logf("Evaluation accuracy: %.2f%%", accuracy)
	t.Logf("False accepts: %d", falseAccepts)
	t.Logf("False acceptance rate: %.2f%%", falseAcceptanceRate)
	t.Logf("False rejects: %d", falseRejects)
	t.Logf("False rejection rate: %.2f%%", falseRejectionRate)
}
