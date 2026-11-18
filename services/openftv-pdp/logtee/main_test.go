package main

import (
	"bytes"
	"strings"
	"testing"
)

// Without an OTLP endpoint the wrapper only passes the output through,
// line for line and unchanged.
func TestTeePassesOutputThrough(t *testing.T) {
	in := "first\n\n{\"msg\":\"Decision Log\"}\nlast"
	var out bytes.Buffer
	tee(strings.NewReader(in), &out, nil, 0)
	if got, want := out.String(), "first\n\n{\"msg\":\"Decision Log\"}\nlast\n"; got != want {
		t.Fatalf("output = %q, want %q", got, want)
	}
}

// A decision-log line far beyond bufio's default token size still passes.
func TestTeeLongLine(t *testing.T) {
	long := strings.Repeat("x", 1<<20)
	var out bytes.Buffer
	tee(strings.NewReader(long+"\n"), &out, nil, 0)
	if out.Len() != len(long)+1 {
		t.Fatalf("output length = %d, want %d", out.Len(), len(long)+1)
	}
}
