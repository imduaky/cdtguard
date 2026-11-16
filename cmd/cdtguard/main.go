package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/imduaky/cdtguard/internal/guard"
	"github.com/imduaky/cdtguard/internal/procfs"
	"github.com/imduaky/cdtguard/internal/size"
	"github.com/imduaky/cdtguard/internal/state"
)

var version = "dev"

// cst is Beijing time, which Aliyun bills in.
var cst = time.FixedZone("CST", 8*3600)

const usage = `cdtguard - monthly traffic guard for Aliyun CDT

Usage:
  cdtguard init [usage] [flags]  initialize this cycle's usage and config
  cdtguard run [flags]           run the daemon
  cdtguard status [flags]        show usage and history
  cdtguard version

Run "cdtguard <command> -h" for options and examples.`

// common holds the flags shared by every subcommand.
type common struct {
	iface, state string
}

func newFlags(name string, c *common) *flag.FlagSet {
	fs := flag.NewFlagSet(name, flag.ExitOnError)
	fs.StringVar(&c.state, "state", "/var/lib/cdtguard/state.bin", "state file path")
	if name == "run" {
		fs.StringVar(&c.iface, "iface", "", "network interface (defaults to the one saved by init)")
	}
	return fs
}

func (c *common) resolveIface() {
	if c.iface != "" {
		return
	}
	ifs, err := procfs.Interfaces()
	if err != nil {
		log.Fatalf("list interfaces: %v", err)
	}
	switch len(ifs) {
	case 0:
		log.Fatal("no network interface found")
	case 1:
		c.iface = ifs[0]
	default:
		log.Fatalf("multiple eligible interfaces %v found, please choose one with -iface", ifs)
	}
}

func main() {
	log.SetFlags(0) // journald adds timestamps
	if len(os.Args) < 2 {
		fmt.Fprintln(os.Stderr, usage)
		os.Exit(2)
	}
	args := os.Args[2:]
	switch os.Args[1] {
	case "run":
		runCmd(args)
	case "init":
		initCmd(args)
	case "status":
		statusCmd(args)
	case "version", "-version", "--version":
		fmt.Println("cdtguard", version)
	case "help", "-h", "--help":
		fmt.Println(usage)
	default:
		fmt.Fprintf(os.Stderr, "unknown command %q\n\n%s\n", os.Args[1], usage)
		os.Exit(2)
	}
}

func runCmd(args []string) {
	var c common
	fs := newFlags("run", &c)
	command := fs.String("cmd", "", "required shell command to run when the limit is reached")
	restore := fs.String("restore-cmd", "", "run at startup after firing this cycle; reapply state lost on reboot (e.g. traffic shaping)")
	reset := fs.String("reset-cmd", "", "run at the next cycle after firing; undo a reversible action (e.g. lift traffic shaping)")
	interval := fs.Duration("interval", time.Minute, "traffic sampling interval")
	fs.Usage = func() {
		fmt.Fprintln(os.Stderr, `Usage: cdtguard run [options]

Runs with configuration saved in the state file. -iface overrides its saved interface.

Options:`)
		fs.PrintDefaults()
		fmt.Fprintln(os.Stderr, `
Traffic shaping example (enable at limit, reapply after reboot, remove at cycle reset):
  cdtguard run \
    -cmd '/usr/local/bin/cdtguard-throttle on' \
    -restore-cmd '/usr/local/bin/cdtguard-throttle on' \
    -reset-cmd '/usr/local/bin/cdtguard-throttle off'

Safe test example (initialize a low limit and test state with cdtguard init first):
  cdtguard run -state /tmp/cdtguard-test.state -interval 1s \
    -cmd 'printf reached > /tmp/cdtguard-reached'`)
	}
	fs.Parse(args)
	if strings.TrimSpace(*command) == "" {
		log.Fatal("-cmd is required (set it in the service environment file or pass it to cdtguard run)")
	}
	if *interval < time.Second {
		log.Fatal("-interval must be at least 1s")
	}
	st, err := state.Load(c.state)
	if err != nil && !os.IsNotExist(err) {
		log.Fatalf("read state: %v", err)
	}
	limit, mode, savedIface := uint64(0), "", ""
	if st != nil {
		limit, mode, savedIface = st.Config()
	}
	if c.iface == "" {
		if savedIface != "" {
			c.iface = savedIface
		} else {
			c.resolveIface()
		}
	}
	if limit == 0 || mode == "" {
		limit, mode = 195<<30, "max"
		rx, tx, err := procfs.Counters(c.iface)
		if err != nil {
			log.Fatalf("read counters: %v", err)
		}
		if st == nil {
			now := time.Now()
			st = &state.State{
				PeriodStart: guard.CycleStart(now).Unix(), SavedAt: now.Unix(),
				Rx: rx, Tx: tx, LastRx: rx, LastTx: tx,
			}
		} else {
			st.LastRx, st.LastTx, st.SavedAt = rx, tx, time.Now().Unix()
			st.SetConfig(limit, mode, c.iface)
		}
		if used := guard.Usage(mode, st.Rx, st.Tx); used >= limit {
			log.Fatalf("initial usage %s is at or above the default limit %s; run `cdtguard init <usage> -limit <limit> -iface %s` with the real usage", size.Format(used), size.Format(limit), c.iface)
		}
		st.SetConfig(limit, mode, c.iface)
		if err := state.Save(c.state, st); err != nil {
			log.Fatalf("write initial state: %v", err)
		}
		log.Printf("no initialized configuration found; using defaults: limit=%s mode=%s iface=%s", size.Format(limit), mode, c.iface)
	} else if c.iface != savedIface {
		log.Printf("using iface override %s (saved default is %s)", c.iface, savedIface)
		rx, tx, err := procfs.Counters(c.iface)
		if err != nil {
			log.Fatalf("read counters for -iface override: %v", err)
		}
		st.LastRx, st.LastTx = rx, tx
		st.SavedAt = time.Now().Unix()
		st.SetConfig(limit, mode, c.iface)
		if err := state.Save(c.state, st); err != nil {
			log.Fatalf("save -iface override: %v", err)
		}
	}
	boot, err := procfs.BootTime()
	if err != nil {
		log.Fatalf("read boot time: %v", err)
	}

	g := &guard.Guard{
		StatePath:  c.state,
		Limit:      limit,
		Mode:       mode,
		Command:    *command,
		ResetCmd:   *reset,
		RestoreCmd: *restore,
		Interval:   *interval,
		Boot:       boot,
		Logf:       log.Printf,
		Counter:    func() (uint64, uint64, error) { return procfs.Counters(c.iface) },
	}
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	log.Printf("cdtguard %s: iface=%s mode=%s limit=%s cmd=%q", version, c.iface, mode, size.Format(limit), *command)
	if err := g.Run(ctx); err != nil {
		if errors.Is(err, guard.ErrOverLimit) {
			log.Print(err)
			os.Exit(2) // systemd: RestartPreventExitStatus=2
		}
		log.Fatal(err)
	}
}

