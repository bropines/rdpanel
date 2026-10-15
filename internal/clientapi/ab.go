package clientapi

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strconv"
	"strings"

	"github.com/go-chi/chi/v5"

	"github.com/bropines/rustdesk-panel/internal/store"
)

// abPeer is a peer as the client reads it (peer_model.dart). password/hash are
// returned byte-for-byte; forceAlwaysRelay is the string "true"/"false".
type abPeer struct {
	ID               string   `json:"id"`
	Hash             string   `json:"hash"`
	Password         string   `json:"password"`
	Username         string   `json:"username"`
	Hostname         string   `json:"hostname"`
	Platform         string   `json:"platform"`
	Alias            string   `json:"alias"`
	Tags             []string `json:"tags"`
	Note             string   `json:"note"`
	ForceAlwaysRelay string   `json:"forceAlwaysRelay"`
	RDPPort          string   `json:"rdpPort"`
	RDPUsername      string   `json:"rdpUsername"`
	LoginName        string   `json:"loginName"`
	SameServer       bool     `json:"same_server"`
	DeviceGroupName  string   `json:"device_group_name"`
}

func newABPeer(e *store.ABEntry) abPeer {
	tags := e.Tags
	if tags == nil {
		tags = []string{}
	}
	return abPeer{
		ID: e.RustdeskID, Hash: e.Hash, Password: e.Password, Username: e.OSUsername, Hostname: e.Hostname,
		Platform: e.Platform, Alias: e.Alias, Tags: tags, Note: e.Note,
		ForceAlwaysRelay: strconv.FormatBool(e.ForceAlwaysRelay), RDPPort: e.RDPPort, RDPUsername: e.RDPUsername,
		LoginName: e.LoginName, SameServer: e.SameServer, DeviceGroupName: e.DeviceGroupName,
	}
}

type abTag struct {
	Name  string `json:"name"`
	Color uint32 `json:"color"`
}

type abProfile struct {
	GUID  string         `json:"guid"`
	Name  string         `json:"name"`
	Owner string         `json:"owner"`
	Note  string         `json:"note"`
	Rule  int            `json:"rule"`
	Info  *abProfileInfo `json:"info,omitempty"`
}

type abProfileInfo struct {
	Password string `json:"password"`
}

var errBadPeer = errors.New("invalid peer")

// parsePeer decodes a client peer object into its RustDesk ID and a patch in
// which only the keys present in the JSON are set. Lenient about types the
// client has sent over the years (forceAlwaysRelay as string or bool, rdpPort
// as string or number).
func parsePeer(raw json.RawMessage) (string, store.ABEntryPatch, error) {
	var (
		m map[string]json.RawMessage
		p store.ABEntryPatch
	)
	if err := json.Unmarshal(raw, &m); err != nil || m == nil {
		return "", p, errBadPeer
	}
	id, ok, err := jsonString(m["id"])
	if err != nil || !ok || !validID(id) {
		return "", p, errBadPeer
	}
	strs := []struct {
		key string
		dst **string
	}{
		{"alias", &p.Alias}, {"username", &p.OSUsername}, {"hostname", &p.Hostname}, {"platform", &p.Platform},
		{"loginName", &p.LoginName}, {"note", &p.Note}, {"password", &p.Password}, {"hash", &p.Hash},
		{"rdpPort", &p.RDPPort}, {"rdpUsername", &p.RDPUsername},
	}
	for _, f := range strs {
		v, ok, err := jsonString(m[f.key])
		if err != nil {
			return "", p, fmt.Errorf("%w: %s", errBadPeer, f.key)
		}
		if ok {
			if len(v) > maxField {
				return "", p, fmt.Errorf("%w: %s too long", errBadPeer, f.key)
			}
			*f.dst = &v
		}
	}
	bools := []struct {
		keys []string
		dst  **bool
	}{
		{[]string{"forceAlwaysRelay"}, &p.ForceAlwaysRelay},
		{[]string{"same_server", "sameServer"}, &p.SameServer},
	}
	for _, f := range bools {
		for _, k := range f.keys {
			v, ok, err := jsonBool(m[k])
			if err != nil {
				return "", p, fmt.Errorf("%w: %s", errBadPeer, k)
			}
			if ok {
				*f.dst = &v
				break
			}
		}
	}
	if raw, ok := m["tags"]; ok && string(raw) != "null" {
		var tags []string
		if err := json.Unmarshal(raw, &tags); err != nil || len(tags) > 1000 {
			return "", p, fmt.Errorf("%w: tags", errBadPeer)
		}
		for _, t := range tags {
			if len(t) > 128 {
				return "", p, fmt.Errorf("%w: tag too long", errBadPeer)
			}
		}
		p.Tags = &tags
	}
	return id, p, nil
}

