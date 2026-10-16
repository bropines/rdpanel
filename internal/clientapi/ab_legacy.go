package clientapi

import (
	"encoding/json"
	"net/http"

	"github.com/bropines/rdpanel/internal/store"
)

// legacyBlob is the legacy address book (AbBlob). "data" in the HTTP body and
// "tag_colors" inside it are JSON-encoded strings (double encoding).
type legacyBlob struct {
	Tags      []string `json:"tags"`
	Peers     []abPeer `json:"peers"`
	TagColors string   `json:"tag_colors"`
}

// legacyGet implements GET /api/ab (Flutter) and POST /api/ab/get (Sciter):
// {"data": "<JSON string of AbBlob>"} for the caller's personal book.
func (h *handler) legacyGet(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	b, err := h.st.AddressBooks.EnsurePersonal(ctx, sessionFrom(ctx).user.ID)
	if err != nil {
		h.abFail(ctx, w, err)
		return
	}
	entries, _, err := h.st.AddressBooks.ListEntries(ctx, b.ID, 0, 0)
	if err != nil {
		h.abFail(ctx, w, err)
		return
	}
	tags, err := h.st.AddressBooks.ListTags(ctx, b.ID)
	if err != nil {
		h.abFail(ctx, w, err)
		return
	}
	blob := legacyBlob{Tags: make([]string, 0, len(tags)), Peers: make([]abPeer, 0, len(entries))}
	colors := make(map[string]uint32, len(tags))
	for _, t := range tags {
		blob.Tags = append(blob.Tags, t.Name)
		colors[t.Name] = t.Color
	}
	for _, e := range entries {
		blob.Peers = append(blob.Peers, newABPeer(e))
	}
	tc, err := marshalNoEscape(colors)
	if err != nil {
		h.abFail(ctx, w, err)
		return
	}
	blob.TagColors = tc
	data, err := marshalNoEscape(blob)
	if err != nil {
		h.abFail(ctx, w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"data": data})
}

func marshalNoEscape(v any) (string, error) {
	rec := &bufWriter{}
	enc := json.NewEncoder(rec)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(v); err != nil {
		return "", err
	}
	b := rec.b
	if n := len(b); n > 0 && b[n-1] == '\n' {
		b = b[:n-1]
	}
	return string(b), nil
}

type bufWriter struct{ b []byte }

func (w *bufWriter) Write(p []byte) (int, error) { w.b = append(w.b, p...); return len(p), nil }

// legacyPush implements POST /api/ab {"data": "<JSON string of AbBlob>"}: a
// full replace of the caller's personal book. Keys missing from a peer keep
// their stored values (e.g. a note set through the new API).
func (h *handler) legacyPush(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	var body struct {
		Data *string `json:"data"`
	}
	if err := decodeBody(w, r, &body, maxLegacyBody); err != nil || body.Data == nil {
		writeError(w, http.StatusBadRequest, "Invalid request")
		return
	}
	var blob struct {
		Tags      []string          `json:"tags"`
		Peers     []json.RawMessage `json:"peers"`
		TagColors json.RawMessage   `json:"tag_colors"`
	}
	if *body.Data != "" {
		if err := json.Unmarshal([]byte(*body.Data), &blob); err != nil {
			writeError(w, http.StatusBadRequest, "Invalid address book data")
			return
		}
	}
	colors := map[string]uint32{}
	if len(blob.TagColors) > 0 && string(blob.TagColors) != "null" {
		raw := []byte(blob.TagColors)
		var s string
		if json.Unmarshal(raw, &s) == nil {
			raw = []byte(s)
		}
		var m map[string]json.RawMessage
		if len(raw) > 0 {
			if err := json.Unmarshal(raw, &m); err != nil {
				writeError(w, http.StatusBadRequest, "Invalid tag colors")
				return
			}
		}
		for k, v := range m {
			if c, ok := parseColor(v); ok {
				colors[k] = c
			}
		}
	}
	tags := make([]string, 0, len(blob.Tags))
	for _, t := range blob.Tags {
		if validTagName(t) {
			tags = append(tags, t)
		}
	}
	entries := make([]store.LegacyEntry, 0, len(blob.Peers))
	for _, raw := range blob.Peers {
		id, patch, err := parsePeer(raw)
		if err != nil {
			writeError(w, http.StatusBadRequest, "Invalid peer")
			return
		}
		entries = append(entries, store.LegacyEntry{RustdeskID: id, Patch: patch})
	}
	b, err := h.st.AddressBooks.EnsurePersonal(ctx, sessionFrom(ctx).user.ID)
	if err != nil {
		h.abFail(ctx, w, err)
		return
	}
	if err := h.st.AddressBooks.ReplaceAll(ctx, b.ID, entries, tags, colors); err != nil {
		h.abFail(ctx, w, err)
		return
	}
	writeEmpty(w)
}
