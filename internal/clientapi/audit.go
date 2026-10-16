package clientapi

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"strconv"
	"time"
	"unicode/utf8"

	"github.com/bropines/rdpanel/internal/metrics"
	"github.com/bropines/rdpanel/internal/store"
)

// openAuditMaxAge bounds how far back an "authorized"/"close" event looks for
// the matching "new" record (conn_id is a client-local counter).
const openAuditMaxAge = 7 * 24 * time.Hour

const maxNonceLen = 64

// maxNoteRunes caps session notes (the client limits them to 256 chars).
const maxNoteRunes = 1024

// u64 is a JSON number or string holding an unsigned 64-bit integer, kept as
// its decimal text (session ids are random u64; never go through float64).
type u64 string

func (v *u64) UnmarshalJSON(b []byte) error {
	b = bytes.TrimSpace(b)
	if string(b) == "null" {
		*v = ""
		return nil
	}
	if len(b) > 0 && b[0] == '"' {
		var s string
		if err := json.Unmarshal(b, &s); err != nil {
			return err
		}
		b = []byte(s)
	}
	n, err := strconv.ParseUint(string(b), 10, 64)
	if err != nil {
		return err
	}
	*v = u64(strconv.FormatUint(n, 10))
	return nil
}

var errAuditDevice = errors.New("unknown device or uuid mismatch")

// auditDevice checks that (id, uuid) is a known device with exactly that uuid.
func (h *handler) auditDevice(ctx context.Context, id, uuid string) error {
	if !validID(id) || uuid == "" || len(uuid) > maxUUIDLen {
		return errAuditDevice
	}
	d, err := h.st.Devices.GetByRustdeskID(ctx, id)
	if errors.Is(err, store.ErrNotFound) {
		return errAuditDevice
	}
	if err != nil {
		return err
	}
	if d.UUID == "" || d.UUID != uuid {
		return errAuditDevice
	}
	return nil
}

// storeAudit runs write after claiming nonce, in one transaction: a retried
// record (nonce already seen) is acknowledged without storing it again, and a
// failed write releases the nonce. Responses follow the 1.5.0 contract: 2xx
// with an empty body = stored; 5xx = retry; other 4xx = dropped.
func (h *handler) storeAudit(w http.ResponseWriter, r *http.Request, kind, id, uuid, nonce string, write func(tx *store.Store) error) {
	ctx := r.Context()
	if len(nonce) > maxNonceLen {
		h.Telemetry.Audit(kind, metrics.AuditRejected)
		writeError(w, http.StatusBadRequest, "Invalid nonce")
		return
	}
	if err := h.auditDevice(ctx, id, uuid); err != nil {
		if errors.Is(err, errAuditDevice) {
			h.Telemetry.Audit(kind, metrics.AuditRejected)
			writeError(w, http.StatusForbidden, "Unknown device or uuid mismatch")
			return
		}
		h.Telemetry.Audit(kind, metrics.AuditError)
		h.log.ErrorContext(ctx, "audit: load device", slog.Any("err", err))
		internalError(w)
		return
	}
	err := h.st.Tx(ctx, func(tx *store.Store) error {
		if nonce != "" {
			fresh, err := tx.AuditNonces.Claim(ctx, nonce)
			if err != nil || !fresh {
				return err
			}
		}
		return write(tx)
	})
	if err != nil {
		h.Telemetry.Audit(kind, metrics.AuditError)
		h.log.ErrorContext(ctx, "audit: store", slog.Any("err", err))
		internalError(w)
		return
	}
	h.Telemetry.Audit(kind, metrics.AuditStored)
	writeEmpty(w)
}

type connAuditRequest struct {
	ID           string   `json:"id"`
	UUID         string   `json:"uuid"`
	ConnID       int64    `json:"conn_id"`
	SessionID    u64      `json:"session_id"`
	Nonce        string   `json:"nonce"`
	Action       *string  `json:"action"`
	IP           string   `json:"ip"`
	ConnAuditRef string   `json:"conn_audit_ref"`
	Peer         []string `json:"peer"`
	Type         *int     `json:"type"`
	PrimaryAuth  *int     `json:"primary_auth"`
	TwoFactor    *int     `json:"two_factor"`
	Note         *string  `json:"note"`
}