// jsonString reads a JSON string (numbers are taken verbatim); null/absent
// reports ok=false.
func jsonString(raw json.RawMessage) (string, bool, error) {
	if len(raw) == 0 || string(raw) == "null" {
		return "", false, nil
	}
	var s string
	if err := json.Unmarshal(raw, &s); err == nil {
		return s, true, nil
	}
	var n json.Number
	if err := json.Unmarshal(raw, &n); err == nil {
		return n.String(), true, nil
	}
	return "", false, errBadPeer
}

// jsonBool reads true/false or the strings "true"/"false".
func jsonBool(raw json.RawMessage) (bool, bool, error) {
	if len(raw) == 0 || string(raw) == "null" {
		return false, false, nil
	}
	var b bool
	if err := json.Unmarshal(raw, &b); err == nil {
		return b, true, nil
	}
	var s string
	if err := json.Unmarshal(raw, &s); err == nil {
		return strings.EqualFold(s, "true"), true, nil
	}
	return false, false, errBadPeer
}

// parseColor accepts a Flutter ARGB colour (uint32; negative int32 values are
// taken as their two's complement).
func parseColor(raw json.RawMessage) (uint32, bool) {
	var n json.Number
	if err := json.Unmarshal(raw, &n); err != nil {
		return 0, false
	}
	v, err := strconv.ParseInt(n.String(), 10, 64)
	if err != nil || v < -(1<<31) || v > 0xFFFFFFFF {
		return 0, false
	}
	return uint32(v), true
}

func validTagName(s string) bool { return s != "" && len(s) <= 128 }

// book resolves the address book from the {guid} path parameter or the "ab"
// query parameter and checks that the caller has at least need. It writes the
// error response and returns nil when access is refused.
func (h *handler) book(w http.ResponseWriter, r *http.Request, need store.ABPermission) *store.AddressBook {
	guid := chi.URLParam(r, "guid")
	if guid == "" {
		guid = r.URL.Query().Get("ab")
	}
	ctx := r.Context()
	u := sessionFrom(ctx).user
	b, err := h.st.AddressBooks.GetByGUID(ctx, guid)
	if errors.Is(err, store.ErrNotFound) {
		writeError(w, http.StatusNotFound, "Address book not found")
		return nil
	}
	if err != nil {
		h.abFail(ctx, w, err)
		return nil
	}
	perm, err := h.st.AddressBooks.Permission(ctx, b, u.ID)
	if err != nil {
		h.abFail(ctx, w, err)
		return nil
	}
	switch {
	case perm == store.ABNone:
		writeError(w, http.StatusNotFound, "Address book not found")
		return nil
	case perm < need:
		writeError(w, http.StatusForbidden, "Permission denied")
		return nil
	}
	return b
}

func (h *handler) abFail(ctx context.Context, w http.ResponseWriter, err error) {
	h.log.ErrorContext(ctx, "address book", slog.Any("err", err))
	internalError(w)
}

// abPersonal implements POST /api/ab/personal: {"guid"} of the caller's
// personal book, or 404 to select the legacy address book (setting).
func (h *handler) abPersonal(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	legacy, err := store.GetOr(ctx, h.st.Settings, SettingABLegacyOnly, false)
	if err != nil {
		h.abFail(ctx, w, err)
		return
	}
	if legacy {
		writeError(w, http.StatusNotFound, "Personal address book is disabled")
		return
	}
	b, err := h.st.AddressBooks.EnsurePersonal(ctx, sessionFrom(ctx).user.ID)
	if err != nil {
		h.abFail(ctx, w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"guid": b.GUID})
}

// abSettings implements POST /api/ab/settings.
func (h *handler) abSettings(w http.ResponseWriter, r *http.Request) {
	n, err := store.GetOr(r.Context(), h.st.Settings, SettingABMaxPeers, 0)
	if err != nil {
		h.abFail(r.Context(), w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]int{"max_peer_one_ab": max(n, 0)})
}

