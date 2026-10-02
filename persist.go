package slogate

import (
	"bytes"
	"encoding/binary"
	"hash/crc32"
	"io"
	"math"
	"os"
	"path/filepath"
	"time"
)

// StateVersion is the on-disk format version produced by this build.
const StateVersion byte = 1

var stateMagic = [4]byte{'S', 'L', 'G', 'T'}

// Wire layout (little-endian):
//
//	magic[4]="SLGT" version u8
//	cfg:       min f64, max f64, relErr f64
//	source:    idLen u16, id bytes, epoch u64
//	meta:      seq u64, createdAtNano u64, takenAtNano u64
//	counts:    total,under,over,errors u64 ; sum,underMin,overMax f64
//	buckets:   bucketCount u32, then bucketCount * u64
//	crc32(IEEE) u32 over every preceding byte
//
// Any truncated, flipped or otherwise corrupted payload fails the CRC or
// structural checks and Decode returns ErrCorruptState.

func appendU64(b []byte, v uint64) []byte {
	var buf [8]byte
	binary.LittleEndian.PutUint64(buf[:], v)
	return append(b, buf[:]...)
}

func appendF64(b []byte, v float64) []byte {
	return appendU64(b, math.Float64bits(v))
}

// Encode writes a checksummed, self-describing snapshot to w.
func Encode(w io.Writer, s Snapshot) error {
	if err := s.Config.Validate(); err != nil {
		return err
	}
	if err := s.validate(); err != nil {
		return ErrCorruptState
	}
	if len(s.Source.ID) > 65535 {
		return ErrInvalidArgument
	}
	b := make([]byte, 0, 64+8*len(s.Counts))
	b = append(b, stateMagic[:]...)
	b = append(b, StateVersion)
	b = appendF64(b, s.Config.MinValue)
	b = appendF64(b, s.Config.MaxValue)
	b = appendF64(b, s.Config.RelativeError)
	var lnb [2]byte
	binary.LittleEndian.PutUint16(lnb[:], uint16(len(s.Source.ID)))
	b = append(b, lnb[:]...)
	b = append(b, s.Source.ID...)
	b = appendU64(b, s.Source.Epoch)
	b = appendU64(b, s.Seq)
	b = appendU64(b, uint64(s.CreatedAt.UnixNano()))
	b = appendU64(b, uint64(s.TakenAt.UnixNano()))
	b = appendU64(b, s.Total)
	b = appendU64(b, s.Underflow)
	b = appendU64(b, s.Overflow)
	b = appendU64(b, s.Errors)
	b = appendF64(b, s.Sum)
	b = appendF64(b, s.UnderflowMin)
	b = appendF64(b, s.OverflowMax)
	var cnt [4]byte
	binary.LittleEndian.PutUint32(cnt[:], uint32(len(s.Counts)))
	b = append(b, cnt[:]...)
	for _, c := range s.Counts {
		b = appendU64(b, c)
	}
	sum := crc32.ChecksumIEEE(b)
	var crcb [4]byte
	binary.LittleEndian.PutUint32(crcb[:], sum)
	b = append(b, crcb[:]...)
	_, err := w.Write(b)
	return err
}

type decoder struct {
	data []byte
	pos  int
}

func (d *decoder) u8() (byte, error) {
	if d.pos+1 > len(d.data) {
		return 0, io.ErrUnexpectedEOF
	}
	v := d.data[d.pos]
	d.pos++
	return v, nil
}

func (d *decoder) u16() (uint16, error) {
	if d.pos+2 > len(d.data) {
		return 0, io.ErrUnexpectedEOF
	}
	v := binary.LittleEndian.Uint16(d.data[d.pos:])
	d.pos += 2
	return v, nil
}

func (d *decoder) u32() (uint32, error) {
	if d.pos+4 > len(d.data) {
		return 0, io.ErrUnexpectedEOF
	}
	v := binary.LittleEndian.Uint32(d.data[d.pos:])
	d.pos += 4
	return v, nil
}

func (d *decoder) u64() (uint64, error) {
	if d.pos+8 > len(d.data) {
		return 0, io.ErrUnexpectedEOF
	}
	v := binary.LittleEndian.Uint64(d.data[d.pos:])
	d.pos += 8
	return v, nil
}

func (d *decoder) f64() (float64, error) {
	v, err := d.u64()
	if err != nil {
		return 0, err
	}
	return math.Float64frombits(v), nil
}

func (d *decoder) bytes(n int) ([]byte, error) {
	if d.pos+n > len(d.data) {
		return nil, io.ErrUnexpectedEOF
	}
	v := d.data[d.pos : d.pos+n]
	d.pos += n
	return v, nil
}

