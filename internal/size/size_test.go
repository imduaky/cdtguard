package size

import "testing"

func TestParse(t *testing.T) {
	cases := map[string]uint64{
		"195G": 195 << 30, "1.5GB": 3 << 29, "512MiB": 512 << 20, "100": 100, " 2k ": 2048,
	}
	for in, want := range cases {
		got, err := Parse(in)
		if err != nil || got != want {
			t.Errorf("Parse(%q) = %d, %v; want %d", in, got, err, want)
		}
	}
	for _, bad := range []string{"", "abc", "-1G", "G"} {
		if _, err := Parse(bad); err == nil {
			t.Errorf("Parse(%q) should fail", bad)
		}
	}
}

func TestFormat(t *testing.T) {
	if got := Format(195 << 30); got != "195.00G" {
		t.Errorf("got %s", got)
	}
	if got := Format(5); got != "5B" {
		t.Errorf("got %s", got)
	}
}