func initCmd(args []string) {
	var c common
	fs := newFlags("init", &c)
	fs.StringVar(&c.iface, "iface", "", "network interface (auto-selected when exactly one eligible NIC is found)")
	limitText := fs.String("limit", "195G", "traffic limit per billing cycle")
	mode := fs.String("mode", "max", "billing mode: max, total, tx or rx; max matches Aliyun CDT")
	fs.Usage = func() {
		fmt.Fprintln(os.Stderr, `Usage: cdtguard init [current-usage] [options]

If usage is omitted, the selected NIC's current counters are used as initial usage.

Options:`)
		fs.PrintDefaults()
		fmt.Fprintln(os.Stderr, `
Examples:
  cdtguard init 20G -limit 195G -iface ens2 -mode max
  cdtguard init -limit 195G -iface ens2 -mode max`)
	}
	fs.Parse(reorderInitArgs(args))
	limit, err := size.Parse(*limitText)
	if err != nil || limit == 0 {
		log.Fatalf("invalid -limit %q", *limitText)
	}
	switch *mode {
	case "max", "total", "tx", "rx":
	default:
		log.Fatalf("invalid -mode %q (want max, total, tx or rx)", *mode)
	}
	if fs.NArg() > 1 {
		log.Fatal("usage: cdtguard init [usage] [-limit 195G] [-mode max] [-iface eth0]")
	}
	c.resolveIface()
	rx, tx, err := procfs.Counters(c.iface)
	if err != nil {
		log.Fatalf("read counters: %v", err)
	}
	used := guard.Usage(*mode, rx, tx)
	if fs.NArg() == 1 {
		used, err = size.Parse(fs.Arg(0))
		if err != nil {
			log.Fatalf("invalid usage %q", fs.Arg(0))
		}
	}
	if used >= limit {
		log.Fatalf("usage %s is not below the limit %s; choose an initial usage below the limit", size.Format(used), size.Format(limit))
	}
	prev, _ := state.Load(c.state) // keep its traffic history, if readable
	st := guard.SeedState(time.Now(), *mode, rx, tx, used, prev)
	if fs.NArg() == 0 {
		st.Rx, st.Tx = rx, tx
	}
	st.SetConfig(limit, *mode, c.iface)
	if err := state.Save(c.state, st); err != nil {
		log.Fatalf("write state: %v", err)
	}
	fmt.Printf("state written to %s: used %s / %s (iface %s, mode %s).\n", c.state, size.Format(used), size.Format(limit), c.iface, *mode)
	fmt.Println("Start the daemon with: systemctl start cdtguard (systemd) or /etc/init.d/cdtguard start (OpenRC).")
}

