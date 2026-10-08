//go:build linux

package main

import "syscall"

// prSetDumpable is PR_SET_DUMPABLE from linux/prctl.h.
const prSetDumpable = 4

// setNotDumpable asks the kernel to treat this process as not dumpable.
//
// That does two things worth having. It disables core dumps, and it re-owns
// /proc/<pid> to root, so another process running as the same user can no longer
// read this one's memory, open file descriptors, or environment. The environment
// matters because the push URL may have been supplied that way, and nothing in
// Go can scrub it from the block the kernel exposes there.
//
// It needs no privileges, and it is called through the syscall directly to keep
// the program free of dependencies.
func setNotDumpable() error {
	if _, _, errno := syscall.Syscall(syscall.SYS_PRCTL, prSetDumpable, 0, 0); errno != 0 {
		return errno
	}
	return nil
}
