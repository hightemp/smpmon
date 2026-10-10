//go:build linux

// Package linux implements collectors backed by /proc and /sys.
package linux

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/hightemp/smpmon/internal/collect"
)

// FS points collectors at the proc and sys filesystems. Tests use fixture
// directories; production uses DefaultFS.
type FS struct {
	Proc string
	Sys  string
}

// DefaultFS is the real system.
var DefaultFS = FS{Proc: "/proc", Sys: "/sys"}

func (f FS) proc(elem ...string) string { return filepath.Join(append([]string{f.Proc}, elem...)...) }
func (f FS) sys(elem ...string) string  { return filepath.Join(append([]string{f.Sys}, elem...)...) }

// Collectors returns all Linux collectors using DefaultFS.
func Collectors() []collect.Collector {
	return []collect.Collector{
		NewCPU(DefaultFS),
		NewLoad(DefaultFS),
		NewMem(DefaultFS),
		NewDisks(DefaultFS),
		NewNet(DefaultFS),
		NewProcs(DefaultFS),
	}
}

// clockTicks is USER_HZ. It is 100 on every mainstream Linux architecture and
// cannot be queried without cgo.
const clockTicks = 100

// reader reads small files into a reusable buffer to avoid per-read allocations.
type reader struct {
	buf []byte
}

// read returns the contents of path. The returned slice is only valid until
// the next call. Raw syscalls are used instead of os.File: collectors read
// thousands of tiny /proc files per sweep, and os.File adds an allocation,
// a finalizer and extra syscalls to each.
func (r *reader) read(path string) ([]byte, error) {
	fd, err := openRetry(path)
	if err != nil {
		return nil, &os.PathError{Op: "open", Path: path, Err: err}
	}
	defer func() { _ = syscall.Close(fd) }()
	if r.buf == nil {
		r.buf = make([]byte, 0, 4096)
	}
	r.buf = r.buf[:0]
	for {
		if len(r.buf) == cap(r.buf) {
			r.buf = append(r.buf, 0)[:len(r.buf)]
		}
		n, err := syscall.Read(fd, r.buf[len(r.buf):cap(r.buf)])
		if err == syscall.EINTR {
			continue
		}
		if err != nil {
			return nil, &os.PathError{Op: "read", Path: path, Err: err}
		}
		if n == 0 {
			return r.buf, nil
		}
		r.buf = r.buf[:len(r.buf)+n]
	}
}

func openRetry(path string) (int, error) {
	for {
		fd, err := syscall.Open(path, syscall.O_RDONLY|syscall.O_CLOEXEC, 0)
		if err != syscall.EINTR {
			return fd, err
		}
	}
}

// readString reads a whole file and trims surrounding whitespace.
func readString(path string) (string, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(b)), nil
}

// readUint reads a file containing a single unsigned integer.
func readUint(path string) (uint64, error) {
	s, err := readString(path)
	if err != nil {
		return 0, err
	}
	return strconv.ParseUint(s, 10, 64)
}

// lines calls f for every line of b.
func lines(b []byte, f func(line []byte)) {
	for len(b) > 0 {
		i := bytes.IndexByte(b, '\n')
		if i < 0 {
			f(b)
			return
		}
		f(b[:i])
		b = b[i+1:]
	}
}

// fields splits b on spaces into dst without allocating and returns the
// number of fields found (at most len(dst)).
func fields(b []byte, dst [][]byte) int {
	n := 0
	for i := 0; i < len(b) && n < len(dst); {
		for i < len(b) && (b[i] == ' ' || b[i] == '\n' || b[i] == '\t') {
			i++
		}
		start := i
		for i < len(b) && b[i] != ' ' && b[i] != '\n' && b[i] != '\t' {
			i++
		}
		if i > start {
			dst[n] = b[start:i]
			n++
		}
	}
	return n
}

// listPIDs appends the numeric entries of dir (a /proc mount) to dst using
// getdents64 directly, avoiding a string allocation per entry.
func listPIDs(dir string, buf []byte, dst []int32) ([]int32, []byte, error) {
	fd, err := openRetry(dir)
	if err != nil {
		return dst, buf, &os.PathError{Op: "open", Path: dir, Err: err}
	}
	defer func() { _ = syscall.Close(fd) }()
	if len(buf) < 32*1024 {
		buf = make([]byte, 32*1024)
	}
	for {
		n, err := syscall.Getdents(fd, buf)
		if err == syscall.EINTR {
			continue
		}
		if err != nil {
			return dst, buf, &os.PathError{Op: "getdents", Path: dir, Err: err}
		}
		if n <= 0 {
			return dst, buf, nil
		}
		// struct linux_dirent64 { u64 ino; s64 off; u16 reclen; u8 type; char name[]; }
		for off := 0; off+19 < n; {
			reclen := int(buf[off+16]) | int(buf[off+17])<<8
			if reclen == 0 {
				break
			}
			name := buf[off+19 : off+reclen]
			if pid, ok := parsePID(name); ok {
				dst = append(dst, pid)
			}
			off += reclen
		}
	}
}

// parsePID parses a NUL-terminated decimal directory name.
func parsePID(name []byte) (int32, bool) {
	var v int64
	digits := 0
	for _, c := range name {
		if c == 0 {
			break
		}
		if c < '0' || c > '9' {
			return 0, false
		}
		v = v*10 + int64(c-'0')
		digits++
		if v > 1<<31-1 {
			return 0, false
		}
	}
	return int32(v), digits > 0
}

// parseUint parses an unsigned decimal integer, returning 0 on error.
func parseUint(b []byte) uint64 {
	var n uint64
	for _, c := range b {
		if c < '0' || c > '9' {
			return n
		}
		n = n*10 + uint64(c-'0')
	}
	return n
}

// parseInt parses a signed decimal integer, returning 0 on error.
func parseInt(b []byte) int64 {
	if len(b) > 0 && b[0] == '-' {
		return -int64(parseUint(b[1:]))
	}
	return int64(parseUint(b))
}

// delta returns cur-prev, treating counter resets as zero.
func delta(cur, prev uint64) uint64 {
	if cur < prev {
		return 0
	}
	return cur - prev
}

// rate turns a counter delta into a per-second rate.
func rate(cur, prev uint64, elapsed time.Duration) float64 {
	if elapsed <= 0 {
		return 0
	}
	return float64(delta(cur, prev)) / elapsed.Seconds()
}

// bootTime reads btime from /proc/stat.
func bootTime(fs FS) (time.Time, error) {
	b, err := os.ReadFile(fs.proc("stat"))
	if err != nil {
		return time.Time{}, err
	}
	var bt time.Time
	lines(b, func(l []byte) {
		if v, ok := bytes.CutPrefix(l, []byte("btime ")); ok {
			bt = time.Unix(int64(parseUint(bytes.TrimSpace(v))), 0)
		}
	})
	if bt.IsZero() {
		return bt, errors.New("btime not found in /proc/stat")
	}
	return bt, nil
}
