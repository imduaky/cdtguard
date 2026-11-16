// Package lock serializes access to a cdtguard state file.
package lock

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
)

// BusyError reports that another process owns a state lock.
type BusyError struct {
	Path     string
	OwnerPID int
}

func (e *BusyError) Error() string {
	if e.OwnerPID > 0 {
		return fmt.Sprintf("state is locked by pid %d (%s)", e.OwnerPID, e.Path)
	}
	return fmt.Sprintf("state is locked (%s)", e.Path)
}

// Handle is a held state lock. The lock is released when Release is called or
// when the process exits and the file descriptor is closed by the kernel.
type Handle struct {
	file   *os.File
	shared bool
}

// Path returns the lock file path.
func Path(statePath string) string { return statePath + ".lock" }

// Acquire takes an exclusive or shared non-blocking lock for statePath.
func Acquire(statePath string, shared bool) (*Handle, error) {
	lockPath := Path(statePath)
	if err := os.MkdirAll(filepath.Dir(lockPath), 0o755); err != nil {
		return nil, err
	}
	f, err := os.OpenFile(lockPath, os.O_RDWR|os.O_CREATE, 0o600)
	if err != nil {
		return nil, err
	}
	operation := syscall.LOCK_EX | syscall.LOCK_NB
	if shared {
		operation = syscall.LOCK_SH | syscall.LOCK_NB
	}
	if err := syscall.Flock(int(f.Fd()), operation); err != nil {
		pid := readPID(f)
		_ = f.Close()
		if errors.Is(err, syscall.EWOULDBLOCK) || errors.Is(err, syscall.EAGAIN) {
			return nil, &BusyError{Path: lockPath, OwnerPID: pid}
		}
		return nil, err
	}
	if shared {
		// A previous exclusive owner may have died without clearing its PID.
		// Shared readers do not have a single owner, so clear that stale value.
		if err := writePID(f, 0); err != nil {
			_ = syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
			_ = f.Close()
			return nil, err
		}
	} else {
		if err := writePID(f, os.Getpid()); err != nil {
			_ = syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
			_ = f.Close()
			return nil, err
		}
	}
	return &Handle{file: f, shared: shared}, nil
}

// Release unlocks and closes the lock file.
func (h *Handle) Release() error {
	if h == nil || h.file == nil {
		return nil
	}
	if !h.shared {
		_ = writePID(h.file, 0)
	}
	err := syscall.Flock(int(h.file.Fd()), syscall.LOCK_UN)
	if closeErr := h.file.Close(); err == nil {
		err = closeErr
	}
	h.file = nil
	return err
}

// OwnerPID returns the PID recorded in a state lock, or zero if unavailable.
func OwnerPID(statePath string) int {
	f, err := os.Open(Path(statePath))
	if err != nil {
		return 0
	}
	defer f.Close()
	return readPID(f)
}

// OwnerHolds reports whether pid currently has the state lock file open. The
// PID text in a lock file can outlive a crashed process, so callers that may
// signal a process should verify the open file descriptor first.
func OwnerHolds(statePath string, pid int) bool {
	if pid <= 1 {
		return false
	}
	lockInfo, err := os.Stat(Path(statePath))
	if err != nil {
		return false
	}
	entries, err := os.ReadDir(filepath.Join("/proc", strconv.Itoa(pid), "fd"))
	if err != nil {
		return false
	}
	for _, entry := range entries {
		fdInfo, err := os.Stat(filepath.Join("/proc", strconv.Itoa(pid), "fd", entry.Name()))
		if err == nil && os.SameFile(lockInfo, fdInfo) {
			return true
		}
	}
	return false
}

func readPID(f *os.File) int {
	if _, err := f.Seek(0, 0); err != nil {
		return 0
	}
	b := make([]byte, 128)
	n, _ := f.Read(b)
	b = b[:n]
	for _, line := range strings.Split(string(b), "\n") {
		if v, ok := strings.CutPrefix(line, "pid="); ok {
			pid, _ := strconv.Atoi(strings.TrimSpace(v))
			return pid
		}
	}
	return 0
}

func writePID(f *os.File, pid int) error {
	if err := f.Truncate(0); err != nil {
		return err
	}
	if _, err := f.Seek(0, 0); err != nil {
		return err
	}
	_, err := fmt.Fprintf(f, "pid=%d\n", pid)
	return err
}
