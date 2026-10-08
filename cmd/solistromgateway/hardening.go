package main

import (
	"fmt"
	"log/slog"
	"syscall"
)

// hardeningReport is what the process managed to do to protect its own memory.
type hardeningReport struct {
	Applied []string
	Failed  []string
	Skipped bool
}

// hardenProcess limits what another process on this machine can learn about
// this one.
//
// The configuration holds the push URL, and that URL is a credential: anyone
// with it can write readings into the owner's energy account. Device passwords
// live alongside it. Both end up in this process's memory for as long as it
// runs, so the two places they could escape to are a core dump and another
// process reading /proc.
//
// Neither step needs any privilege, so both work inside a container running with
// every capability dropped.
func hardenProcess() hardeningReport {
	var h hardeningReport

	if err := disableCoreDumps(); err != nil {
		h.Failed = append(h.Failed, fmt.Sprintf("could not disable core dumps: %v", err))
	} else {
		h.Applied = append(h.Applied, "core dumps disabled")
	}

	switch err := setNotDumpable(); {
	case err == errNotSupported:
		// Not a failure: there is simply no equivalent on this platform.
	case err != nil:
		h.Failed = append(h.Failed, fmt.Sprintf("could not mark the process not dumpable: %v", err))
	default:
		h.Applied = append(h.Applied, "process marked not dumpable, so another process with the same user cannot read its memory or environment")
	}

	return h
}

// disableCoreDumps stops the kernel writing this process's memory to a file if
// it crashes, since that file would contain the push URL in plain text.
//
// The hard limit goes to zero as well, so nothing can raise it again.
func disableCoreDumps() error {
	return syscall.Setrlimit(syscall.RLIMIT_CORE, &syscall.Rlimit{Cur: 0, Max: 0})
}

// logHardening reports what was done, and makes a deliberate weakening visible.
func logHardening(log *slog.Logger, h hardeningReport) {
	if h.Skipped {
		log.Warn("memory protection skipped because debugging was allowed; another process running as the same user can read this process's memory, including the push URL")
		return
	}

	for _, applied := range h.Applied {
		log.Debug("memory protection applied", "measure", applied)
	}
	for _, failed := range h.Failed {
		log.Warn("could not apply a memory protection", "detail", failed)
	}
	if len(h.Failed) == 0 {
		log.Info("memory protection in place", "measures", len(h.Applied))
	}
}
