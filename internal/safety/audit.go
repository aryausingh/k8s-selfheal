package safety

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"sync"
	"time"
)

// AuditMetadata is supplied by the experiment harness. Safety copies it onto
// every transition but never infers or fabricates either value.
type AuditMetadata struct {
	Workload string
	ArmLabel string
}

// AuditEntry is the frozen shared Week 3 transition schema. Owner 3 adapts
// these camelCase events into its separate aggregate metrics representation.
type AuditEntry struct {
	IncidentID    string    `json:"incidentID"`
	AttemptNumber int       `json:"attemptNumber"`
	Timestamp     time.Time `json:"timestamp"`
	State         State     `json:"state"`
	Action        string    `json:"action"`
	Result        string    `json:"result"`
	Workload      string    `json:"workload"`
	ArmLabel      string    `json:"armLabel"`
}

// AuditWriter appends one entry for a lifecycle transition.
type AuditWriter interface {
	Append(AuditEntry) error
}

// JSONLAuditWriter serializes each entry as one append-only JSON line.
type JSONLAuditWriter struct {
	mu      sync.Mutex
	encoder *json.Encoder
	sync    func() error
	close   func() error
}

func NewJSONLAuditWriter(writer io.Writer) *JSONLAuditWriter {
	return &JSONLAuditWriter{encoder: json.NewEncoder(writer)}
}

// NewJSONLFileAuditWriter opens an append-only audit file. The parent
// directory must already exist so a missing volume mount fails loudly instead
// of silently creating an ephemeral path in the controller container.
func NewJSONLFileAuditWriter(filePath string) (*JSONLAuditWriter, error) {
	if filePath == "" {
		return nil, fmt.Errorf("open audit file: path is required")
	}
	file, err := os.OpenFile(filePath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return nil, fmt.Errorf("open audit file %q: %w", filePath, err)
	}
	return &JSONLAuditWriter{
		encoder: json.NewEncoder(file),
		sync:    file.Sync,
		close:   file.Close,
	}, nil
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
	if w.sync != nil {
		if err := w.sync(); err != nil {
			return fmt.Errorf("sync audit entry for state %s: %w", entry.State, err)
		}
	}
	return nil
}

// Close releases a file-backed writer. In-memory and stdout-backed writers do
// not own their underlying writer and therefore have nothing to close.
func (w *JSONLAuditWriter) Close() error {
	if w == nil {
		return nil
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.close == nil {
		return nil
	}
	err := w.close()
	w.close = nil
	w.sync = nil
	if err != nil {
		return fmt.Errorf("close audit writer: %w", err)
	}
	return nil
}
