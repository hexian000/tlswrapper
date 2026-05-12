// tlswrapper (c) 2021-2026 He Xian <hexian000@outlook.com>
// This code is licensed under MIT license (see LICENSE for details)

package eventlog

import (
	"bytes"
	"errors"
	"strings"
	"testing"
	"time"
)

type errWriter struct{}

func (errWriter) Write([]byte) (int, error) {
	return 0, errors.New("write failed")
}

func TestRecentFormatAndRingBehavior(t *testing.T) {
	r := NewRecent(2)
	base := time.Unix(1_700_000_000, 0)

	r.Add(base, "alpha")
	r.Add(base.Add(time.Second), "alpha")
	r.Add(base.Add(2*time.Second), "beta")
	r.Add(base.Add(3*time.Second), "beta")

	var buf bytes.Buffer
	if err := r.Format(&buf, 10); err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSpace(buf.String()), "\n")
	if len(lines) != 2 {
		t.Fatalf("line count = %d, want 2", len(lines))
	}
	if !strings.Contains(lines[0], "beta (x2)") {
		t.Fatalf("line[0] = %q, want beta (x2)", lines[0])
	}
	if !strings.Contains(lines[1], "alpha (x2)") {
		t.Fatalf("line[1] = %q, want alpha (x2)", lines[1])
	}

	r.Add(base.Add(4*time.Second), "gamma")
	buf.Reset()
	if err := r.Format(&buf, 2); err != nil {
		t.Fatal(err)
	}
	lines = strings.Split(strings.TrimSpace(buf.String()), "\n")
	if len(lines) != 2 {
		t.Fatalf("line count after ring overwrite = %d, want 2", len(lines))
	}
	if !strings.Contains(lines[0], "gamma") {
		t.Fatalf("line[0] = %q, want gamma", lines[0])
	}
	if !strings.Contains(lines[1], "beta (x2)") {
		t.Fatalf("line[1] = %q, want beta (x2)", lines[1])
	}
}

func TestRecentFormatWriterError(t *testing.T) {
	r := NewRecent(1)
	r.Add(time.Unix(1_700_000_000, 0), "entry")
	err := r.Format(errWriter{}, 1)
	if err == nil {
		t.Fatal("expected writer error, got nil")
	}
}