// abSharedProfiles implements POST /api/ab/shared/profiles?current&pageSize.
func (h *handler) abSharedProfiles(w http.ResponseWriter, r *http.Request) {
	pg := parsePage(r)
	books, total, err := h.st.AddressBooks.ListShared(r.Context(), sessionFrom(r.Context()).user.ID, pg.limit, pg.offset)
	if err != nil {
		h.abFail(r.Context(), w, err)
		return
	}
	out := make([]abProfile, 0, len(books))
	for _, b := range books {
		p := abProfile{GUID: b.GUID, Name: b.Name, Owner: b.OwnerUsername, Note: b.Note, Rule: int(b.Rule)}
		if b.DefaultPassword != "" {
			p.Info = &abProfileInfo{Password: b.DefaultPassword}
		}
		out = append(out, p)
	}
	writeJSON(w, http.StatusOK, newList(total, out))
}

// abPeers implements POST /api/ab/peers?current&pageSize&ab=<guid>.
func (h *handler) abPeers(w http.ResponseWriter, r *http.Request) {
	b := h.book(w, r, store.ABRead)
	if b == nil {
		return
	}
	pg := parsePage(r)
	entries, total, err := h.st.AddressBooks.ListEntries(r.Context(), b.ID, pg.limit, pg.offset)
	if err != nil {
		h.abFail(r.Context(), w, err)
		return
	}
	out := make([]abPeer, 0, len(entries))
	for _, e := range entries {
		out = append(out, newABPeer(e))
	}
	writeJSON(w, http.StatusOK, newList(total, out))
}

// abTags implements POST /api/ab/tags/{guid}: a bare JSON array.
func (h *handler) abTags(w http.ResponseWriter, r *http.Request) {
	b := h.book(w, r, store.ABRead)
	if b == nil {
		return
	}
	tags, err := h.st.AddressBooks.ListTags(r.Context(), b.ID)
	if err != nil {
		h.abFail(r.Context(), w, err)
		return
	}
	out := make([]abTag, 0, len(tags))
	for _, t := range tags {
		out = append(out, abTag{Name: t.Name, Color: t.Color})
	}
	writeJSON(w, http.StatusOK, out)
}

// abPeerAdd implements POST /api/ab/peer/add/{guid}. Adding an existing id
// updates it (idempotent retries).
func (h *handler) abPeerAdd(w http.ResponseWriter, r *http.Request) {
	b := h.book(w, r, store.ABReadWrite)
	if b == nil {
		return
	}
	raw, err := readBody(w, r, maxBody)
	if err != nil || len(raw) == 0 {
		writeError(w, http.StatusBadRequest, "Invalid request")
		return
	}
	id, patch, err := parsePeer(raw)
	if err != nil {
		writeError(w, http.StatusBadRequest, "Invalid peer")
		return
	}
	ctx := r.Context()
	limit, err := store.GetOr(ctx, h.st.Settings, SettingABMaxPeers, 0)
	if err != nil {
		h.abFail(ctx, w, err)
		return
	}
	errLimit := errors.New("limit")
	err = h.st.Tx(ctx, func(tx *store.Store) error {
		if limit > 0 {
			if _, err := tx.AddressBooks.GetEntry(ctx, b.ID, id); errors.Is(err, store.ErrNotFound) {
				n, err := tx.AddressBooks.CountEntries(ctx, b.ID)
				if err != nil {
					return err
				}
				if n >= limit {
					return errLimit
				}
			} else if err != nil {
				return err
			}
		}
		_, err := tx.AddressBooks.UpsertEntry(ctx, b.ID, id, patch)
		return err
	})
	switch {
	case errors.Is(err, errLimit):
		writeError(w, http.StatusBadRequest, "exceed_max_devices")
	case err != nil:
		h.abFail(ctx, w, err)
	default:
		writeEmpty(w)
	}
}

// abPeerUpdate implements PUT /api/ab/peer/update/{guid}: a partial update,
// absent keys are left unchanged.
func (h *handler) abPeerUpdate(w http.ResponseWriter, r *http.Request) {
	b := h.book(w, r, store.ABReadWrite)
	if b == nil {
		return
	}
	raw, err := readBody(w, r, maxBody)
	if err != nil || len(raw) == 0 {
		writeError(w, http.StatusBadRequest, "Invalid request")
		return
	}
	id, patch, err := parsePeer(raw)
	if err != nil {
		writeError(w, http.StatusBadRequest, "Invalid peer")
		return
	}
	err = h.st.AddressBooks.PatchEntry(r.Context(), b.ID, id, patch)
	switch {
	case errors.Is(err, store.ErrNotFound):
		writeError(w, http.StatusNotFound, "Peer not found")
	case err != nil:
		h.abFail(r.Context(), w, err)
	default:
		writeEmpty(w)
	}
}

