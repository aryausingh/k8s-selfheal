package classifier

import (
	"context"
	"testing"
)

// These cover the crash-after-rollout bad_deploy path added to
// validateSemanticConsistency and MockClassifier.
//
// Why it exists: the original bad_deploy indicators were all image-pull
// wording, and a pod that cannot pull its image sits in ImagePullBackOff —
// never CrashLoopBackOff, the only state the detector watches. The two halves
// of the guard could never both be true, so rollout_undo was unreachable in
// production even though every unit test for it passed.
//
// The safety property being pinned here is that "back-off restarting failed
// container" on its own — true of literally every crash-looping pod — is not
// enough. It only means bad_deploy alongside recent rollout evidence.

// crashLoopAfterRolloutEvents is what the collector actually produces for a
// Deployment whose new revision immediately crash-loops.
func crashLoopAfterRolloutEvents() []string {
	return []string{
		"30s ago: Normal ScalingReplicaSet (Deployment/checkoutservice): Scaled up replica set checkoutservice-abc123 to 1",
		"25s ago: Normal Created (Pod/checkoutservice-abc123): Created container workload",
		"3s ago: Warning BackOff (Pod/checkoutservice-abc123): Back-off restarting failed container workload",
	}
}

func TestBadDeployAcceptsCrashLoopAfterRollout(t *testing.T) {
	input := testIncident()
	input.Logs = ""
	input.Events = crashLoopAfterRolloutEvents()

	proposal := Proposal{
		SubCause:          "bad_deploy",
		RecommendedAction: ActionRolloutUndo,
		Target: Target{
			Kind:      "Deployment",
			Namespace: input.Namespace,
			Name:      input.OwnerDeployment,
		},
		SafeForAutomation: true,
		Reasoning:         "the new revision crash-loops immediately",
	}

	result := ValidateProposal(proposal, input)

	if !result.Valid {
		t.Fatalf("expected a crash loop that began at a rollout to validate as bad_deploy, got: %s", result.Reason)
	}
	if result.Decision != DecisionAutomate {
		t.Fatalf("expected %s, got %s", DecisionAutomate, result.Decision)
	}
}

func TestBadDeployRejectsCrashLoopWithoutRolloutEvidence(t *testing.T) {
	// Every crash-looping pod produces a BackOff event, so without rollout
	// evidence this indicator would hand a rollout_undo to any crash at all.
	input := testIncident()
	input.Logs = ""
	input.Events = []string{
		"3s ago: Warning BackOff (Pod/checkoutservice-abc123): Back-off restarting failed container workload",
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
		Reasoning:         "suspecting the last deploy",
	}

	result := ValidateProposal(proposal, input)

	if result.Valid {
		t.Fatal("a crash loop with no rollout evidence must not validate as bad_deploy")
	}
	if result.ReasonCode != ReasonCodeMissingDeploymentEvidence {
		t.Fatalf("expected %s, got %s", ReasonCodeMissingDeploymentEvidence, result.ReasonCode)
	}
}

func TestMockClassifierProposesRolloutUndoForCrashLoopAfterRollout(t *testing.T) {
	input := testIncident()
	input.Logs = ""
	input.Events = crashLoopAfterRolloutEvents()

	proposal, err := MockClassifier{}.Classify(context.Background(), input)

	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if proposal.SubCause != "bad_deploy" || proposal.RecommendedAction != ActionRolloutUndo {
		t.Fatalf("got %s/%s, want bad_deploy/%s", proposal.SubCause, proposal.RecommendedAction, ActionRolloutUndo)
	}
	if !proposal.SafeForAutomation {
		t.Error("a crash loop that began at a rollout is the canonical automatable rollout_undo case")
	}
	if proposal.Target.Kind != "Deployment" || proposal.Target.Name != input.OwnerDeployment {
		t.Errorf("target = %+v, want the owning Deployment", proposal.Target)
	}
}