// Decode reads and verifies a snapshot. The payload must have been written
// for cfg (same layout); any checksum, truncation, version or consistency
// failure returns an error wrapping ErrCorruptState/ErrUnsupportedVer and
// no usable Snapshot, so callers cannot silently accumulate onto bad data.
func Decode(r io.Reader, cfg HistogramConfig) (Snapshot, error) {
	if err := cfg.Validate(); err != nil {
		return Snapshot{}, err
	}
	data, err := io.ReadAll(r)
	if err != nil {
		return Snapshot{}, err
	}
	if len(data) < 4+1+4 {
		return Snapshot{}, ErrCorruptState
	}
	wantCRC := binary.LittleEndian.Uint32(data[len(data)-4:])
	body := data[:len(data)-4]
	if crc32.ChecksumIEEE(body) != wantCRC {
		return Snapshot{}, ErrCorruptState
	}
	d := &decoder{data: body}
	magic, err := d.bytes(4)
	if err != nil || !bytes.Equal(magic, stateMagic[:]) {
		return Snapshot{}, ErrCorruptState
	}
	ver, err := d.u8()
	if err != nil {
		return Snapshot{}, ErrCorruptState
	}
	if ver != StateVersion {
		return Snapshot{}, ErrUnsupportedVer
	}
	var s Snapshot
	if s.Config.MinValue, err = d.f64(); err != nil {
		return Snapshot{}, ErrCorruptState
	}
	if s.Config.MaxValue, err = d.f64(); err != nil {
		return Snapshot{}, ErrCorruptState
	}
	if s.Config.RelativeError, err = d.f64(); err != nil {
		return Snapshot{}, ErrCorruptState
	}
	if !s.Config.equal(cfg) {
		return Snapshot{}, ErrConfigMismatch
	}
	idLen, err := d.u16()
	if err != nil {
		return Snapshot{}, ErrCorruptState
	}
	id, err := d.bytes(int(idLen))
	if err != nil {
		return Snapshot{}, ErrCorruptState
	}
	s.Source.ID = string(id)
	if s.Source.Epoch, err = d.u64(); err != nil {
		return Snapshot{}, ErrCorruptState
	}
	if s.Seq, err = d.u64(); err != nil {
		return Snapshot{}, ErrCorruptState
	}
	createdNano, err := d.u64()
	if err != nil {
		return Snapshot{}, ErrCorruptState
	}
	takenNano, err := d.u64()
	if err != nil {
		return Snapshot{}, ErrCorruptState
	}
	s.CreatedAt = time.Unix(0, int64(createdNano)).UTC()
	s.TakenAt = time.Unix(0, int64(takenNano)).UTC()
	if s.Total, err = d.u64(); err != nil {
		return Snapshot{}, ErrCorruptState
	}
	if s.Underflow, err = d.u64(); err != nil {
		return Snapshot{}, ErrCorruptState
	}
	if s.Overflow, err = d.u64(); err != nil {
		return Snapshot{}, ErrCorruptState
	}
	if s.Errors, err = d.u64(); err != nil {
		return Snapshot{}, ErrCorruptState
	}
	if s.Sum, err = d.f64(); err != nil {
		return Snapshot{}, ErrCorruptState
	}
	if s.UnderflowMin, err = d.f64(); err != nil {
		return Snapshot{}, ErrCorruptState
	}
	if s.OverflowMax, err = d.f64(); err != nil {
		return Snapshot{}, ErrCorruptState
	}
	bcount, err := d.u32()
	if err != nil {
		return Snapshot{}, ErrCorruptState
	}
	if int(bcount) != cfg.BucketCount() {
		return Snapshot{}, ErrCorruptState
	}
	s.Counts = make([]uint64, bcount)
	for i := range s.Counts {
		if s.Counts[i], err = d.u64(); err != nil {
			return Snapshot{}, ErrCorruptState
		}
	}
	if d.pos != len(body) {
		return Snapshot{}, ErrCorruptState
	}
	if err := s.validate(); err != nil {
		return Snapshot{}, err
	}
	return s, nil
}

// Save atomically persists s to path (temp file in the same directory
// followed by an atomic rename), so a crash mid-write cannot leave a
// half-written state that later loads as valid.
func Save(path string, s Snapshot) error {
	dir := filepath.Dir(path)
	tmp, err := os.CreateTemp(dir, ".slogate-state-*")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	cleanup := func() { _ = os.Remove(tmpName) }
	if err := Encode(tmp, s); err != nil {
		_ = tmp.Close()
		cleanup()
		return err
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		cleanup()
		return err
	}
	if err := tmp.Close(); err != nil {
		cleanup()
		return err
	}
	if err := os.Rename(tmpName, path); err != nil {
		cleanup()
		return err
	}
	return nil
}

// Load restores a snapshot previously written with Save.
func Load(path string, cfg HistogramConfig) (Snapshot, error) {
	f, err := os.Open(path)
	if err != nil {
		return Snapshot{}, err
	}
	defer f.Close()
	return Decode(f, cfg)
}
