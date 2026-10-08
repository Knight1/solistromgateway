package main

import (
	"bytes"
	"log/slog"
	"strings"
	"syscall"
	"testing"
)

func TestDisableCoreDumpsSetsTheLimitToZero(t *testing.T) {
	// A core dump of this process would contain the push URL in plain text.
	if err := disableCoreDumps(); err != nil {
		t.Fatalf("disableCoreDumps: %v", err)
	}

	var lim syscall.Rlimit
	if err := syscall.Getrlimit(syscall.RLIMIT_CORE, &lim); err != nil {
		t.Fatalf("Getrlimit: %v", err)
	}
	if lim.Cur != 0 {
		t.Errorf("soft core limit = %d, want 0", lim.Cur)
	}
	if lim.Max != 0 {
		t.Errorf("hard core limit = %d, want 0 so it cannot be raised again", lim.Max)
	}
}

func TestHardenProcessReportsWhatItApplied(t *testing.T) {
	h := hardenProcess()

	if len(h.Applied) == 0 {
		t.Error("hardenProcess reported nothing applied; at least core dumps should be off")
	}
	joined := strings.Join(h.Applied, " ")
	if !strings.Contains(strings.ToLower(joined), "core") {
		t.Errorf("Applied = %v, want core dumps mentioned", h.Applied)
	}
	for _, f := range h.Failed {
		t.Errorf("hardening step failed on this platform: %s", f)
	}
}

func TestLogHardeningNamesEachMeasure(t *testing.T) {
	var buf bytes.Buffer
	log := slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug}))

	logHardening(log, hardeningReport{
		Applied: []string{"core dumps disabled", "process marked not dumpable"},
	})

	out := buf.String()
	for _, want := range []string{"core dumps disabled", "process marked not dumpable"} {
		if !strings.Contains(out, want) {
			t.Errorf("log is missing %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, "level=WARN") {
		t.Errorf("nothing failed, so nothing should be raised:\n%s", out)
	}
}

func TestLogHardeningWarnsAboutFailures(t *testing.T) {
	var buf bytes.Buffer
	log := slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug}))

	logHardening(log, hardeningReport{
		Applied: []string{"core dumps disabled"},
		Failed:  []string{"could not mark the process not dumpable: operation not permitted"},
	})

	out := buf.String()
	if !strings.Contains(out, "level=WARN") {
		t.Errorf("a failed hardening step should be raised:\n%s", out)
	}
	if !strings.Contains(out, "not permitted") {
		t.Errorf("the reason should survive into the log:\n%s", out)
	}
}

func TestLogHardeningSaysSoWhenSkipped(t *testing.T) {
	// Running with debugging allowed is a deliberate weakening and has to be
	// visible in the log, not silent.
	var buf bytes.Buffer
	log := slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug}))

	logHardening(log, hardeningReport{Skipped: true})

	out := buf.String()
	if !strings.Contains(out, "level=WARN") {
		t.Errorf("skipping hardening should be raised as a warning:\n%s", out)
	}
	if !strings.Contains(strings.ToLower(out), "debug") {
		t.Errorf("the log should say why it was skipped:\n%s", out)
	}
}
