package procfs

import (
	"os"
	"path/filepath"
	"testing"
)

func TestProcfs(t *testing.T) {
	d := t.TempDir()
	SysNet, ProcStat = filepath.Join(d, "net"), filepath.Join(d, "stat")
	os.MkdirAll(filepath.Join(SysNet, "lo"), 0o755)
	st := filepath.Join(SysNet, "eth0", "statistics")
	os.MkdirAll(st, 0o755)
	os.WriteFile(filepath.Join(st, "rx_bytes"), []byte("10\n"), 0o644)
	os.WriteFile(filepath.Join(st, "tx_bytes"), []byte("20\n"), 0o644)
	os.WriteFile(ProcStat, []byte("cpu 1 2\nbtime 1700000000\n"), 0o644)

	if ifs, _ := Interfaces(); len(ifs) != 1 || ifs[0] != "eth0" {
		t.Errorf("interfaces = %v", ifs)
	}
	if rx, tx, err := Counters("eth0"); err != nil || rx != 10 || tx != 20 {
		t.Errorf("counters = %d %d %v", rx, tx, err)
	}
	if bt, err := BootTime(); err != nil || bt.Unix() != 1700000000 {
		t.Errorf("boot = %v %v", bt, err)
	}
}
