package auth

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
)

// LockFile is the process lock's name inside state_dir.
const LockFile = "LOCK"

// ErrLocked means another process holds the state dir. Two processes on the
// same accounts would kick each other off with LoggedInElsewhere (07 §10.3).
var ErrLocked = errors.New("auth: state dir is locked by another process")

// Lock is an exclusive flock on <state_dir>/LOCK, held for the life of the
// process. The kernel drops it when the process exits, so a crash never
// leaves a stale lock behind.
type Lock struct {
	f *os.File
}

// AcquireLock takes the lock without waiting, creating stateDir (0700) if
// needed. It returns an error wrapping ErrLocked when it is already held.
// The holder's PID is written into the file to help an operator.
func AcquireLock(stateDir string) (*Lock, error) {
	if err := os.MkdirAll(stateDir, 0o700); err != nil {
		return nil, fmt.Errorf("auth: create state dir: %w", err)
	}
	path := filepath.Join(stateDir, LockFile)
	f, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE, 0o600)
	if err != nil {
		return nil, fmt.Errorf("auth: open lock: %w", err)
	}
	if err := flock(f); err != nil {
		_ = f.Close()
		return nil, fmt.Errorf("auth: lock %s: %w", path, err)
	}
	// The PID is advisory; failing to write it does not matter.
	if err := f.Truncate(0); err == nil {
		_, _ = f.WriteAt([]byte(strconv.Itoa(os.Getpid())+"\n"), 0)
	}
	return &Lock{f: f}, nil
}

// Release drops the lock. It is safe to call more than once.
func (l *Lock) Release() error {
	if l == nil || l.f == nil {
		return nil
	}
	err := funlock(l.f)
	if cerr := l.f.Close(); err == nil {
		err = cerr
	}
	l.f = nil
	if err != nil {
		return fmt.Errorf("auth: release lock: %w", err)
	}
	return nil
}
