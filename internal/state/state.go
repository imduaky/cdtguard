// Package state persists the guard's accounting in a tiny fixed-size binary file.
//
// Layout (big endian, 63 bytes):
//
//	magic "CG" | version u8 | PeriodStart i64 | SavedAt i64 | Rx u64 | Tx u64 | LastRx u64 | LastTx u64 | FiredAt i64 | crc32 u32
package state

import (
	"encoding/binary"
	"errors"
	"fmt"
	"hash/crc32"
	"os"
	"path/filepath"
)

const (
	version = 1
	size    = 63
)

var (
	magic = [2]byte{'C', 'G'}
	// ErrCorrupt is returned when the file exists but cannot be decoded.
	ErrCorrupt = errors.New("state file corrupt")
)

// State is everything the guard remembers between runs.
type State struct {
	PeriodStart int64  // unix time the current billing cycle began
	SavedAt     int64  // unix time of the last update
	Rx, Tx      uint64 // bytes received / sent in the current cycle
	LastRx      uint64 // NIC rx counter at SavedAt
	LastTx      uint64 // NIC tx counter at SavedAt
	FiredAt     int64  // unix time the action ran this cycle; 0 = not yet
}

func (s *State) marshal() []byte {
	b := make([]byte, size)
	copy(b, magic[:])
	b[2] = version
	binary.BigEndian.PutUint64(b[3:], uint64(s.PeriodStart))
	binary.BigEndian.PutUint64(b[11:], uint64(s.SavedAt))
	binary.BigEndian.PutUint64(b[19:], s.Rx)
	binary.BigEndian.PutUint64(b[27:], s.Tx)
	binary.BigEndian.PutUint64(b[35:], s.LastRx)
	binary.BigEndian.PutUint64(b[43:], s.LastTx)
	binary.BigEndian.PutUint64(b[51:], uint64(s.FiredAt))
	binary.BigEndian.PutUint32(b[59:], crc32.ChecksumIEEE(b[:59]))
	return b
}

// Load reads path. A missing file yields an error satisfying os.IsNotExist.
func Load(path string) (*State, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	if len(b) != size || b[0] != magic[0] || b[1] != magic[1] || b[2] != version ||
		binary.BigEndian.Uint32(b[59:]) != crc32.ChecksumIEEE(b[:59]) {
		return nil, fmt.Errorf("%w: %s", ErrCorrupt, path)
	}
	return &State{
		PeriodStart: int64(binary.BigEndian.Uint64(b[3:])),
		SavedAt:     int64(binary.BigEndian.Uint64(b[11:])),
		Rx:          binary.BigEndian.Uint64(b[19:]),
		Tx:          binary.BigEndian.Uint64(b[27:]),
		LastRx:      binary.BigEndian.Uint64(b[35:]),
		LastTx:      binary.BigEndian.Uint64(b[43:]),
		FiredAt:     int64(binary.BigEndian.Uint64(b[51:])),
	}, nil
}

// Save writes atomically (temp file + fsync + rename) so a power cut never
// leaves a half-written file.
func Save(path string, s *State) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	tmp := path + ".tmp"
	f, err := os.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o644)
	if err != nil {
		return err
	}
	if _, err = f.Write(s.marshal()); err == nil {
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
