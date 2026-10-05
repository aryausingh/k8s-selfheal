package safety

import (
	"encoding/json"
	"fmt"
	"io"
	"sync"
	"time"
)

// AuditEntry is one line of the shared audit log.
//
// The first five fields are the Week 2 contract and are unchanged. The three
// below them are additive and omitempty, so every line Service.Remediate
// already writes serialises exactly as before — they exist because the metrics
// adapter cannot group transitions without them
// (docs/measurement-definitions.md §6).
//
// Owner 2: Service.Remediate should populate IncidentID and AttemptNumber from
// the DetectionEvent on every entry it records. Owner 1 populates them on the
// CLOSED line only (internal/controller/incident.go).
type AuditEntry struct {
	Timestamp time.Time `json:"timestamp"`
	Pod       string    `json:"pod"`
	State     State     `json:"state"`
	Action    string    `json:"action"`
	Result    string    `json:"result"`

	IncidentID    string `json:"incidentID,omitempty"`
	AttemptNumber int    `json:"attemptNumber,omitempty"`

	// ClassifierMillis is the classifier call duration. One value per
	// incident rather than per attempt: evidence is frozen at detection, so
	// every attempt classifies the same input and the durations are
	// equivalent. Carried on the CLOSED line.
	ClassifierMillis int64 `json:"classifierMillis,omitempty"`
}

// AuditWriter appends one entry for a lifecycle transition.
type AuditWriter interface {
	Append(AuditEntry) error
}

// JSONLAuditWriter serializes each entry as one append-only JSON line.
type JSONLAuditWriter struct {
	mu      sync.Mutex
	encoder *json.Encoder
}

func NewJSONLAuditWriter(writer io.Writer) *JSONLAuditWriter {
	return &JSONLAuditWriter{encoder: json.NewEncoder(writer)}
}

func (w *JSONLAuditWriter) Append(entry AuditEntry) error {
	if w == nil || w.encoder == nil {
		return fmt.Errorf("append audit entry: writer is required")
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	if err := w.encoder.Encode(entry); err != nil {
		return fmt.Errorf("append audit entry for state %s: %w", entry.State, err)
	}
	return nil
}
