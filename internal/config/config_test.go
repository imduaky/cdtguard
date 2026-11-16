package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLoadAndQuoteCommands(t *testing.T) {
	path := filepath.Join(t.TempDir(), "cdtguard.conf")
	contents := `cmd = ["systemctl", "stop", "sing-box"]
restore_cmd = ["systemctl", "stop", "sing-box"]
reset_cmd = ["systemctl", "restart", "sing-box"]
iface = "eth0"
`
	if err := os.WriteFile(path, []byte(contents), 0600); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := CommandString(cfg.Cmd); got != "'systemctl' 'stop' 'sing-box'" {
		t.Fatalf("command = %q", got)
	}
	if cfg.Iface != "eth0" || cfg.Interval != "1m" {
		t.Fatalf("config = %+v", cfg)
	}
}

func TestLoadRejectsMissingCommand(t *testing.T) {
	path := filepath.Join(t.TempDir(), "cdtguard.conf")
	if err := os.WriteFile(path, []byte(`interval = "1m"`), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(path); err == nil {
		t.Fatal("expected missing cmd error")
	}
}
