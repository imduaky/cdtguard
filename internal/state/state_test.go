package state

import (
	"encoding/binary"
	"errors"
	"hash/crc32"
	"os"
	"path/filepath"
	"testing"
)

func TestRoundTrip(t *testing.T) {
	p := filepath.Join(t.TempDir(), "sub", "s.bin")
	want := &State{PeriodStart: 1, SavedAt: 2, Rx: 3, Tx: 4, LastRx: 5, LastTx: 6, FiredAt: 7, LastHour: 8}
	want.Daily[31] = Traffic{9, 10}
	want.Hourly[47] = Traffic{11, 12}
	want.History[11] = Cycle{Start: 13, Traffic: Traffic{14, 15}, FiredAt: 16}
	want.SetConfig(195<<30, "max", "ens2")
	if err := Save(p, want); err != nil {
		t.Fatal(err)
	}
	got, err := Load(p)
	if err != nil || *got != *want {
		t.Fatalf("got %+v, %v", got, err)
	}
	fi, _ := os.Stat(p)
	if n := fileSize(filepath.Dir(p)); fi.Size() != int64(n) || n < minSize {
		t.Errorf("size = %d, want %d (>= %d)", fi.Size(), n, minSize)
	}
}

func TestConfig(t *testing.T) {
	var s State
	s.SetConfig(195<<30, "total", "enp1s0")
	limit, mode, iface := s.Config()
	if limit != 195<<30 || mode != "total" || iface != "enp1s0" {
		t.Fatalf("config = %d %q %q", limit, mode, iface)
	}
}

func TestMissingAndCorrupt(t *testing.T) {
	p := filepath.Join(t.TempDir(), "s.bin")
	if _, err := Load(p); !os.IsNotExist(err) {
		t.Errorf("want not-exist, got %v", err)
	}
	Save(p, &State{Rx: 9})
	b, _ := os.ReadFile(p)
	b[2] = 2 // unsupported state format version
	binary.BigEndian.PutUint32(b[len(b)-4:], crc32.ChecksumIEEE(b[:len(b)-4]))
	if err := os.WriteFile(p, b, 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(p); !errors.Is(err, ErrCorrupt) {
		t.Errorf("unsupported version: want corrupt, got %v", err)
	}
	b, _ = os.ReadFile(p)
	for _, i := range []int{20, len(b) - 10} { // a field, then the padding
		c := append([]byte(nil), b...)
		c[i] ^= 0xff
		os.WriteFile(p, c, 0o644)
		if _, err := Load(p); !errors.Is(err, ErrCorrupt) {
			t.Errorf("flip byte %d: want corrupt, got %v", i, err)
		}
	}
}

func TestEndCycle(t *testing.T) {
	s := &State{PeriodStart: 100, Rx: 1, Tx: 2, FiredAt: 150}
	s.Daily[3] = Traffic{1, 2}
	s.EndCycle(200)
	s.Rx = 5
	s.EndCycle(300)
	if s.PeriodStart != 300 || s.Rx != 0 || s.FiredAt != 0 || s.Daily != [Days]Traffic{} {
		t.Errorf("not cleared: %+v", s)
	}
	if s.History[0] != (Cycle{Start: 200, Traffic: Traffic{5, 0}}) || s.History[1] != (Cycle{Start: 100, Traffic: Traffic{1, 2}, FiredAt: 150}) {
		t.Errorf("history = %+v", s.History[:2])
	}
	for i := range Cycles + 5 {
		s.EndCycle(int64(400 + i)) // archives 300, 400, ..., 415
	}
	if s.History[0].Start != 415 || s.History[Cycles-1].Start != 404 {
		t.Errorf("history = %+v", s.History)
	}
}

func TestAddHourly(t *testing.T) {
	s := &State{}
	s.AddHourly(1000, 1, 1)
	s.AddHourly(1000, 1, 0)
	s.AddHourly(1002, 5, 5) // 1001 had no traffic
	s.AddHourly(999, 1, 0)  // clock stepped back: counts in the newest hour
	if s.LastHour != 1002 || s.Hourly[1000%Hours] != (Traffic{2, 1}) || s.Hourly[1001%Hours] != (Traffic{}) || s.Hourly[1002%Hours] != (Traffic{6, 5}) {
		t.Fatalf("hourly = %+v last=%d", s.Hourly, s.LastHour)
	}
	s.AddHourly(1000+Hours, 7, 7) // overwrites hour 1000's slot
	if s.Hourly[1000%Hours] != (Traffic{7, 7}) || s.Hourly[1002%Hours] != (Traffic{6, 5}) {
		t.Fatalf("hourly = %+v", s.Hourly)
	}
	s.AddHourly(5000, 1, 1) // long gap clears everything
	if s.Hourly[1002%Hours] != (Traffic{}) || s.Hourly[5000%Hours] != (Traffic{1, 1}) {
		t.Fatalf("hourly = %+v", s.Hourly)
	}
}
