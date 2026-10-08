package main

import "errors"

// errNotSupported means a protection has no equivalent on this platform, which
// is different from it having been attempted and failed.
var errNotSupported = errors.New("not supported on this platform")
