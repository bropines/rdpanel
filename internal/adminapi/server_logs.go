package adminapi

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/go-chi/chi/v5"

	"github.com/bropines/rdpanel/internal/httpx"
	"github.com/bropines/rdpanel/internal/logs"
)

// Server -> Logs (admin only): recent hbbs/hbbr/panel log lines from the
// in-memory buffer (internal/logs), a live SSE stream and a text download.
// Every line is redacted before it is stored.

// Log capture modes reported to the UI.
const (
	LogCaptureFiles = "files" // s6 image: hbbs, hbbr and panel from s6-log dirs
	LogCapturePanel = "panel" // plain `rdpanel serve`: the panel's own lines only
	LogCaptureOff   = "off"   // RDPANEL_LOGS_BUFFER=0
)

const (
	logsDefaultLimit = 500
	logsMaxLimit     = 2000
	logsMaxQ         = 200
)

// logStreamKeepalive is the SSE keepalive/re-auth period (tests shorten it).
var logStreamKeepalive = 15 * time.Second

func (c *b2bCore) registerLogs(r chi.Router) {
	r.Get("/server/logs", c.handleLogs)
	r.Get("/server/logs/download", c.handleLogsDownload)
	r.Get("/server/logs/stream", c.handleLogsStream)
}

type logEntryDTO struct {
	Seq     uint64     `json:"seq"`
	Time    *time.Time `json:"time"`
	Source  string     `json:"source"`
	Level   string     `json:"level"`
	Message string     `json:"message"`
	Raw     string     `json:"raw"`
}

type logSourceDTO struct {
	Name  string `json:"name"`
	Lines int    `json:"lines"`
}

type logPageDTO struct {
	Enabled    bool           `json:"enabled"`
	Capture    string         `json:"capture"`
	Capacity   int            `json:"capacity"`
	Sources    []logSourceDTO `json:"sources"`
	Items      []logEntryDTO  `json:"items"`
	NextBefore *uint64        `json:"next_before"`
}

func logEntry(e logs.Entry) logEntryDTO {
	return logEntryDTO{Seq: e.Seq, Time: timePtr(e.Time), Source: e.Source, Level: e.Level.String(), Message: e.Message, Raw: e.Raw}
}

func (c *b2bCore) logCapture() string {
	switch {
	case !c.d.Logs.Enabled():
		return LogCaptureOff
	case c.d.LogCapture != "":
		return c.d.LogCapture
	}
	return LogCapturePanel
}

// parseLogFilter reads source, level and q (plus before/after/limit when
// paged is set). It writes a 400 and returns false on bad input.
func parseLogFilter(w http.ResponseWriter, r *http.Request, paged bool) (logs.Filter, bool) {
	q := r.URL.Query()
	var f logs.Filter
	if s := strings.TrimSpace(q.Get("source")); s != "" {
		for _, p := range strings.Split(s, ",") {
			switch p = strings.TrimSpace(p); p {
			case logs.SourceHBBS, logs.SourceHBBR, logs.SourcePanel, logs.SourceOther:
				f.Sources = append(f.Sources, p)
			case "":
			default:
				badRequest(w, "source must be a comma-separated list of hbbs, hbbr, panel, other")
				return f, false
			}
		}
	}
	if s := q.Get("level"); s != "" {
		switch s {
		case "debug", "info", "warn", "error":
			f.MinLevel, _ = logs.ParseLevel(s)
		default:
			badRequest(w, "level must be debug, info, warn or error")
			return f, false
		}
	}
	f.Q = q.Get("q")
	if utf8.RuneCountInString(f.Q) > logsMaxQ {
		badRequest(w, fmt.Sprintf("q must be at most %d characters", logsMaxQ))
		return f, false
	}
	seq := func(name string) (uint64, bool) {
		v := q.Get(name)
		if v == "" {
			return 0, true
		}
		n, err := strconv.ParseUint(v, 10, 64)
		if err != nil || n == 0 {
			badRequest(w, name+" must be a positive integer (an entry seq)")
			return 0, false
		}
		return n, true
	}
	var ok bool
	if f.After, ok = seq("after"); !ok {
		return f, false
	}
	if !paged {
		return f, true
	}
	if f.Before, ok = seq("before"); !ok {
		return f, false
	}
	f.Limit = logsDefaultLimit
	if v := q.Get("limit"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 1 || n > logsMaxLimit {
			badRequest(w, fmt.Sprintf("limit must be 1-%d", logsMaxLimit))
			return f, false
		}
		f.Limit = n
	}
	return f, true
}

func (c *b2bCore) handleLogs(w http.ResponseWriter, r *http.Request) {
	f, ok := parseLogFilter(w, r, true)
	if !ok {
		return
	}
	out := logPageDTO{Enabled: c.d.Logs.Enabled(), Capture: c.logCapture(), Capacity: c.d.Logs.Capacity(),
		Sources: []logSourceDTO{}, Items: []logEntryDTO{}}
	for _, s := range c.d.Logs.Sources(c.d.LogSources...) {
		out.Sources = append(out.Sources, logSourceDTO{Name: s.Name, Lines: s.Lines})
	}
	page := c.d.Logs.Query(f)
	for _, e := range page.Items {
		out.Items = append(out.Items, logEntry(e))
	}
	if page.More && len(page.Items) > 0 {
		nb := page.Items[0].Seq
		out.NextBefore = &nb
	}
	httpx.WriteJSON(w, http.StatusOK, out)
}

