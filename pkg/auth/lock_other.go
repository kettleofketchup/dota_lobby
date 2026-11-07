//go:build !(linux || darwin || freebsd || netbsd || openbsd || dragonfly)

package auth

import (
	"errors"
	"os"
)

// The service ships as a Linux container; other platforms get a clear error
// rather than running unlocked.
var errLockUnsupported = errors.New("flock is not supported on this platform")

func flock(*os.File) error   { return errLockUnsupported }
func funlock(*os.File) error { return nil }
