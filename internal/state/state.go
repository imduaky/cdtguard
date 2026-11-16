// Package state persists the guard's accounting in a small binary file.
//
// Layout: magic "CG" | version u8 | State (big endian, field order) | 0... | crc32 u32
//
// The file is padded with zeros to fill whole filesystem blocks (one 4 KiB block
// in practice). The crc32 is always the last 4 bytes and covers everything before
// it.
package state

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"hash/crc32"
	"os"
	"path/filepath"
	"syscall"
)

// Sizes of the traffic history.
const (
	Days   = 32 // calendar days a billing cycle can touch (1st 12:00 to next 1st 12:00)
	Hours  = 48 // hourly ring buffer
	Cycles = 12 // finished billing cycles kept
)

const (
	version = 3

	defaultBlock = 4096
	maxBlock     = 64 << 10
)

var (
	magic    = [2]byte{'C', 'G'}
	bodySize = binary.Size(State{})
	minSize  = 3 + bodySize + 4
	// ErrCorrupt is returned when the file exists but cannot be decoded.
	ErrCorrupt = errors.New("state file corrupt")
)

// Traffic is bytes received and sent in some period.
type Traffic struct{ Rx, Tx uint64 }

func (t *Traffic) Add(rx, tx uint64) { t.Rx += rx; t.Tx += tx }

// Cycle summarises a finished billing cycle.
type Cycle struct {
	Start int64 // unix time the cycle began; 0 = empty slot
	Traffic
	FiredAt int64
}

// State is everything the guard remembers between runs. Fields are encoded in
// declaration order.
type State struct {
	PeriodStart int64  // unix time the current billing cycle began
	SavedAt     int64  // unix time of the last update
	Rx, Tx      uint64 // bytes received / sent in the current cycle
	LastRx      uint64 // NIC rx counter at SavedAt
	LastTx      uint64 // NIC tx counter at SavedAt
	FiredAt     int64  // unix time the action ran this cycle; 0 = not yet
	// Traffic history.
	Daily    [Days]Traffic  // per Beijing calendar day; index 0 = the day the cycle began
	LastHour int64          // unix hour (unix time / 3600) of the newest Hourly slot
	Hourly   [Hours]Traffic // slot h%Hours holds unix hour h, for the Hours up to LastHour
	History  [Cycles]Cycle  // finished cycles, newest first
	// Configuration written by `cdtguard init`.
	Limit uint64
	Mode  [8]byte
	Iface [16]byte
}

// SetConfig stores the configuration selected by `cdtguard init`.
func (s *State) SetConfig(limit uint64, mode, iface string) {
	s.Limit = limit
	s.Mode = [8]byte{}
	s.Iface = [16]byte{}
	copy(s.Mode[:], mode)
	copy(s.Iface[:], iface)
}

// Config returns the saved limit, mode, and interface.
func (s *State) Config() (limit uint64, mode, iface string) {
	return s.Limit, string(bytes.TrimRight(s.Mode[:], "\x00")), string(bytes.TrimRight(s.Iface[:], "\x00"))
}

// EndCycle archives the current cycle into History and clears the per-cycle
// counters, ready for a new cycle beginning at start.
func (s *State) EndCycle(start int64) {
	if s.PeriodStart != 0 {
		copy(s.History[1:], s.History[:])
		s.History[0] = Cycle{Start: s.PeriodStart, Traffic: Traffic{s.Rx, s.Tx}, FiredAt: s.FiredAt}
	}
	s.PeriodStart, s.FiredAt, s.Rx, s.Tx, s.Daily = start, 0, 0, 0, [Days]Traffic{}
}

// AddHourly adds traffic to the slot for unix hour h, clearing the slots skipped
// since LastHour. An h older than LastHour (clock stepped back) counts in LastHour.
func (s *State) AddHourly(h int64, rx, tx uint64) {
	switch {
	case s.LastHour == 0 || h-s.LastHour >= Hours:
		s.Hourly, s.LastHour = [Hours]Traffic{}, h
	case h > s.LastHour:
		for s.LastHour < h {
			s.LastHour++
			s.Hourly[s.LastHour%Hours] = Traffic{}
		}
	}
	s.Hourly[s.LastHour%Hours].Add(rx, tx)
}

func (s *State) marshal(n int) []byte {
	b := make([]byte, n)
	copy(b, magic[:])
	b[2] = version
	if _, err := binary.Encode(b[3:], binary.BigEndian, s); err != nil {
		panic(err) // fixed-size struct, n >= minSize
	}
	binary.BigEndian.PutUint32(b[n-4:], crc32.ChecksumIEEE(b[:n-4]))
	return b
}

// fileSize rounds minSize up to whole blocks of the filesystem holding dir: the
// file takes whole blocks on disk regardless, so it might as well fill them.
func fileSize(dir string) int {
	bs := defaultBlock
	var fs syscall.Statfs_t
	if syscall.Statfs(dir, &fs) == nil && fs.Bsize > 0 && fs.Bsize <= maxBlock {
		bs = int(fs.Bsize)
	}
	return (minSize + bs - 1) / bs * bs
}

// Load reads path. A missing file yields an error satisfying os.IsNotExist.
func Load(path string) (*State, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	n := len(b)
	ok := n >= minSize && n <= maxBlock && b[0] == magic[0] && b[1] == magic[1] &&
		binary.BigEndian.Uint32(b[n-4:]) == crc32.ChecksumIEEE(b[:n-4]) &&
		b[2] == version
	if !ok {
		return nil, fmt.Errorf("%w: %s", ErrCorrupt, path)
	}
	s := new(State)
	if _, err := binary.Decode(b[3:3+bodySize], binary.BigEndian, s); err != nil {
		return nil, fmt.Errorf("%w: %s: %v", ErrCorrupt, path, err)
	}
	return s, nil
}

// Save writes atomically (temp file + fsync + rename) so a power cut never
// leaves a half-written file.
func Save(path string, s *State) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	tmp := path + ".tmp"
	f, err := os.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o644)
	if err != nil {
		return err
	}
	if _, err = f.Write(s.marshal(fileSize(dir))); err == nil {
		err = f.Sync()
	}
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		os.Remove(tmp)
		return err
	}
	return os.Rename(tmp, path)
}
