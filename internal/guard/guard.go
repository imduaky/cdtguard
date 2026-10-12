// Package guard accumulates monthly traffic and runs a command once the limit is hit.
package guard

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"time"

	"github.com/imduaky/cdtguard/internal/size"
	"github.com/imduaky/cdtguard/internal/state"
)

// Aliyun resets the CDT quota at 12:00 on the 1st, Beijing time (UTC+8, no DST).
var beijing = time.FixedZone("CST", 8*3600)

// ErrOverLimit means a fresh start would begin at or over the limit; the user must
// supply the real usage with `cdtguard init`.
var ErrOverLimit = errors.New("starting usage is at or over the limit")

// CycleStart returns the most recent monthly reset moment at or before now.
func CycleStart(now time.Time) time.Time {
	n := now.In(beijing)
	t := time.Date(n.Year(), n.Month(), 1, 12, 0, 0, 0, beijing)
	if n.Before(t) {
		t = time.Date(n.Year(), n.Month()-1, 1, 12, 0, 0, 0, beijing)
	}
	return t
}

// Usage turns accumulated rx/tx into the number billed. mode is max (Aliyun CDT),
// total, tx or rx.
func Usage(mode string, rx, tx uint64) uint64 {
	switch mode {
	case "tx":
		return tx
	case "rx":
		return rx
	case "total":
		return rx + tx
	}
	return max(rx, tx)
}

// Guard holds configuration and the in-memory state. Injectable fields keep it testable.
type Guard struct {
	StatePath  string
	Limit      uint64
	Mode       string // see Usage
	Command    string // run when the limit is reached
	ResetCmd   string // run when a new cycle begins after the action had fired
	RestoreCmd string // run at startup when the action already fired this cycle
	Interval   time.Duration
	Boot       time.Time
	Counter    func() (rx, tx uint64, err error) // cumulative NIC bytes since boot
	Now        func() time.Time                  // defaults to time.Now
	Exec       func(cmd string) error            // defaults to sh -c
	Logf       func(format string, a ...any)     // defaults to a no-op

	st           *state.State
	pendingReset bool
}

func (g *Guard) now() time.Time {
	if g.Now != nil {
		return g.Now()
	}
	return time.Now()
}

func (g *Guard) logf(format string, a ...any) {
	if g.Logf != nil {
		g.Logf(format, a...)
	}
}

func (g *Guard) used() uint64 { return Usage(g.Mode, g.st.Rx, g.st.Tx) }

// Init loads (or creates) the state and folds in the current counters.
// It reports whether the action already ran in the current cycle.
func (g *Guard) Init() (alreadyFired bool, err error) {
	rx, tx, err := g.Counter()
	if err != nil {
		return false, err
	}
	now := g.now()
	st, err := state.Load(g.StatePath)
	switch {
	case err == nil:
		g.st = st
		// A reboot can only happen between runs, so only check for one here.
		g.advance(now, rx, tx, st.SavedAt < g.Boot.Unix())
		g.logf("loaded state: used %s in cycle since %s", size.Format(g.used()), time.Unix(g.st.PeriodStart, 0).In(beijing).Format("2006-01-02 15:04"))
	case os.IsNotExist(err) || errors.Is(err, state.ErrCorrupt):
		if !os.IsNotExist(err) {
			g.logf("%v; starting fresh", err)
		}
		if err := g.initFresh(now, rx, tx); err != nil {
			return false, err
		}
	default:
		return false, err
	}
	if err := state.Save(g.StatePath, g.st); err != nil {
		return false, err
	}
	return g.st.FiredAt != 0, nil
}

// SeedState builds the state for the current cycle from the NIC counters (rx, tx)
// and the real usage so far, as reported by the user (`cdtguard init`).
func SeedState(now time.Time, mode string, rx, tx, usage uint64) *state.State {
	st := &state.State{PeriodStart: CycleStart(now).Unix(), SavedAt: now.Unix(), Rx: usage, Tx: usage, LastRx: rx, LastTx: tx}
	if mode == "total" {
		st.Rx, st.Tx = usage/2, usage-usage/2
	}
	return st
}

// initFresh seeds the state from the NIC counters (traffic since boot).
func (g *Guard) initFresh(now time.Time, rx, tx uint64) error {
	g.st = &state.State{PeriodStart: CycleStart(now).Unix(), SavedAt: now.Unix(), Rx: rx, Tx: tx, LastRx: rx, LastTx: tx}
	g.logf("no state file, starting from NIC counters: rx=%s tx=%s -> used %s", size.Format(rx), size.Format(tx), size.Format(g.used()))
	if g.Boot.Before(CycleStart(now)) {
		g.logf("WARNING: server booted %s, before this billing cycle began, so the counters include older traffic. Run `cdtguard init <real usage>` (see the Aliyun console) to start accurately.",
			g.Boot.In(beijing).Format("2006-01-02 15:04"))
	}
	if g.used() >= g.Limit {
		return fmt.Errorf("%w: %s used, limit %s. Not starting. Check this cycle's real usage in the Aliyun console, run `cdtguard init <usage>` (must be below the limit), then start again",
			ErrOverLimit, size.Format(g.used()), size.Format(g.Limit))
	}
	return nil
}

