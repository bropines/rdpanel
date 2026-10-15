package clientapi

import (
	"crypto/ed25519"
	"encoding/base64"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/bropines/rustdesk-panel/internal/httpx"
	"github.com/bropines/rustdesk-panel/internal/store"
)

// switchGrantSkew is the accepted clock difference for switch-grant timestamps.
const switchGrantSkew = 5 * time.Minute

// deploy results (CLI exit codes: OK 0, NOT_ENABLED 3, INVALID_INPUT 5, ID_TAKEN 6).
const (
	deployOK           = "OK"
	deployNotEnabled   = "NOT_ENABLED"
	deployInvalidInput = "INVALID_INPUT"
	deployIDTaken      = "ID_TAKEN"
)

func decodeB64(s string) ([]byte, error) {
	s = strings.TrimSpace(s)
	for _, enc := range []*base64.Encoding{base64.StdEncoding, base64.RawStdEncoding, base64.URLEncoding, base64.RawURLEncoding} {
		if b, err := enc.DecodeString(s); err == nil {
			return b, nil
		}
	}
	return nil, errors.New("invalid base64")
}

// adminOr401 authenticates an admin client token or writes 401/500.
func (h *handler) adminOr401(w http.ResponseWriter, r *http.Request) *session {
	s, err := h.requireAdminToken(r)
	if errors.Is(err, errUnauthorized) {
		writeError(w, http.StatusUnauthorized, "Unauthorized")
		return nil
	}
	if err != nil {
		h.log.ErrorContext(r.Context(), "provisioning auth", slog.Any("err", err))
		internalError(w)
		return nil
	}
	return s
}

func (h *handler) recordAdmin(r *http.Request, s *session, action, target string, details map[string]any) {
	err := h.st.AdminAudit.Record(r.Context(), &store.AuditEntry{ActorUserID: &s.user.ID, ActorName: s.user.Username,
		ActorIP: httpx.ClientIPString(r), Action: action, TargetType: "device", TargetID: target, Details: details})
	if err != nil {
		h.log.WarnContext(r.Context(), "admin audit", slog.Any("err", err))
	}
}

// deploy implements POST /api/devices/deploy {"id","uuid","pk"} with an admin
// client token: registers (or re-confirms) a device and its Ed25519 key.
func (h *handler) deploy(w http.ResponseWriter, r *http.Request) {
	s := h.adminOr401(w, r)
	if s == nil {
		return
	}
	ctx := r.Context()
	result := func(res string) { writeJSON(w, http.StatusOK, map[string]string{"result": res}) }
	enabled, err := store.GetOr(ctx, h.st.Settings, SettingDeployEnabled, true)
	if err != nil {
		internalError(w)
		return
	}
	if !enabled {
		result(deployNotEnabled)
		return
	}
	var req struct {
		ID   string `json:"id"`
		UUID string `json:"uuid"`
		PK   string `json:"pk"`
	}
	if err := decodeBody(w, r, &req, maxBody); err != nil || !validID(req.ID) || req.UUID == "" ||
		len(req.UUID) > maxUUIDLen {
		result(deployInvalidInput)
		return
	}
	if _, err := decodeB64(req.UUID); err != nil {
		result(deployInvalidInput)
		return
	}
	pk, err := decodeB64(req.PK)
	if err != nil || len(pk) != ed25519.PublicKeySize {
		result(deployInvalidInput)
		return
	}
	errTaken := errors.New("taken")
	err = h.st.Tx(ctx, func(tx *store.Store) error {
		d, err := tx.Devices.GetByRustdeskID(ctx, req.ID)
		if errors.Is(err, store.ErrNotFound) {
			return tx.Devices.Create(ctx, &store.Device{RustdeskID: req.ID, UUID: req.UUID, PublicKey: pk})
		}
		if err != nil {
			return err
		}
		if d.UUID != "" && d.UUID != req.UUID {
			return errTaken
		}
		return tx.Devices.SetIdentity(ctx, d.ID, req.UUID, pk)
	})
	switch {
	case errors.Is(err, errTaken):
		result(deployIDTaken)
	case err != nil:
		h.log.ErrorContext(ctx, "deploy", slog.Any("err", err))
		internalError(w)
	default:
		h.recordAdmin(r, s, "device.deploy", req.ID, nil)
		result(deployOK)
	}
}

type assignRequest struct {
	ID                  string  `json:"id"`
	UUID                string  `json:"uuid"`
	UserName            *string `json:"user_name"`
	StrategyName        *string `json:"strategy_name"`
	DeviceGroupName     *string `json:"device_group_name"`
	Note                *string `json:"note"`
	DeviceUsername      *string `json:"device_username"`
	DeviceName          *string `json:"device_name"`
	AddressBookName     *string `json:"address_book_name"`
	AddressBookTag      *string `json:"address_book_tag"`
	AddressBookAlias    *string `json:"address_book_alias"`
	AddressBookPassword *string `json:"address_book_password"`
	AddressBookNote     *string `json:"address_book_note"`
}

// assignError is printed verbatim by `rustdesk --assign`.
type assignError string

func (e assignError) Error() string { return string(e) }

