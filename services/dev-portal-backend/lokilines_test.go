package main

import (
	"encoding/json"
	"strings"
	"testing"
)

// splice reproduces promtail's Docker target: the line cut into 16 KiB
// chunks, each continuation prefixed with its timestamp.
func splice(line string, stamps ...string) string {
	var b strings.Builder
	for i := 0; len(line) > dockerLogChunk; i++ {
		b.WriteString(line[:dockerLogChunk])
		b.WriteString(stamps[i%len(stamps)])
		line = line[dockerLogChunk:]
	}
	b.WriteString(line)
	return b.String()
}

func TestRepairDockerSplitsRejoinsALongDecisionLine(t *testing.T) {
	steps := make([]map[string]string, 0, 600)
	for range 600 {
		steps = append(steps, map[string]string{"code": "CONSENT_WITHDRAWN", "expected": "consent.withdrawn == false", "status": "fail"})
	}
	raw, _ := json.Marshal(map[string]any{"msg": "Decision Log", "result": map[string]any{"steps": steps}})
	original := string(raw)
	if len(original) < 2*dockerLogChunk {
		t.Fatalf("fixture too short to cross two chunk boundaries: %d bytes", len(original))
	}

	// Timestamps vary in length: RFC 3339 drops trailing zeros of the fraction.
	spliced := splice(original, "2026-09-15T09:28:19.017072333Z ", "2026-09-15T09:28:19.0171Z ")
	if spliced == original {
		t.Fatal("fixture did not splice anything")
	}

	if got := repairDockerSplits(spliced); got != original {
		t.Fatalf("repaired line differs from the original (len %d, want %d)", len(got), len(original))
	}
}

func TestRepairDockerSplitsLeavesShortAndCleanLinesAlone(t *testing.T) {
	short := `{"msg":"Decision Log","time":"2026-09-15T09:28:19Z"}`
	if got := repairDockerSplits(short); got != short {
		t.Errorf("short line changed: %q", got)
	}
	// A long line that Docker did not split must survive intact, even with a
	// timestamp in its content that is not at a chunk boundary.
	clean := `{"a":"` + strings.Repeat("x", 3*dockerLogChunk) + `"}`
	if got := repairDockerSplits(clean); got != clean {
		t.Error("unsplit long line changed")
	}
}