// auditConn implements POST /api/audit/conn: "new", authorized (no action)
// and "close" events from the controlled side, plus the controller-side note
// {"id","session_id","note"} (no uuid, response ignored).
func (h *handler) auditConn(w http.ResponseWriter, r *http.Request) {
	var req connAuditRequest
	if err := decodeBody(w, r, &req, maxBody); err != nil {
		writeError(w, http.StatusBadRequest, "Invalid request")
		return
	}
	ctx := r.Context()
	if req.Note != nil && req.Action == nil && req.UUID == "" {
		h.sessionNote(ctx, req.ID, string(req.SessionID), *req.Note)
		writeEmpty(w)
		return
	}
	sessionID := string(req.SessionID)
	if sessionID == "0" {
		sessionID = "" // not logged in yet
	}
	action := ""
	if req.Action != nil {
		action = *req.Action
	}
	h.storeAudit(w, r, metrics.AuditConn, req.ID, req.UUID, req.Nonce, func(tx *store.Store) error {
		switch action {
		case "new":
			return tx.ConnectionAudits.Create(ctx, &store.ConnectionAudit{
				DeviceID: req.ID, DeviceUUID: req.UUID, ConnID: req.ConnID, SessionID: sessionID,
				ConnAuditRef: clip(req.ConnAuditRef, 256), ControllerIP: clip(req.IP, 64),
			})
		case "close":
			a, err := tx.ConnectionAudits.FindOpen(ctx, req.ID, req.ConnID, openAuditMaxAge)
			if errors.Is(err, store.ErrNotFound) {
				return nil
			}
			if err != nil {
				return err
			}
			now := h.Now()
			a.EndedAt = &now
			return tx.ConnectionAudits.Update(ctx, a)
		case "":
			a, err := tx.ConnectionAudits.FindOpen(ctx, req.ID, req.ConnID, openAuditMaxAge)
			create := errors.Is(err, store.ErrNotFound)
			if create {
				a = &store.ConnectionAudit{DeviceID: req.ID, DeviceUUID: req.UUID, ConnID: req.ConnID}
			} else if err != nil {
				return err
			}
			if sessionID != "" {
				a.SessionID = sessionID
			}
			if req.Type != nil {
				a.ConnType = *req.Type
			}
			if req.PrimaryAuth != nil {
				a.PrimaryAuth = req.PrimaryAuth
			}
			if req.TwoFactor != nil {
				a.TwoFactor = req.TwoFactor
			}
			if req.ConnAuditRef != "" {
				a.ConnAuditRef = clip(req.ConnAuditRef, 256)
			}
			if len(req.Peer) > 0 {
				a.ControllerRustdeskID = clip(req.Peer[0], maxIDLen)
			}
			if len(req.Peer) > 1 {
				a.ControllerName = clip(req.Peer[1], 255)
			}
			if create {
				return tx.ConnectionAudits.Create(ctx, a)
			}
			return tx.ConnectionAudits.Update(ctx, a)
		default:
			return nil // unknown future action: acknowledge, nothing to store
		}
	})
}

// sessionNote stores the controller's note sent to /api/audit/conn.
func (h *handler) sessionNote(ctx context.Context, deviceID, sessionID, note string) {
	if !validID(deviceID) || sessionID == "" || sessionID == "0" {
		return
	}
	a, err := h.st.ConnectionAudits.FindBySession(ctx, deviceID, sessionID, nil)
	if err != nil {
		if !errors.Is(err, store.ErrNotFound) {
			h.log.WarnContext(ctx, "audit note: find", slog.Any("err", err))
		}
		return
	}
	if err := h.st.ConnectionAudits.SetNote(ctx, a.GUID, clipRunes(note, maxNoteRunes)); err != nil {
		h.log.WarnContext(ctx, "audit note: set", slog.Any("err", err))
	}
}

func clipRunes(s string, n int) string {
	if utf8.RuneCountInString(s) <= n {
		return s
	}
	return string([]rune(s)[:n])
}

type fileAuditRequest struct {
	ID     string          `json:"id"`
	UUID   string          `json:"uuid"`
	PeerID string          `json:"peer_id"`
	ConnID int64           `json:"conn_id"`
	Type   int             `json:"type"`
	Path   string          `json:"path"`
	IsFile bool            `json:"is_file"`
	Info   json.RawMessage `json:"info"`
	Nonce  string          `json:"nonce"`
}

// auditInfo returns the info value as JSON text: the client sends a JSON
// string containing JSON; a non-JSON string is kept as {"raw": ...}.
func auditInfo(raw json.RawMessage) string {
	if len(raw) == 0 || string(raw) == "null" {
		return "{}"
	}
	var s string
	if json.Unmarshal(raw, &s) == nil {
		if json.Valid([]byte(s)) {
			return s
		}
		b, _ := json.Marshal(map[string]string{"raw": s})
		return string(b)
	}
	return string(raw)
}

