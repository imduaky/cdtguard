package state

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestRoundTrip(t *testing.T) {
	p := filepath.Join(t.TempDir(), "sub", "s.bin")
	want := &State{PeriodStart: 1, SavedAt: 2, Rx: 3, Tx: 4, LastRx: 5, LastTx: 6, FiredAt: 7}
	if err := Save(p, want); err != nil {
		t.Fatal(err)
	}
	got, err := Load(p)
	if err != nil || *got != *want {
		t.Fatalf("got %+v, %v", got, err)
	}
	if fi, _ := os.Stat(p); fi.Size() != size {
		t.Errorf("size = %d", fi.Size())
	}
}

func TestMissingAndCorrupt(t *testing.T) {
	p := filepath.Join(t.TempDir(), "s.bin")
	if _, err := Load(p); !os.IsNotExist(err) {
		t.Errorf("want not-exist, got %v", err)
	}
	Save(p, &State{Rx: 9})
	b, _ := os.ReadFile(p)
	b[20] ^= 0xff
	os.WriteFile(p, b, 0o644)
	if _, err := Load(p); !errors.Is(err, ErrCorrupt) {
		t.Errorf("want corrupt, got %v", err)
	}
}
