package safety

import (
	"time"

	"github.com/aryausingh/k8s-selfheal/internal/contracts"
)

// DetectionEvent is the frozen Week 2 hand-off contract from Owner 1.
// The alias keeps Arya's action implementations structurally compatible with
// RemediationAction without duplicating the shared contract.
type DetectionEvent = contracts.DetectionEvent

// OutcomeResult is the terminal result exposed to Owner 3.
type OutcomeResult string

const (
	OutcomeRecovered  OutcomeResult = "recovered"
	OutcomeRolledBack OutcomeResult = "rolled_back"
)

// Outcome is the frozen Week 2 hand-off contract consumed by Owner 3.
// MTTR is event timestamp to RECOVERED or ROLLED_BACK. A JSON/report boundary
// must serialize MTTR.Seconds() explicitly rather than encoding time.Duration.
type Outcome struct {
	Result       OutcomeResult
	MTTR         time.Duration
	AuditEntries []AuditEntry
}
