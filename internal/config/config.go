// Package config loads the runtime configuration used by cdtguard run.
package config

import (
	"fmt"
	"strings"

	"github.com/BurntSushi/toml"
)

// Config contains runtime options that are independent of the accounting state.
// StatePath deliberately does not live here: one config can be reused with
// multiple state files by passing -state on the command line.
type Config struct {
	Cmd        []string `toml:"cmd"`
	ResetCmd   []string `toml:"reset_cmd"`
	RestoreCmd []string `toml:"restore_cmd"`
	Iface      string   `toml:"iface"`
	Interval   string   `toml:"interval"`
}

// Load reads and validates a TOML runtime configuration.
func Load(path string) (Config, error) {
	var cfg Config
	meta, err := toml.DecodeFile(path, &cfg)
	if err != nil {
		return Config{}, err
	}
	if undecoded := meta.Undecoded(); len(undecoded) != 0 {
		return Config{}, fmt.Errorf("unknown config option %q", undecoded[0])
	}
	if len(cfg.Cmd) == 0 || allEmpty(cfg.Cmd) {
		return Config{}, fmt.Errorf("config option cmd must contain at least one argument")
	}
	if strings.TrimSpace(cfg.Interval) == "" {
		cfg.Interval = "1m"
	}
	return cfg, nil
}

func allEmpty(argv []string) bool {
	for _, arg := range argv {
		if strings.TrimSpace(arg) != "" {
			return false
		}
	}
	return true
}

// CommandString converts an argv-style TOML command into the shell command
// consumed by the guard. Quoting each argument preserves spaces and metacharacters.
func CommandString(argv []string) string {
	parts := make([]string, len(argv))
	for i, arg := range argv {
		parts[i] = shellQuote(arg)
	}
	return strings.Join(parts, " ")
}

func shellQuote(s string) string {
	if s == "" {
		return "''"
	}
	return "'" + strings.ReplaceAll(s, "'", "'\"'\"'") + "'"
}
