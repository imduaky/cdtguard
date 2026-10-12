package guard

import (
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/imduaky/cdtguard/internal/state"
)

func ts(y int, m time.Month, d, h int) time.Time { return time.Date(y, m, d, h, 0, 0, 0, beijing) }

var (
	bootThisCycle = ts(2026, 10, 10, 0)
	bootOld       = ts(2026, 8, 1, 0)
)

func TestCycleStart(t *testing.T) {
	cases := []struct{ now, want time.Time }{
		{ts(2026, 10, 12, 0), ts(2026, 10, 1, 12)},
		{ts(2026, 10, 1, 11), ts(2026, 9, 1, 12)},
		{ts(2026, 10, 1, 12), ts(2026, 10, 1, 12)},
		{ts(2026, 1, 1, 0), ts(2025, 12, 1, 12)},
	}
	for _, c := range cases {
		if got := CycleStart(c.now); !got.Equal(c.want) {
			t.Errorf("CycleStart(%v) = %v, want %v", c.now, got, c.want)
		}
	}
}

func TestUsage(t *testing.T) {
	if Usage("max", 5, 9) != 9 || Usage("max", 9, 5) != 9 || Usage("total", 5, 9) != 14 || Usage("tx", 5, 9) != 9 || Usage("rx", 5, 9) != 5 {
		t.Error("usage modes wrong")
	}
}

type env struct {
	g       *Guard
	rx, tx  uint64
	now     time.Time
	execs   []string
	execErr error
}

func newEnv(t *testing.T, boot time.Time) *env {
	e := &env{now: ts(2026, 10, 12, 0)}
	e.g = &Guard{
		StatePath: filepath.Join(t.TempDir(), "s.bin"),
		Limit:     100,
		Mode:      "max",
		Command:   "shutdown now",
		Boot:      boot,
		Counter:   func() (uint64, uint64, error) { return e.rx, e.tx, nil },
		Now:       func() time.Time { return e.now },
		Exec:      func(c string) error { e.execs = append(e.execs, c); return e.execErr },
	}
	return e
}

func TestFreshStartUsesCounters(t *testing.T) {
	e := newEnv(t, bootThisCycle)
	e.rx, e.tx = 30, 40
	if fired, err := e.g.Init(); err != nil || fired {
		t.Fatal(fired, err)
	}
	if e.g.used() != 40 {
		t.Errorf("used = %d", e.g.used())
	}
}

func TestSeedState(t *testing.T) {
	now := ts(2026, 10, 12, 0)
	st := SeedState(now, "max", 30, 140, 60)
	if st.Rx != 60 || st.Tx != 60 || st.LastRx != 30 || st.LastTx != 140 || st.PeriodStart != CycleStart(now).Unix() {
		t.Errorf("st = %+v", st)
	}
	if st := SeedState(now, "total", 1, 2, 61); st.Rx+st.Tx != 61 {
		t.Errorf("total st = %+v", st)
	}
}

func TestSeededStateIsUsed(t *testing.T) {
	e := newEnv(t, bootOld)
	e.rx, e.tx = 30, 140 // counters alone would be over the limit
	if err := state.Save(e.g.StatePath, SeedState(e.now, "max", e.rx, e.tx, 60)); err != nil {
		t.Fatal(err)
	}
	if _, err := e.g.Init(); err != nil {
		t.Fatal(err)
	}
	e.now, e.tx = e.now.Add(time.Minute), 150
	e.g.Step(true)
	if e.g.used() != 70 || len(e.execs) != 0 {
		t.Errorf("used = %d execs=%v", e.g.used(), e.execs)
	}
}

func TestFreshOverLimitRefuses(t *testing.T) {
	for _, boot := range []time.Time{bootOld, bootThisCycle} {
		e := newEnv(t, boot)
		e.rx, e.tx = 10, 150
		if _, err := e.g.Init(); !errors.Is(err, ErrOverLimit) {
			t.Fatalf("err = %v", err)
		}
		if _, err := state.Load(e.g.StatePath); err == nil {
			t.Error("must not write a state file when refusing")
		}
	}
}

