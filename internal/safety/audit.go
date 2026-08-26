package safety

import (
	"encoding/json"
	"fmt"
	"io"
	"sync"
	"time"
)

// AuditEntry contains exactly the five fields retained for Week 2 integration.
type AuditEntry struct {
	Timestamp time.Time `json:"timestamp"`
	Pod       string    `json:"pod"`
	State     State     `json:"state"`
	Action    string    `json:"action"`
	Result    string    `json:"result"`
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