// decodeNames reads a DELETE body: a JSON array of strings.
func decodeNames(w http.ResponseWriter, r *http.Request) ([]string, bool) {
	var names []string
	if err := decodeBody(w, r, &names, maxBody); err != nil || len(names) > 10000 {
		writeError(w, http.StatusBadRequest, "Invalid request")
		return nil, false
	}
	return names, true
}

// abPeerDelete implements DELETE /api/ab/peer/{guid} with body ["id", ...].
func (h *handler) abPeerDelete(w http.ResponseWriter, r *http.Request) {
	b := h.book(w, r, store.ABFull)
	if b == nil {
		return
	}
	ids, ok := decodeNames(w, r)
	if !ok {
		return
	}
	if _, err := h.st.AddressBooks.DeleteEntries(r.Context(), b.ID, ids); err != nil {
		h.abFail(r.Context(), w, err)
		return
	}
	writeEmpty(w)
}

type tagBody struct {
	Name  string          `json:"name"`
	Color json.RawMessage `json:"color"`
}

func decodeTag(w http.ResponseWriter, r *http.Request) (string, uint32, bool) {
	var t tagBody
	if err := decodeBody(w, r, &t, maxBody); err != nil || !validTagName(t.Name) {
		writeError(w, http.StatusBadRequest, "Invalid tag")
		return "", 0, false
	}
	color := store.DefaultTagColor
	if len(t.Color) > 0 && string(t.Color) != "null" {
		c, ok := parseColor(t.Color)
		if !ok {
			writeError(w, http.StatusBadRequest, "Invalid tag color")
			return "", 0, false
		}
		color = c
	}
	return t.Name, color, true
}

// abTagAdd implements POST /api/ab/tag/add/{guid} {"name","color"}.
func (h *handler) abTagAdd(w http.ResponseWriter, r *http.Request) {
	b := h.book(w, r, store.ABReadWrite)
	if b == nil {
		return
	}
	name, color, ok := decodeTag(w, r)
	if !ok {
		return
	}
	if err := h.st.AddressBooks.UpsertTag(r.Context(), b.ID, name, color); err != nil {
		h.abFail(r.Context(), w, err)
		return
	}
	writeEmpty(w)
}

// abTagUpdate implements PUT /api/ab/tag/update/{guid} {"name","color"}.
func (h *handler) abTagUpdate(w http.ResponseWriter, r *http.Request) {
	b := h.book(w, r, store.ABReadWrite)
	if b == nil {
		return
	}
	name, color, ok := decodeTag(w, r)
	if !ok {
		return
	}
	err := h.st.AddressBooks.SetTagColor(r.Context(), b.ID, name, color)
	switch {
	case errors.Is(err, store.ErrNotFound):
		writeError(w, http.StatusNotFound, "Tag not found")
	case err != nil:
		h.abFail(r.Context(), w, err)
	default:
		writeEmpty(w)
	}
}

// abTagRename implements PUT /api/ab/tag/rename/{guid} {"old","new"}; peers'
// tag lists follow because entries reference tags by id.
func (h *handler) abTagRename(w http.ResponseWriter, r *http.Request) {
	b := h.book(w, r, store.ABReadWrite)
	if b == nil {
		return
	}
	var req struct {
		Old string `json:"old"`
		New string `json:"new"`
	}
	if err := decodeBody(w, r, &req, maxBody); err != nil || !validTagName(req.Old) || !validTagName(req.New) {
		writeError(w, http.StatusBadRequest, "Invalid tag")
		return
	}
	err := h.st.AddressBooks.RenameTag(r.Context(), b.ID, req.Old, req.New)
	switch {
	case errors.Is(err, store.ErrNotFound):
		writeError(w, http.StatusNotFound, "Tag not found")
	case errors.Is(err, store.ErrConflict):
		writeError(w, http.StatusConflict, "Tag already exists")
	case err != nil:
		h.abFail(r.Context(), w, err)
	default:
		writeEmpty(w)
	}
}

// abTagDelete implements DELETE /api/ab/tag/{guid} with body ["tag", ...];
// the tags are removed from every peer as well.
func (h *handler) abTagDelete(w http.ResponseWriter, r *http.Request) {
	b := h.book(w, r, store.ABFull)
	if b == nil {
		return
	}
	names, ok := decodeNames(w, r)
	if !ok {
		return
	}
	if _, err := h.st.AddressBooks.DeleteTags(r.Context(), b.ID, names); err != nil {
		h.abFail(r.Context(), w, err)
		return
	}
	writeEmpty(w)
}
