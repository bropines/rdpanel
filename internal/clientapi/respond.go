package clientapi

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strconv"
	"strings"
	"unicode/utf8"
)

// Request body limits.
const (
	maxBody       = 1 << 20 // 1 MiB: every JSON request except the legacy blob
	maxLegacyBody = 8 << 20 // POST /api/ab carries the whole address book
	maxField      = 4096    // bytes per free-text field
	maxIDLen      = 100     // devices.rustdesk_id / address_book_entries.rustdesk_id
)

// writeJSON writes v as compact JSON without HTML escaping (values such as
// passwords are returned byte-for-byte) and without a trailing newline.
func writeJSON(w http.ResponseWriter, status int, v any) {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(v); err != nil {
		status = http.StatusInternalServerError
		buf.Reset()
		buf.WriteString(`{"error":"Internal error"}`)
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_, _ = w.Write(bytes.TrimSuffix(buf.Bytes(), []byte("\n")))
}

// writeError writes the client error shape {"error":"<msg>"}.
func writeError(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, map[string]string{"error": msg})
}

// writeEmpty writes HTTP 200 with an empty body: the only success response
// the client accepts for address-book actions and audit uploads.
func writeEmpty(w http.ResponseWriter) {
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Content-Length", "0")
	w.WriteHeader(http.StatusOK)
}

// writeText writes a plain-text body (sysinfo answers are compared byte-for-byte).
func writeText(w http.ResponseWriter, status int, s string) {
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_, _ = io.WriteString(w, s)
}

func internalError(w http.ResponseWriter) {
	writeError(w, http.StatusInternalServerError, "Internal error")
}

var errEmptyBody = errors.New("empty body")

// readBody reads at most limit bytes of the request body. Bodies may be empty
// (Content-Length: 0 POSTs) and DELETE requests carry JSON bodies.
func readBody(w http.ResponseWriter, r *http.Request, limit int64) ([]byte, error) {
	if r.Body == nil {
		return nil, nil
	}
	b, err := io.ReadAll(http.MaxBytesReader(w, r.Body, limit))
	if err != nil {
		return nil, err
	}
	return bytes.TrimSpace(b), nil
}

// decodeBody unmarshals a JSON body into v. An empty body (or "null") leaves v
// unchanged and returns errEmptyBody so callers can decide whether it is fine.
func decodeBody(w http.ResponseWriter, r *http.Request, v any, limit int64) error {
	b, err := readBody(w, r, limit)
	if err != nil {
		return err
	}
	if len(b) == 0 || string(b) == "null" {
		return errEmptyBody
	}
	return json.Unmarshal(b, v)
}

// page holds the client's 1-based "current" and "pageSize" query parameters.
type page struct {
	limit, offset int
}

func parsePage(r *http.Request) page {
	q := r.URL.Query()
	current, err := strconv.Atoi(q.Get("current"))
	if err != nil || current < 1 {
		current = 1
	}
	size, err := strconv.Atoi(q.Get("pageSize"))
	if err != nil || size < 1 {
		size = 100
	}
	size = min(size, 1000)
	if current > 1_000_000 {
		current = 1_000_000
	}
	return page{limit: size, offset: (current - 1) * size}
}

// listResponse is the paginated envelope {"total": n, "data": [...]}.
type listResponse[T any] struct {
	Total int `json:"total"`
	Data  []T `json:"data"`
}

func newList[T any](total int, data []T) listResponse[T] {
	if data == nil {
		data = []T{}
	}
	return listResponse[T]{Total: total, Data: data}
}

// validID reports whether s is a plausible RustDesk ID: 1..100 bytes, no
// whitespace or control characters.
func validID(s string) bool {
	if s == "" || len(s) > maxIDLen || !utf8.ValidString(s) {
		return false
	}
	return !strings.ContainsFunc(s, func(r rune) bool { return r <= ' ' || r == 0x7f })
}

// clip truncates s to n bytes on a rune boundary.
func clip(s string, n int) string {
	if len(s) <= n {
		return s
	}
	s = s[:n]
	for len(s) > 0 && !utf8.ValidString(s) {
		s = s[:len(s)-1]
	}
	return s
}