// deviceAssign implements POST /api/devices/cli (`rustdesk --assign`) with an
// admin client token. Success is an empty body ("Done!"); any other text is
// printed verbatim by the CLI.
func (h *handler) deviceAssign(w http.ResponseWriter, r *http.Request) {
	s := h.adminOr401(w, r)
	if s == nil {
		return
	}
	ctx := r.Context()
	var req assignRequest
	if err := decodeBody(w, r, &req, maxBody); err != nil || !validID(req.ID) {
		writeText(w, http.StatusBadRequest, "Invalid request")
		return
	}
	if req.StrategyName != nil {
		writeText(w, http.StatusBadRequest, "Strategies are not supported by this server")
		return
	}
	err := h.st.Tx(ctx, func(tx *store.Store) error {
		d, err := tx.Devices.GetByRustdeskID(ctx, req.ID)
		if errors.Is(err, store.ErrNotFound) || (err == nil && d.UUID != "" && req.UUID != "" && d.UUID != req.UUID) {
			return assignError("Device not found")
		}
		if err != nil {
			return err
		}
		if req.UserName != nil {
			if *req.UserName == "" {
				d.OwnerUserID = nil
			} else {
				u, err := tx.Users.GetByUsername(ctx, *req.UserName)
				if errors.Is(err, store.ErrNotFound) {
					return assignError("User not found")
				} else if err != nil {
					return err
				}
				d.OwnerUserID = &u.ID
			}
		}
		if req.DeviceGroupName != nil {
			if *req.DeviceGroupName == "" {
				d.DeviceGroupID = nil
			} else {
				g, err := tx.DeviceGroups.GetByName(ctx, *req.DeviceGroupName)
				if errors.Is(err, store.ErrNotFound) {
					return assignError("Device group not found")
				} else if err != nil {
					return err
				}
				d.DeviceGroupID = &g.ID
			}
		}
		if req.Note != nil {
			d.Note = clip(*req.Note, maxField)
		}
		if req.DeviceUsername != nil {
			d.OSUsername = clip(*req.DeviceUsername, 255)
		}
		if req.DeviceName != nil {
			d.Hostname = clip(*req.DeviceName, 255)
		}
		if err := tx.Devices.Update(ctx, d); err != nil {
			return err
		}
		if req.AddressBookName == nil || *req.AddressBookName == "" {
			return nil
		}
		books, err := tx.AddressBooks.FindSharedByName(ctx, *req.AddressBookName)
		if err != nil {
			return err
		}
		switch len(books) {
		case 0:
			return assignError("Address book not found")
		case 1:
		default:
			return assignError("Address book name is ambiguous")
		}
		var p store.ABEntryPatch
		if req.AddressBookAlias != nil {
			p.Alias = req.AddressBookAlias
		}
		if req.AddressBookPassword != nil {
			p.Password = req.AddressBookPassword
		}
		if req.AddressBookNote != nil {
			p.Note = req.AddressBookNote
		}
		if req.AddressBookTag != nil && *req.AddressBookTag != "" {
			tags := []string{clip(*req.AddressBookTag, 128)}
			if e, err := tx.AddressBooks.GetEntry(ctx, books[0].ID, d.RustdeskID); err == nil {
				tags = append(e.Tags, tags...)
			}
			p.Tags = &tags
		}
		h2 := d.Hostname
		p.Hostname = &h2
		_, err = tx.AddressBooks.UpsertEntry(ctx, books[0].ID, d.RustdeskID, p)
		return err
	})
	var ae assignError
	switch {
	case errors.As(err, &ae):
		writeText(w, http.StatusBadRequest, string(ae))
	case err != nil:
		h.log.ErrorContext(ctx, "device assign", slog.Any("err", err))
		writeText(w, http.StatusInternalServerError, "Internal error")
	default:
		h.recordAdmin(r, s, "device.assign", req.ID, nil)
		writeEmpty(w)
	}
}

// switchGrant implements POST /api/switch-grant (client 1.5.0): verifies the
// Ed25519 signature over "switch-grant\0<id>\0<verifier>\0<timestamp>" with
// the device key recorded by deploy. Nothing consumes grants yet (the OSS
// hbbs does not enforce switch codes), so a verified grant is only acknowledged.
func (h *handler) switchGrant(w http.ResponseWriter, r *http.Request) {
	var req struct {
		ID        string `json:"id"`
		Verifier  string `json:"switch_code_verifier"`
		Timestamp string `json:"timestamp"`
		Signature string `json:"signature"`
	}
	if err := decodeBody(w, r, &req, maxBody); err != nil || !validID(req.ID) {
		writeError(w, http.StatusBadRequest, "Invalid request")
		return
	}
	rejected := map[string]bool{"accepted": false}
	d, err := h.st.Devices.GetByRustdeskID(r.Context(), req.ID)
	if errors.Is(err, store.ErrNotFound) || (err == nil && len(d.PublicKey) != ed25519.PublicKeySize) {
		writeJSON(w, http.StatusOK, rejected)
		return
	}
	if err != nil {
		internalError(w)
		return
	}
	sig, err := decodeB64(req.Signature)
	msg := fmt.Sprintf("switch-grant\x00%s\x00%s\x00%s", req.ID, req.Verifier, req.Timestamp)
	if err != nil || !ed25519.Verify(d.PublicKey, []byte(msg), sig) {
		writeJSON(w, http.StatusOK, rejected)
		return
	}
	ts, err := strconv.ParseInt(req.Timestamp, 10, 64)
	now := h.Now()
	if err != nil || now.Sub(time.Unix(ts, 0)).Abs() > switchGrantSkew {
		writeJSON(w, http.StatusOK, map[string]any{"accepted": false, "server_time": now.Unix()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"accepted": true})
}

// record implements POST /api/record (session recording upload). This server
// does not store recordings; answering {"error"} makes the client abort the
// upload instead of streaming the file. (Dead code in OSS client builds.)
func (h *handler) record(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]string{"error": "Recording upload is not supported"})
}