func (c *b2bCore) handleLogsDownload(w http.ResponseWriter, r *http.Request) {
	f, ok := parseLogFilter(w, r, false)
	if !ok {
		return
	}
	if !c.d.Logs.Enabled() {
		httpx.WriteError(w, http.StatusServiceUnavailable, "logs_disabled", "the log buffer is disabled (RDPANEL_LOGS_BUFFER=0)")
		return
	}
	page := c.d.Logs.Query(f)
	name := "rdpanel-logs-" + c.d.Store.Now().UTC().Format("20060102-150405") + ".txt"
	h := w.Header()
	h.Set("Content-Type", "text/plain; charset=utf-8")
	h.Set("Content-Disposition", `attachment; filename="`+name+`"`)
	h.Set("Cache-Control", "no-store")
	w.WriteHeader(http.StatusOK)
	var sb strings.Builder
	for _, e := range page.Items {
		sb.Reset()
		sb.WriteString("[")
		sb.WriteString(e.Source)
		sb.WriteString("] ")
		sb.WriteString(e.Raw)
		sb.WriteString("\n")
		if _, err := w.Write([]byte(sb.String())); err != nil {
			return
		}
	}
}

// stillAdmin re-validates the stream's session (revoked sessions, demoted or
// disabled users end the stream).
func (c *b2bCore) stillAdmin(r *http.Request) bool {
	ok := false
	// Drop the session loaded when the stream opened, then load it afresh.
	fresh := r.WithContext(WithSession(r.Context(), nil))
	c.sessLoad(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		s := CurrentSession(r.Context())
		ok = s != nil && s.User != nil && !s.MFAPending && !s.MustEnrollTOTP && s.User.IsAdmin()
	})).ServeHTTP(discardWriter{}, fresh)
	return ok
}

type discardWriter struct{}

func (discardWriter) Header() http.Header         { return http.Header{} }
func (discardWriter) Write(b []byte) (int, error) { return len(b), nil }
func (discardWriter) WriteHeader(int)             {}

// handleLogsStream is the live tail: text/event-stream with
//
//	event: log      data: <LogEntry>   (id: <seq>)
//	event: dropped  data: {"dropped":N} (lines skipped for this slow client)
//
// With after=<seq> the stream first replays buffered matching entries newer
// than seq, so a client that just loaded a page misses nothing.
func (c *b2bCore) handleLogsStream(w http.ResponseWriter, r *http.Request) {
	f, ok := parseLogFilter(w, r, false)
	if !ok {
		return
	}
	if !c.d.Logs.Enabled() {
		httpx.WriteError(w, http.StatusServiceUnavailable, "logs_disabled", "the log buffer is disabled (RDPANEL_LOGS_BUFFER=0)")
		return
	}
	sub := c.d.Logs.Subscribe(f, 0)
	defer sub.Close()

	rc := http.NewResponseController(w)
	h := w.Header()
	h.Set("Content-Type", "text/event-stream; charset=utf-8")
	h.Set("Cache-Control", "no-store")
	h.Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)
	if _, err := fmt.Fprint(w, "retry: 5000\n\n"); err != nil {
		return
	}
	var last uint64
	send := func(e logs.Entry) error {
		if e.Seq <= last {
			return nil // already replayed
		}
		last = e.Seq
		b, err := json.Marshal(logEntry(e))
		if err != nil {
			return err
		}
		_, err = fmt.Fprintf(w, "id: %d\nevent: log\ndata: %s\n\n", e.Seq, b)
		return err
	}
	if f.After > 0 {
		replay := f
		replay.Limit = logsMaxLimit
		for _, e := range c.d.Logs.Query(replay).Items {
			if send(e) != nil {
				return
			}
		}
	}
	if rc.Flush() != nil {
		return
	}

	ctx := r.Context()
	ka := time.NewTicker(logStreamKeepalive)
	defer ka.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ka.C:
			if !c.stillAdmin(r) {
				_, _ = fmt.Fprint(w, "event: unauthenticated\ndata: {}\n\n")
				_ = rc.Flush()
				return
			}
			if _, err := fmt.Fprint(w, ": keepalive\n\n"); err != nil {
				return
			}
		case e := <-sub.C():
			if send(e) != nil {
				return
			}
		drain:
			for i := 0; i < 256; i++ {
				select {
				case e := <-sub.C():
					if send(e) != nil {
						return
					}
				default:
					break drain
				}
			}
		}
		if d := sub.TakeDropped(); d > 0 {
			if _, err := fmt.Fprintf(w, "event: dropped\ndata: {\"dropped\":%d}\n\n", d); err != nil {
				return
			}
		}
		if rc.Flush() != nil {
			return
		}
	}
}
