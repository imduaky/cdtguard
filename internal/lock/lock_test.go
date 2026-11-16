package lock

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestExclusiveLockPreventsSecondOwner(t *testing.T) {
	state := filepath.Join(t.TempDir(), "state.bin")
	first, err := Acquire(state, false)
	if err != nil {
		t.Fatal(err)
	}
	defer first.Release()
	if _, err := Acquire(state, false); err == nil {
		t.Fatal("second exclusive lock succeeded")
	} else {
		var busy *BusyError
		if !errors.As(err, &busy) {
			t.Fatalf("error = %v, want BusyError", err)
		}
		if busy.OwnerPID <= 0 {
			t.Fatalf("owner pid = %d", busy.OwnerPID)
		}
	}
	if shared, err := Acquire(state, true); err == nil {
		shared.Release()
		t.Fatal("shared lock succeeded while exclusive lock was held")
	}
	if got := OwnerPID(state); got <= 0 {
		t.Fatalf("owner pid from file = %d", got)
	}
	if err := first.Release(); err != nil {
		t.Fatal(err)
	}
	second, err := Acquire(state, false)
	if err != nil {
		t.Fatal(err)
	}
	second.Release()
}

func TestSharedLockClearsStaleOwner(t *testing.T) {
	state := filepath.Join(t.TempDir(), "state.bin")
	f, err := os.OpenFile(Path(state), os.O_RDWR|os.O_CREATE, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	if err := writePID(f, 999999); err != nil {
		f.Close()
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
	shared, err := Acquire(state, true)
	if err != nil {
		t.Fatal(err)
	}
	defer shared.Release()
	if got := OwnerPID(state); got != 0 {
		t.Fatalf("owner pid after shared acquire = %d, want 0", got)
	}
}
