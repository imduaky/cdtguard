// Package size parses and formats byte sizes using binary (1024-based) units.
package size

import (
	"fmt"
	"strconv"
	"strings"
)

var units = []struct {
	suffix string
	mult   float64
}{
	{"T", 1 << 40}, {"G", 1 << 30}, {"M", 1 << 20}, {"K", 1 << 10},
}

// Parse accepts values like "195G", "1.5GB", "512MiB" or a plain byte count.
func Parse(s string) (uint64, error) {
	t := strings.ToUpper(strings.TrimSpace(s))
	t = strings.TrimSuffix(strings.TrimSuffix(t, "IB"), "B")
	mult := 1.0
	for _, u := range units {
		if strings.HasSuffix(t, u.suffix) {
			mult = u.mult
			t = strings.TrimSuffix(t, u.suffix)
			break
		}
	}
	v, err := strconv.ParseFloat(strings.TrimSpace(t), 64)
	if err != nil || v < 0 {
		return 0, fmt.Errorf("invalid size %q", s)
	}
	return uint64(v * mult), nil
}

// Format renders n with the largest fitting unit, e.g. "12.34G".
func Format(n uint64) string {
	f := float64(n)
	for _, u := range units {
		if f >= u.mult {
			return fmt.Sprintf("%.2f%s", f/u.mult, u.suffix)
		}
	}
	return fmt.Sprintf("%dB", n)
}