// auditFile implements POST /api/audit/file.
func (h *handler) auditFile(w http.ResponseWriter, r *http.Request) {
	var req fileAuditRequest
	if err := decodeBody(w, r, &req, maxBody); err != nil {
		writeError(w, http.StatusBadRequest, "Invalid request")
		return
	}
	info := auditInfo(req.Info)
	var meta struct {
		IP   string `json:"ip"`
		Name string `json:"name"`
		Num  int64  `json:"num"`
	}
	_ = json.Unmarshal([]byte(info), &meta)
	h.storeAudit(w, r, metrics.AuditFile, req.ID, req.UUID, req.Nonce, func(tx *store.Store) error {
		return tx.FileTransferAudits.Create(r.Context(), &store.FileTransferAudit{
			DeviceID: req.ID, DeviceUUID: req.UUID, ControllerRustdeskID: clip(req.PeerID, maxIDLen),
			ControllerName: clip(meta.Name, 255), ControllerIP: clip(meta.IP, 64), Direction: req.Type,
			IsFile: req.IsFile, Path: clip(req.Path, maxField), FileCount: meta.Num, Info: info,
		})
	})
}

type alarmAuditRequest struct {
	ID           string          `json:"id"`
	UUID         string          `json:"uuid"`
	Typ          int             `json:"typ"`
	Info         json.RawMessage `json:"info"`
	ConnID       *int64          `json:"conn_id"`
	Nonce        string          `json:"nonce"`
	ConnAuditRef string          `json:"conn_audit_ref"`
}

// auditAlarm implements POST /api/audit/alarm.
func (h *handler) auditAlarm(w http.ResponseWriter, r *http.Request) {
	var req alarmAuditRequest
	if err := decodeBody(w, r, &req, maxBody); err != nil {
		writeError(w, http.StatusBadRequest, "Invalid request")
		return
	}
	h.storeAudit(w, r, metrics.AuditAlarm, req.ID, req.UUID, req.Nonce, func(tx *store.Store) error {
		return tx.AlarmAudits.Create(r.Context(), &store.AlarmAudit{
			DeviceID: req.ID, DeviceUUID: req.UUID, AlarmType: req.Typ, ConnID: req.ConnID,
			ConnAuditRef: clip(req.ConnAuditRef, 256), Info: auditInfo(req.Info),
		})
	})
}

// auditConnActive implements GET /api/audit/conn/active?id&session_id&conn_type:
// a JSON string with the conn-audit guid, or "" while the controlled side
// has not reported the session yet (the client retries).
func (h *handler) auditConnActive(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	id := q.Get("id")
	var sid u64
	if err := sid.UnmarshalJSON([]byte(q.Get("session_id"))); err != nil || !validID(id) || sid == "" {
		writeError(w, http.StatusBadRequest, "Invalid request")
		return
	}
	var connType *int
	if ct := q.Get("conn_type"); ct != "" {
		v, err := strconv.Atoi(ct)
		if err != nil {
			writeError(w, http.StatusBadRequest, "Invalid request")
			return
		}
		connType = &v
	}
	a, err := h.st.ConnectionAudits.FindBySession(r.Context(), id, string(sid), connType)
	switch {
	case errors.Is(err, store.ErrNotFound):
		writeJSON(w, http.StatusOK, "")
	case err != nil:
		h.log.ErrorContext(r.Context(), "audit active", slog.Any("err", err))
		internalError(w)
	default:
		writeJSON(w, http.StatusOK, a.GUID)
	}
}

// auditNote implements PUT /api/audit {"guid","note"} (controller-side note
// at the end of a session).
func (h *handler) auditNote(w http.ResponseWriter, r *http.Request) {
	var req struct {
		GUID string `json:"guid"`
		Note string `json:"note"`
	}
	if err := decodeBody(w, r, &req, maxBody); err != nil || req.GUID == "" || len(req.GUID) > 64 {
		writeError(w, http.StatusBadRequest, "Invalid request")
		return
	}
	err := h.st.ConnectionAudits.SetNote(r.Context(), req.GUID, clipRunes(req.Note, maxNoteRunes))
	switch {
	case errors.Is(err, store.ErrNotFound):
		writeError(w, http.StatusNotFound, "Audit record not found")
	case err != nil:
		h.log.ErrorContext(r.Context(), "audit note", slog.Any("err", err))
		internalError(w)
	default:
		writeEmpty(w)
	}
}
