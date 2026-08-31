package safety

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func TestJSONLAuditWriterAppendsOneValidObjectPerLine(t *testing.T) {
	var output bytes.Buffer
	writer := NewJSONLAuditWriter(&output) // FAKE AUDIT LOGS
	entries := []AuditEntry{
		{
			Timestamp: time.Unix(1, 0).UTC(),
			Pod:       "shop/checkout-1",
			State:     StateDetected,
			Action:    "restart pod",
			Result:    "entered",
		},
		{
			Timestamp: time.Unix(2, 0).UTC(),
			Pod:       "shop/checkout-1",
			State:     StateSnapshotted,
			Action:    "restart pod",
			Result:    "captured",
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
		for _, field := range []string{"timestamp", "pod", "state", "action", "result"} {
			if _, ok := decoded[field]; !ok {
				t.Errorf("line %d is missing field %q", index, field)
			}
		}
		if len(decoded) != 5 {
			t.Errorf("line %d has %d fields, want exactly 5", index, len(decoded))
		}
	}
}