func TestMaxAccumulatesRxTxSeparately(t *testing.T) {
	e := newEnv(t, bootThisCycle)
	e.rx, e.tx = 10, 40
	e.g.Init()
	e.now, e.rx, e.tx = e.now.Add(time.Minute), 80, 50 // rx +70, tx +10
	e.g.Step(true)
	if e.g.st.Rx != 80 || e.g.st.Tx != 50 || e.g.used() != 80 {
		t.Fatalf("st = %+v", e.g.st)
	}
}

func TestRebootAndFireOnce(t *testing.T) {
	e := newEnv(t, bootThisCycle)
	e.rx, e.tx = 0, 40
	e.g.Init()

	e.now, e.tx = e.now.Add(time.Minute), 70
	e.g.Step(true)
	if e.g.used() != 70 || len(e.execs) != 0 {
		t.Fatalf("used=%d execs=%v", e.g.used(), e.execs)
	}

	// Reboot: boot is after last save, counters restarted -> added in full.
	e.g.Boot = e.now.Add(time.Minute)
	e.now, e.tx = e.now.Add(2*time.Minute), 5
	e.g.Step(true)
	if e.g.used() != 75 {
		t.Fatalf("after reboot used=%d", e.g.used())
	}

	e.now, e.tx = e.now.Add(time.Minute), 35
	e.g.Step(true)
	if len(e.execs) != 1 || e.execs[0] != "shutdown now" {
		t.Fatalf("execs=%v", e.execs)
	}
	e.tx += 10
	e.g.Step(true)
	if len(e.execs) != 1 {
		t.Fatal("must not fire twice")
	}
}

func TestPowerOnAfterFireDoesNotFireAgain(t *testing.T) {
	e := newEnv(t, bootThisCycle)
	e.g.Init()
	e.tx = 120
	e.g.Step(true)
	if len(e.execs) != 1 {
		t.Fatalf("execs=%v", e.execs)
	}

	// User powers the server back on from the console: new process, new boot.
	e.now = e.now.Add(time.Hour)
	g2 := *e.g
	g2.st = nil
	g2.Boot = e.now
	g2.RestoreCmd = "restore"
	e.tx = 3
	fired, err := g2.Init()
	if err != nil || !fired {
		t.Fatalf("fired=%v err=%v", fired, err)
	}
	if g2.used() != 123 {
		t.Errorf("used=%d", g2.used())
	}
	g2.Step(true)
	if len(e.execs) != 1 {
		t.Fatal("must not fire again this cycle")
	}
}

func TestMonthlyResetRunsResetCmd(t *testing.T) {
	e := newEnv(t, bootThisCycle)
	e.g.ResetCmd = "unthrottle"
	e.g.Init()
	e.tx = 120
	e.g.Step(true)

	e.now, e.tx = ts(2026, 11, 1, 12).Add(time.Minute), 130
	e.g.Step(true)
	if e.g.used() != 0 || e.g.st.FiredAt != 0 {
		t.Fatalf("not reset: %+v", e.g.st)
	}
	if len(e.execs) != 2 || e.execs[1] != "unthrottle" {
		t.Fatalf("execs=%v", e.execs)
	}
	e.tx = 230
	e.g.Step(true)
	if len(e.execs) != 3 || e.execs[2] != "shutdown now" {
		t.Fatalf("should fire again in new cycle, execs=%v", e.execs)
	}
}

func TestFailedCommandAllowsRetry(t *testing.T) {
	e := newEnv(t, bootThisCycle)
	e.execErr = errors.New("boom")
	e.g.Init()
	e.tx = 120
	if err := e.g.Step(true); err == nil {
		t.Fatal("want error")
	}
	st, _ := state.Load(e.g.StatePath)
	if st.FiredAt != 0 {
		t.Error("failed action must not mark as fired")
	}
	e.execErr = nil
	e.g.Step(true)
	if len(e.execs) != 2 {
		t.Errorf("execs=%v", e.execs)
	}
}
