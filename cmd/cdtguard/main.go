package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log"
	"os"
	"os/signal"
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
  cdtguard run    [flags]           run the daemon
  cdtguard init   [flags] <usage>   set this cycle's real usage (e.g. 120G); stop the daemon first
  cdtguard status [flags]           show the saved state
  cdtguard version

Run "cdtguard <command> -h" for flags.`

// common holds the flags shared by every subcommand.
type common struct {
	iface, limit, state, mode string
}

func newFlags(name string, c *common) *flag.FlagSet {
	fs := flag.NewFlagSet(name, flag.ExitOnError)
	fs.StringVar(&c.state, "state", "/var/lib/cdtguard/state.bin", "state file path")
	fs.StringVar(&c.limit, "limit", "195G", "traffic limit per billing cycle")
	fs.StringVar(&c.mode, "mode", "max", "what to bill: max (max(rx,tx), Aliyun CDT), total (rx+tx), tx, rx")
	if name != "status" {
		fs.StringVar(&c.iface, "iface", "", "network interface (auto-detected when the host has exactly one non-lo NIC)")
	}
	return fs
}

// parse validates the shared flags and returns the limit in bytes.
func (c *common) parse() uint64 {
	limit, err := size.Parse(c.limit)
	if err != nil || limit == 0 {
		log.Fatalf("invalid -limit %q", c.limit)
	}
	switch c.mode {
	case "max", "total", "tx", "rx":
	default:
		log.Fatalf("invalid -mode %q (want max, total, tx or rx)", c.mode)
	}
	return limit
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
		log.Fatalf("multiple interfaces %v found, please choose one with -iface", ifs)
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
	command := fs.String("cmd", "shutdown now", "shell command to run when the limit is reached")
	resetCmd := fs.String("reset-cmd", "", "shell command to run when a new cycle begins after -cmd had run (e.g. lift a speed limit)")
	restore := fs.String("restore-cmd", "", "shell command to run at startup when -cmd already ran this cycle (e.g. re-apply a speed limit after reboot)")
	interval := fs.Duration("interval", time.Minute, "sampling interval")
	fs.Parse(args)
	limit := c.parse()
	if *interval < time.Second {
		log.Fatal("-interval must be at least 1s")
	}
	c.resolveIface()
	boot, err := procfs.BootTime()
	if err != nil {
		log.Fatalf("read boot time: %v", err)
	}

	g := &guard.Guard{
		StatePath:  c.state,
		Limit:      limit,
		Mode:       c.mode,
		Command:    *command,
		ResetCmd:   *resetCmd,
		RestoreCmd: *restore,
		Interval:   *interval,
		Boot:       boot,
		Logf:       log.Printf,
		Counter:    func() (uint64, uint64, error) { return procfs.Counters(c.iface) },
	}
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	log.Printf("cdtguard %s: iface=%s mode=%s limit=%s cmd=%q", version, c.iface, c.mode, size.Format(limit), *command)
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
	fs.Parse(args)
	limit := c.parse()
	if fs.NArg() != 1 {
		log.Fatal("usage: cdtguard init [flags] <usage>   e.g. cdtguard init 120G")
	}
	used, err := size.Parse(fs.Arg(0))
	if err != nil {
		log.Fatalf("invalid usage %q", fs.Arg(0))
	}
	if used >= limit {
		log.Fatalf("usage %s is not below the limit %s; the daemon would refuse to start", size.Format(used), size.Format(limit))
	}
	c.resolveIface()
	rx, tx, err := procfs.Counters(c.iface)
	if err != nil {
		log.Fatalf("read counters: %v", err)
	}
	prev, _ := state.Load(c.state) // keep its traffic history, if readable
	if err := state.Save(c.state, guard.SeedState(time.Now(), c.mode, rx, tx, used, prev)); err != nil {
		log.Fatalf("write state: %v", err)
	}
	fmt.Printf("state written to %s: used %s / %s (iface %s). Start the daemon now.\n", c.state, size.Format(used), size.Format(limit), c.iface)
}

func statusCmd(args []string) {
	var c common
	fs := newFlags("status", &c)
	fs.Parse(args)
	limit := c.parse()
	st, err := state.Load(c.state)
	if err != nil {
		log.Fatalf("read state: %v", err)
	}
	used := guard.Usage(c.mode, st.Rx, st.Tx)
	fmt.Printf("used:    %s / %s (%.1f%%)  [rx %s, tx %s, mode %s]\n", size.Format(used), size.Format(limit), float64(used)*100/float64(limit), size.Format(st.Rx), size.Format(st.Tx), c.mode)
	fmt.Printf("cycle:   since %s\n", time.Unix(st.PeriodStart, 0).In(cst).Format("2006-01-02 15:04 MST"))
	fmt.Printf("updated: %s\n", time.Unix(st.SavedAt, 0).Format(time.DateTime))
	if st.FiredAt != 0 {
		fmt.Printf("fired:   %s (will not run again this cycle)\n", time.Unix(st.FiredAt, 0).Format(time.DateTime))
	} else {
		fmt.Println("fired:   no")
	}
	printDaily(st)
	printHourly(st)
	printHistory(st, c.mode)
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
