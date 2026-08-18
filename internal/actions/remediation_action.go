package actions

import (
	"context"

	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/aryausingh/k8s-selfheal/internal/contracts"
)

// RestartPodAction and RolloutUndoAction adapt RestartPod/RolloutUndo to
// Ananya's safety.RemediationAction interface:
//
//	type RemediationAction interface {
//	    Name() string
//	    Execute(context.Context, DetectionEvent) error
//	}
//
// Go satisfies interfaces structurally, so these don't need to import
// internal/safety (not in this repo yet) to implement it — once her
// `DetectionEvent` is `type DetectionEvent = contracts.DetectionEvent`, both
// types below already satisfy her interface as written.
//
// Not build-verified against her actual interface, since it isn't
// importable here yet — confirm on her side once internal/safety lands.
type RestartPodAction struct {
	Client client.Client
}

func (a *RestartPodAction) Name() string { return "restart_pod" }

func (a *RestartPodAction) Execute(ctx context.Context, event contracts.DetectionEvent) error {
	return RestartPod(ctx, a.Client, event)
}

type RolloutUndoAction struct {
	Client client.Client
}

func (a *RolloutUndoAction) Name() string { return "rollout_undo" }

func (a *RolloutUndoAction) Execute(ctx context.Context, event contracts.DetectionEvent) error {
	return RolloutUndo(ctx, a.Client, event)
}
