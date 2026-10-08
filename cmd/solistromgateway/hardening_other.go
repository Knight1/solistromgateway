//go:build !linux

package main

// setNotDumpable has no equivalent outside Linux.
//
// macOS and the BSDs have no prctl, and their closest relatives either need
// privileges or do not restrict access to another process's memory in the same
// way. Core dumps are still disabled through the resource limit, which is the
// portable half of the protection.
func setNotDumpable() error { return errNotSupported }
