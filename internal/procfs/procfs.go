// Package procfs reads NIC counters, boot time and interface lists from Linux.
package procfs

import (
	"bufio"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"
)

// Overridable in tests.
var (
	SysNet   = "/sys/class/net"
	ProcStat = "/proc/stat"
)

// Interfaces lists network interfaces excluding loopback, sorted by name.
func Interfaces() ([]string, error) {
	ents, err := os.ReadDir(SysNet)
	if err != nil {
		return nil, err
	}
	var out []string
	for _, e := range ents {
		if MonitoredInterface(e.Name()) {
			out = append(out, e.Name())
		}
	}
	sort.Strings(out)
	return out, nil
}

// MonitoredInterface reports whether an interface name is likely to represent
// a physical or cloud NIC rather than a local, VPN, container, or bridge link.
func MonitoredInterface(name string) bool {
	if name == "lo" {
		return false
	}
	for _, prefix := range []string{
		"tailscale", "docker", "br-", "veth", "tun", "tap", "wg",
		"cni", "flannel", "virbr", "podman", "zt", "lima",
	} {
		if strings.HasPrefix(name, prefix) {
			return false
		}
	}
	return true
}

// Counters returns the cumulative rx/tx byte counters since boot.
func Counters(iface string) (rx, tx uint64, err error) {
	if rx, err = readUint(filepath.Join(SysNet, iface, "statistics", "rx_bytes")); err != nil {
		return
	}
	tx, err = readUint(filepath.Join(SysNet, iface, "statistics", "tx_bytes"))
	return
}

// BootTime returns the system boot time from /proc/stat's btime line.
func BootTime() (time.Time, error) {
	f, err := os.Open(ProcStat)
	if err != nil {
		return time.Time{}, err
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		if v, ok := strings.CutPrefix(sc.Text(), "btime "); ok {
			n, err := strconv.ParseInt(strings.TrimSpace(v), 10, 64)
			return time.Unix(n, 0), err
		}
	}
	return time.Time{}, fmt.Errorf("btime not found in %s", ProcStat)
}

func readUint(path string) (uint64, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return 0, err
	}
	return strconv.ParseUint(strings.TrimSpace(string(b)), 10, 64)
}
