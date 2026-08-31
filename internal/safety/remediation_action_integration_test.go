package safety_test

import (
	"github.com/aryausingh/k8s-selfheal/internal/actions"
	"github.com/aryausingh/k8s-selfheal/internal/safety"
)

var (
	_ safety.RemediationAction = (*actions.RestartPodAction)(nil)
	_ safety.RemediationAction = (*actions.RolloutUndoAction)(nil)
)