func TestMockClassifierPrefersMoreSpecificCausesOverCrashAfterRollout(t *testing.T) {
	// Ordering matters: BackOff plus rollout evidence is present during a
	// transient dependency failure too, and restarting one pod is far cheaper
	// than rolling back a whole Deployment. The narrower cause must win.
	cases := map[string]struct {
		logs        string
		wantCause   string
		wantAction  string
		wantAutomat bool
	}{
		"transient beats crash-after-rollout": {
			logs:        "dial tcp 10.0.0.5:5432: connect: connection refused",
			wantCause:   "transient_failure",
			wantAction:  ActionRestartPod,
			wantAutomat: true,
		},
		"oom beats crash-after-rollout": {
			logs:        "container was OOMKilled",
			wantCause:   "oom_adjacent",
			wantAction:  ActionEscalateToHuman,
			wantAutomat: false,
		},
		"panic beats crash-after-rollout": {
			logs:        "panic: nil map write",
			wantCause:   "application_panic",
			wantAction:  ActionEscalateToHuman,
			wantAutomat: false,
		},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			input := testIncident()
			input.Logs = tc.logs
			input.Events = crashLoopAfterRolloutEvents()

			proposal, err := MockClassifier{}.Classify(context.Background(), input)

			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if proposal.SubCause != tc.wantCause {
				t.Errorf("sub-cause = %s, want %s", proposal.SubCause, tc.wantCause)
			}
			if proposal.RecommendedAction != tc.wantAction {
				t.Errorf("action = %s, want %s", proposal.RecommendedAction, tc.wantAction)
			}
			if proposal.SafeForAutomation != tc.wantAutomat {
				t.Errorf("safe_for_automation = %v, want %v", proposal.SafeForAutomation, tc.wantAutomat)
			}
		})
	}
}

func TestMockClassifierEscalatesCrashLoopWithNoRollout(t *testing.T) {
	input := testIncident()
	input.Logs = ""
	input.Events = []string{
		"3s ago: Warning BackOff (Pod/checkoutservice-abc123): Back-off restarting failed container workload",
	}

	proposal, err := MockClassifier{}.Classify(context.Background(), input)

	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if proposal.SafeForAutomation {
		t.Fatal("a crash loop with no other evidence must escalate, not automate")
	}
	if proposal.SubCause != "unknown" {
		t.Errorf("sub-cause = %s, want unknown", proposal.SubCause)
	}
}

func TestMockClassifierEscalatesCrashAfterRolloutWithNoOwnerDeployment(t *testing.T) {
	input := testIncident()
	input.OwnerDeployment = ""
	input.Logs = ""
	input.Events = crashLoopAfterRolloutEvents()

	proposal, err := MockClassifier{}.Classify(context.Background(), input)

	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if proposal.SafeForAutomation {
		t.Fatal("there is nothing to roll back without an owning Deployment")
	}
	if proposal.RecommendedAction != ActionEscalateToHuman {
		t.Errorf("action = %s, want %s", proposal.RecommendedAction, ActionEscalateToHuman)
	}
}

// TestCrashLoopAfterRolloutSurvivesTheFullPipeline runs the case end to end
// through the same service the controller calls, so a mismatch between the
// mock's proposal and the validator's guard shows up as a fallback_miss here
// rather than as silent escalation in a live cluster.
func TestCrashLoopAfterRolloutSurvivesTheFullPipeline(t *testing.T) {
	input := testIncident()
	input.Logs = ""
	input.Events = crashLoopAfterRolloutEvents()

	outcome := NewClassificationService(MockClassifier{}, 0).ClassifyIncident(context.Background(), input)

	if outcome.FallbackUsed {
		t.Fatalf("the proposal was rejected by validation: %s (%s)", outcome.FallbackReason, outcome.FallbackReasonCode)
	}
	if !outcome.Proposal.SafeForAutomation || outcome.Proposal.RecommendedAction != ActionRolloutUndo {
		t.Fatalf("pipeline produced %+v, want an automatable rollout_undo", outcome.Proposal)
	}
	if outcome.Validation.Decision != DecisionAutomate {
		t.Errorf("decision = %s, want %s", outcome.Validation.Decision, DecisionAutomate)
	}
}
