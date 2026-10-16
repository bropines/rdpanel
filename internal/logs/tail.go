package logs

import (
	"bytes"
	"context"
	"errors"
	"io"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// Tailer follows an s6-log directory (<Dir>/current plus rotated @*.s / @*.u
// archives) and feeds its lines into a Buffer.
//
// On start it replays the newest archive and current (the Buffer keeps only
// the last lines), so the page survives rdpanel restarts; then it polls
// current for growth and follows s6-log's rotations (current renamed, a new
// one created) without losing the tail of the old file.
type Tailer struct {
	Dir    string
	Source string
	Buf    *Buffer
	// Interval is the poll period (default 300 ms).
	Interval time.Duration
	Log      *slog.Logger
}

const (
	defaultTailInterval = 300 * time.Millisecond
	// maxReplayBytes bounds what is read from the archive on start.
	maxReplayBytes = 4 << 20
)

// Run follows the directory until ctx ends.
func (t *Tailer) Run(ctx context.Context) {
	if !t.Buf.Enabled() {
		return
	}
	iv := t.Interval
	if iv <= 0 {
		iv = defaultTailInterval
	}
	log := t.Log
	if log == nil {
		log = slog.New(slog.NewTextHandler(io.Discard, nil))
	}
	f := &follower{t: t}
	defer f.close()
	f.replayArchive()
	tick := time.NewTicker(iv)
	defer tick.Stop()
	warned := false
	for {
		if err := f.poll(); err != nil {
			if !warned && !errors.Is(err, fs.ErrNotExist) {
				log.Warn("logs: tail", slog.String("source", t.Source), slog.Any("err", err))
				warned = true
			}
		} else {
			warned = false
		}
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
		}
	}
}

type follower struct {
	t       *Tailer
	f       *os.File
	info    os.FileInfo
	off     int64
	partial []byte
}

func (f *follower) current() string { return filepath.Join(f.t.Dir, "current") }

func (f *follower) close() {
	if f.f != nil {
		_ = f.f.Close()
		f.f = nil
	}
}

// replayArchive feeds the newest rotated file (the lines just before
// current), at most maxReplayBytes of its tail.
func (f *follower) replayArchive() {
	ents, err := os.ReadDir(f.t.Dir)
	if err != nil {
		return
	}
	var names []string
	for _, e := range ents {
		n := e.Name()
		if e.Type().IsRegular() && strings.HasPrefix(n, "@") && (strings.HasSuffix(n, ".s") || strings.HasSuffix(n, ".u")) {
			names = append(names, n)
		}
	}
	if len(names) == 0 {
		return
	}
	sort.Strings(names) // TAI64N names sort chronologically
	fh, err := os.Open(filepath.Join(f.t.Dir, names[len(names)-1]))
	if err != nil {
		return
	}
	defer fh.Close()
	if st, err := fh.Stat(); err == nil && st.Size() > maxReplayBytes {
		_, _ = fh.Seek(st.Size()-maxReplayBytes, io.SeekStart)
	}
	data, err := io.ReadAll(fh)
	if err != nil {
		return
	}
	if st, _ := fh.Stat(); st != nil && st.Size() > maxReplayBytes {
		if i := bytes.IndexByte(data, '\n'); i >= 0 {
			data = data[i+1:] // drop the cut first line
		}
	}
	f.feed(data, true)
}

// poll reads whatever current gained since the last call.
func (f *follower) poll() error {
	st, err := os.Stat(f.current())
	if err != nil {
		if f.f != nil {
			// Rotated away and not yet recreated: finish the old file.
			if err := f.drain(); err != nil {
				return err
			}
		}
		return err
	}
	if f.f != nil && !os.SameFile(f.info, st) {
		// s6-log rotated: read the rest of the old file, then switch.
		if err := f.drain(); err != nil {
			return err
		}
		f.flushPartial()
		f.close()
	}
	if f.f == nil {
		fh, err := os.Open(f.current())
		if err != nil {
			return err
		}
		f.f, f.info, f.off, f.partial = fh, st, 0, f.partial[:0]
	}
	if st.Size() < f.off {
		// Truncated in place (not s6-log's way, but be safe).
		f.off, f.partial = 0, f.partial[:0]
		if _, err := f.f.Seek(0, io.SeekStart); err != nil {
			return err
		}
	}
	return f.drain()
}

// drain reads the open file to EOF.
func (f *follower) drain() error {
	if f.f == nil {
		return nil
	}
	if _, err := f.f.Seek(f.off, io.SeekStart); err != nil {
		return err
	}
	buf := make([]byte, 64<<10)
	for {
		n, err := f.f.Read(buf)
		if n > 0 {
			f.off += int64(n)
			f.feed(buf[:n], false)
		}
		if err == io.EOF || n == 0 {
			return nil
		}
		if err != nil {
			return err
		}
	}
}

// feed splits data into lines; an unterminated tail is kept for the next
// read (complete=true flushes it: archives always end with a newline).
func (f *follower) feed(data []byte, complete bool) {
	for len(data) > 0 {
		i := bytes.IndexByte(data, '\n')
		if i < 0 {
			f.partial = append(f.partial, data...)
			if len(f.partial) > MaxLineBytes {
				f.flushPartial()
			}
			break
		}
		if len(f.partial) > 0 {
			f.partial = append(f.partial, data[:i]...)
			f.flushPartial()
		} else {
			f.t.Buf.Write(f.t.Source, string(data[:i]))
		}
		data = data[i+1:]
	}
	if complete {
		f.flushPartial()
	}
}

func (f *follower) flushPartial() {
	if len(f.partial) > 0 {
		f.t.Buf.Write(f.t.Source, string(f.partial))
		f.partial = f.partial[:0]
	}
}
