package logs

import (
	"bytes"
	"io"
	"sync"
)

// Writer returns an io.Writer that splits what is written into lines and
// stores them under source. Tee the process logger through it
// (io.MultiWriter(os.Stderr, buf.Writer("panel"))) to capture the panel's own
// slog output. It never fails and never blocks on consumers. A disabled
// buffer yields io.Discard.
func (b *Buffer) Writer(source string) io.Writer {
	if !b.Enabled() {
		return io.Discard
	}
	return &lineWriter{b: b, source: source}
}

type lineWriter struct {
	b      *Buffer
	source string
	mu     sync.Mutex
	rest   []byte
}

func (w *lineWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	n := len(p)
	for len(p) > 0 {
		i := bytes.IndexByte(p, '\n')
		if i < 0 {
			w.rest = append(w.rest, p...)
			if len(w.rest) > MaxLineBytes {
				w.b.Write(w.source, string(w.rest))
				w.rest = w.rest[:0]
			}
			break
		}
		if len(w.rest) > 0 {
			w.rest = append(w.rest, p[:i]...)
			w.b.Write(w.source, string(w.rest))
			w.rest = w.rest[:0]
		} else {
			w.b.Write(w.source, string(p[:i]))
		}
		p = p[i+1:]
	}
	return n, nil
}
