package safety

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestJSONLAuditWriterAppendsOneValidObjectPerLine(t *testing.T) {
	var output bytes.Buffer
	writer := NewJSONLAuditWriter(&output) // FAKE AUDIT LOGS
	entries := []AuditEntry{
		{
			IncidentID:    "incident-1",
			AttemptNumber: 1,
			Timestamp:     time.Unix(1, 0).UTC(),
			State:         StateDetected,
			Action:        "restart_pod",
			Result:        "entered",
			Workload:      "W1",
			ArmLabel:      "enabled",
		},
		{
			IncidentID:    "incident-1",
			AttemptNumber: 1,
			Timestamp:     time.Unix(2, 0).UTC(),
			State:         StateSnapshotted,
			Action:        "restart_pod",
			Result:        "captured",
			Workload:      "W1",
			ArmLabel:      "enabled",
		},
	}

	for _, entry := range entries {
		if err := writer.Append(entry); err != nil {
			t.Fatalf("Append() error = %v", err)
		}
	}

	lines := strings.Split(strings.TrimSpace(output.String()), "\n")
	if len(lines) != len(entries) {
		t.Fatalf("line count = %d, want %d", len(lines), len(entries))
	}
	for index, line := range lines {
		var decoded map[string]json.RawMessage
		if err := json.Unmarshal([]byte(line), &decoded); err != nil {
			t.Fatalf("line %d is invalid JSON: %v", index, err)
		}
		for _, field := range []string{
			"incidentID",
			"attemptNumber",
			"timestamp",
			"state",
			"action",
			"result",
			"workload",
			"armLabel",
		} {
			if _, ok := decoded[field]; !ok {
				t.Errorf("line %d is missing field %q", index, field)
			}
		}
		if len(decoded) != 8 {
			t.Errorf("line %d has %d fields, want exactly 8", index, len(decoded))
		}
	}
}

func TestJSONLFileAuditWriterPersistsConcurrentAppendsAcrossReopen(t *testing.T) {
	filePath := filepath.Join(t.TempDir(), "audit.jsonl")
	writer, err := NewJSONLFileAuditWriter(filePath)
	if err != nil {
		t.Fatalf("NewJSONLFileAuditWriter() error = %v", err)
	}

	const entryCount = 24
	var group sync.WaitGroup
	for index := range entryCount {
		group.Go(func() {
			if appendErr := writer.Append(AuditEntry{
				IncidentID:    "incident-concurrent",
				AttemptNumber: 1,
				Timestamp:     time.Unix(int64(index+1), 0).UTC(),
				State:         StateDetected,
				Action:        "restart_pod",
				Result:        fmt.Sprintf("entry-%d", index),
				Workload:      "W1",
				ArmLabel:      "enabled",
			}); appendErr != nil {
				t.Errorf("Append() error = %v", appendErr)
			}
		})
	}
	group.Wait()
	if err := writer.Close(); err != nil {
		t.Fatalf("Close() error = %v", err)
	}

	reopened, err := NewJSONLFileAuditWriter(filePath)
	if err != nil {
		t.Fatalf("reopen audit writer: %v", err)
	}
	if err := reopened.Append(AuditEntry{
		IncidentID:    "incident-concurrent",
		AttemptNumber: 1,
		Timestamp:     time.Unix(100, 0).UTC(),
		State:         StateLogged,
		Action:        "restart_pod",
		Result:        "reopened",
		Workload:      "W1",
		ArmLabel:      "enabled",
	}); err != nil {
		t.Fatalf("Append() after reopen error = %v", err)
	}
	if err := reopened.Close(); err != nil {
		t.Fatalf("Close() after reopen error = %v", err)
	}

	contents, err := os.ReadFile(filePath)
	if err != nil {
		t.Fatalf("ReadFile() error = %v", err)
	}
	lines := strings.Split(strings.TrimSpace(string(contents)), "\n")
	if len(lines) != entryCount+1 {
		t.Fatalf("line count = %d, want %d", len(lines), entryCount+1)
	}
	for index, line := range lines {
		var entry AuditEntry
		if err := json.Unmarshal([]byte(line), &entry); err != nil {
			t.Fatalf("line %d is invalid JSON: %v", index, err)
		}
	}
}