// reorderInitArgs lets the optional usage appear before flags, as in
// `cdtguard init 20G --limit 195G -iface eth0`.
func reorderInitArgs(args []string) []string {
	var flags, positional []string
	valueNext := false
	for _, arg := range args {
		if valueNext {
			flags = append(flags, arg)
			valueNext = false
			continue
		}
		if strings.HasPrefix(arg, "-") {
			flags = append(flags, arg)
			if !strings.Contains(arg, "=") {
				name := strings.TrimLeft(arg, "-")
				valueNext = name == "iface" || name == "limit" || name == "mode" || name == "state"
			}
			continue
		}
		positional = append(positional, arg)
	}
	return append(flags, positional...)
}

func statusCmd(args []string) {
	var c common
	fs := newFlags("status", &c)
	fs.Usage = func() {
		fmt.Fprintln(os.Stderr, "Usage: cdtguard status [-state path]")
		fs.PrintDefaults()
	}
	fs.Parse(args)
	st, err := state.Load(c.state)
	if err != nil {
		log.Fatalf("read state: %v", err)
	}
	limit, mode, iface := st.Config()
	if limit == 0 || mode == "" {
		log.Fatal("state file has no configuration; run `cdtguard init` first")
	}
	used := guard.Usage(mode, st.Rx, st.Tx)
	fmt.Printf("used:    %s / %s (%.1f%%)  [rx %s, tx %s, mode %s, iface %s]\n", size.Format(used), size.Format(limit), float64(used)*100/float64(limit), size.Format(st.Rx), size.Format(st.Tx), mode, iface)
	fmt.Printf("cycle:   since %s\n", time.Unix(st.PeriodStart, 0).In(cst).Format("2006-01-02 15:04 MST"))
	fmt.Printf("updated: %s\n", time.Unix(st.SavedAt, 0).Format(time.DateTime))
	if st.FiredAt != 0 {
		fmt.Printf("fired:   %s (will not run again this cycle)\n", time.Unix(st.FiredAt, 0).Format(time.DateTime))
	} else {
		fmt.Println("fired:   no")
	}
	printDaily(st)
	printHourly(st)
	printHistory(st, mode)
}

func row(label string, rx, tx uint64, extra ...string) {
	fmt.Printf("  %-18s %10s %10s", label, size.Format(rx), size.Format(tx))
	for _, e := range extra {
		fmt.Printf("  %s", e)
	}
	fmt.Println()
}

func printDaily(st *state.State) {
	fmt.Printf("\n%-20s %10s %10s\n", "daily (CST)", "rx", "tx")
	start := time.Unix(st.PeriodStart, 0).In(cst)
	var sum state.Traffic
	for i := range guard.DayIndex(st.PeriodStart, time.Unix(st.SavedAt, 0)) + 1 {
		d := st.Daily[i]
		sum.Add(d.Rx, d.Tx)
		row(start.AddDate(0, 0, i).Format("01-02 Mon"), d.Rx, d.Tx)
	}
	// Counted before per-day tracking began (NIC counters at first start, `init`, older version).
	if rx, tx := st.Rx-min(sum.Rx, st.Rx), st.Tx-min(sum.Tx, st.Tx); rx|tx != 0 {
		row("untracked", rx, tx, "(no per-day data)")
	}
}

func printHourly(st *state.State) {
	if st.LastHour == 0 {
		return
	}
	fmt.Printf("\n%-20s %10s %10s\n", "last 48 hours (CST)", "rx", "tx")
	started := false // skip the empty hours before tracking began
	for h := st.LastHour - state.Hours + 1; h <= st.LastHour; h++ {
		t := st.Hourly[h%state.Hours]
		if started = started || t != (state.Traffic{}); started {
			row(time.Unix(h*3600, 0).In(cst).Format("01-02 15:00"), t.Rx, t.Tx)
		}
	}
}

func printHistory(st *state.State, mode string) {
	if st.History[0].Start == 0 {
		return
	}
	fmt.Printf("\n%-20s %10s %10s  %10s  %s\n", "past cycles (CST)", "rx", "tx", "used", "fired")
	for _, c := range st.History {
		if c.Start == 0 {
			break
		}
		fired := "-"
		if c.FiredAt != 0 {
			fired = time.Unix(c.FiredAt, 0).In(cst).Format("01-02 15:04")
		}
		row(time.Unix(c.Start, 0).In(cst).Format("2006-01-02 15:04"), c.Rx, c.Tx, fmt.Sprintf("%10s", size.Format(guard.Usage(mode, c.Rx, c.Tx))), fired)
	}
}
