// Package history records metrics to disk and reads them back.
//
// Layout: <dir>/<tier>/<period>.smh, where tier is raw, 10s, 1m or tables
// and each file covers one hour (raw, tables) or one day (10s, 1m), named
// by its UTC start so names sort by time. Retention deletes whole files.
//
// A file is a magic header followed by frames: uvarint length, then a
// deflate-compressed block. A metrics block is columnar: the keys, the
// record times as deltas, then one column per key with each float stored
// as the XOR of its bits with the previous value, which turns slowly
// changing series into runs of zero bytes that deflate well. Missing
// values are NaN. A table block is JSON. A crash can at worst truncate the
// last frame, which readers ignore.
package history

import (
	"bufio"
	"bytes"
	"compress/flate"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"math"
)

const magic = "SMH1"

// block is a set of records for a fixed list of keys.
type block struct {
	keys  []string
	times []int64     // unix ms
	vals  [][]float64 // vals[key][record]
}

func (b *block) encode() ([]byte, error) {
	var raw bytes.Buffer
	var tmp [binary.MaxVarintLen64]byte
	putU := func(v uint64) { raw.Write(tmp[:binary.PutUvarint(tmp[:], v)]) }
	putI := func(v int64) { raw.Write(tmp[:binary.PutVarint(tmp[:], v)]) }

	putU(uint64(len(b.keys)))
	for _, k := range b.keys {
		putU(uint64(len(k)))
		raw.WriteString(k)
	}
	putU(uint64(len(b.times)))
	prev := int64(0)
	for _, t := range b.times {
		putI(t - prev)
		prev = t
	}
	var buf [8]byte
	for _, col := range b.vals {
		var last uint64
		for _, v := range col {
			bits := math.Float64bits(v)
			binary.LittleEndian.PutUint64(buf[:], bits^last)
			raw.Write(buf[:])
			last = bits
		}
	}
	var out bytes.Buffer
	w, err := flate.NewWriter(&out, flate.DefaultCompression)
	if err != nil {
		return nil, err
	}
	if _, err := w.Write(raw.Bytes()); err != nil {
		return nil, err
	}
	if err := w.Close(); err != nil {
		return nil, err
	}
	return out.Bytes(), nil
}

func decodeBlock(data []byte) (*block, error) {
	raw, err := io.ReadAll(flate.NewReader(bytes.NewReader(data)))
	if err != nil {
		return nil, err
	}
	r := bytes.NewReader(raw)
	nk, err := binary.ReadUvarint(r)
	if err != nil || nk > 1<<20 {
		return nil, errors.New("corrupt block: keys")
	}
	b := &block{keys: make([]string, nk)}
	for i := range b.keys {
		n, err := binary.ReadUvarint(r)
		if err != nil || n > 4096 {
			return nil, errors.New("corrupt block: key")
		}
		k := make([]byte, n)
		if _, err := io.ReadFull(r, k); err != nil {
			return nil, err
		}
		b.keys[i] = string(k)
	}
	nr, err := binary.ReadUvarint(r)
	if err != nil || nr > 1<<20 {
		return nil, errors.New("corrupt block: records")
	}
	b.times = make([]int64, nr)
	prev := int64(0)
	for i := range b.times {
		d, err := binary.ReadVarint(r)
		if err != nil {
			return nil, err
		}
		prev += d
		b.times[i] = prev
	}
	b.vals = make([][]float64, nk)
	var buf [8]byte
	for k := range b.vals {
		col := make([]float64, nr)
		var last uint64
		for i := range col {
			if _, err := io.ReadFull(r, buf[:]); err != nil {
				return nil, err
			}
			last ^= binary.LittleEndian.Uint64(buf[:])
			col[i] = math.Float64frombits(last)
		}
		b.vals[k] = col
	}
	return b, nil
}

// writeFrame appends one frame.
func writeFrame(w io.Writer, payload []byte) error {
	var hdr [binary.MaxVarintLen64]byte
	n := binary.PutUvarint(hdr[:], uint64(len(payload)))
	if _, err := w.Write(hdr[:n]); err != nil {
		return err
	}
	_, err := w.Write(payload)
	return err
}

// readFrames calls f for every complete frame in r after the header. A
// truncated tail (from a crash) ends the iteration without an error.
func readFrames(r io.Reader, f func([]byte) error) error {
	br := bufio.NewReader(r)
	head := make([]byte, len(magic))
	if _, err := io.ReadFull(br, head); err != nil {
		if errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) {
			return nil
		}
		return err
	}
	if string(head) != magic {
		return fmt.Errorf("not a history file")
	}
	for {
		n, err := binary.ReadUvarint(br)
		if err != nil {
			return nil // EOF or truncated length
		}
		if n > 64<<20 {
			return fmt.Errorf("corrupt frame length %d", n)
		}
		buf := make([]byte, n)
		if _, err := io.ReadFull(br, buf); err != nil {
			return nil // truncated tail
		}
		if err := f(buf); err != nil {
			return err
		}
	}
}