// FiredAt returns when the action ran this cycle (zero if it has not).
func (g *Guard) FiredAt() time.Time {
	if g.st == nil || g.st.FiredAt == 0 {
		return time.Time{}
	}
	return time.Unix(g.st.FiredAt, 0)
}

// advance folds the counter readings into the state, handling cycle rollover and
// reboots. rebooted says the counters restarted from 0 since the last save.
func (g *Guard) advance(now time.Time, rx, tx uint64, rebooted bool) {
	s := g.st
	incRx, incTx := rx-s.LastRx, tx-s.LastTx
	if rebooted || rx < s.LastRx || tx < s.LastTx {
		incRx, incTx = rx, tx
	}
	// Only ever roll forward: a clock stepped backwards (NTP after boot, VM time
	// sync) must not reset the usage or re-arm the action.
	if cs := CycleStart(now).Unix(); cs > s.PeriodStart {
		// New billing cycle: quota was reset, so is our accounting. Traffic since
		// the last sample may predate the reset; counting it here errs on the safe side.
		g.pendingReset = g.pendingReset || s.FiredAt != 0
		s.PeriodStart, s.FiredAt, s.Rx, s.Tx = cs, 0, incRx, incTx
		g.logf("new billing cycle started, usage reset")
	} else {
		s.Rx += incRx
		s.Tx += incTx
	}
	s.LastRx, s.LastTx, s.SavedAt = rx, tx, now.Unix()
}

// Step samples the counters once, persists, and fires the action if due.
// With fire=false it only checkpoints (used on shutdown).
func (g *Guard) Step(fire bool) error {
	rx, tx, err := g.Counter()
	if err != nil {
		return err
	}
	now := g.now()
	g.advance(now, rx, tx, false)

	if g.pendingReset {
		g.pendingReset = false
		if err := state.Save(g.StatePath, g.st); err != nil {
			return err
		}
		g.runHook("reset", g.ResetCmd)
	}
	if !fire || g.used() < g.Limit || g.st.FiredAt != 0 {
		return state.Save(g.StatePath, g.st)
	}

	// Persist "fired" BEFORE running: a shutdown command may kill us immediately,
	// and after the user powers the server back on we must not fire again this month.
	g.st.FiredAt = now.Unix()
	if err := state.Save(g.StatePath, g.st); err != nil {
		return err
	}
	g.logf("limit reached (%s >= %s), running: %s", size.Format(g.used()), size.Format(g.Limit), g.Command)
	if err := g.exec(g.Command); err != nil {
		var ee *exec.ExitError
		if errors.As(err, &ee) && !ee.Exited() {
			// Killed by a signal: most likely systemd is stopping us (and the
			// command) because the command is shutting the system down.
			g.logf("action terminated by %v; treating it as executed", err)
			return nil
		}
		// The command itself failed, so nothing was shut down; allow a retry.
		g.st.FiredAt = 0
		_ = state.Save(g.StatePath, g.st)
		return fmt.Errorf("action failed: %w", err)
	}
	return nil
}

func (g *Guard) runHook(name, cmd string) {
	if cmd == "" {
		return
	}
	g.logf("running %s command: %s", name, cmd)
	if err := g.exec(cmd); err != nil {
		g.logf("%s command failed: %v", name, err)
	}
}

func (g *Guard) exec(cmd string) error {
	if g.Exec != nil {
		return g.Exec(cmd)
	}
	c := exec.Command("sh", "-c", cmd)
	c.Stdout, c.Stderr = os.Stdout, os.Stderr
	return c.Run()
}

// Run initialises, then samples every Interval until ctx is cancelled.
func (g *Guard) Run(ctx context.Context) error {
	fired, err := g.Init()
	if err != nil {
		return err
	}
	if fired {
		g.logf("NOTICE: action already executed this cycle at %s; it will NOT run again until the quota resets (1st, 12:00 Beijing time). Monitoring continues.",
			g.FiredAt().In(beijing).Format("2006-01-02 15:04:05"))
		g.runHook("restore", g.RestoreCmd)
	}
	if err := g.Step(true); err != nil {
		g.logf("%v", err)
	}
	t := time.NewTicker(g.Interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return g.Step(false)
		case <-t.C:
			if err := g.Step(true); err != nil {
				g.logf("%v", err)
			}
		}
	}
}
