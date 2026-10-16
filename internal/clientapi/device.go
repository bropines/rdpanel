package clientapi

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/bropines/rdpanel/internal/buildinfo"
	"github.com/bropines/rdpanel/internal/httpx"
	"github.com/bropines/rdpanel/internal/store"
)

// Plain-text sysinfo answers, compared byte-for-byte by the client.
const (
	sysinfoUpdated = "SYSINFO_UPDATED"
)

// heartbeatWriteInterval throttles heartbeat writes (clients beat every 3 s
// while controlled, 15 s otherwise).
const heartbeatWriteInterval = 30 * time.Second

const maxUUIDLen = 256

type heartbeatRequest struct {
	ID         string  `json:"id"`
	UUID       string  `json:"uuid"`
	Ver        int64   `json:"ver"`
	Conns      []int32 `json:"conns"`
	ModifiedAt int64   `json:"modified_at"`
}

type heartbeatResponse struct {
	// Sysinfo (presence matters) makes the client re-upload sysinfo.
	Sysinfo    *bool         `json:"sysinfo,omitempty"`
	Disconnect []int32       `json:"disconnect,omitempty"`
	ModifiedAt *int64        `json:"modified_at,omitempty"`
	Strategy   *strategyJSON `json:"strategy,omitempty"`
}

type strategyJSON struct {
	ConfigOptions map[string]string `json:"config_options"`
	Extra         map[string]string `json:"extra"`
}

// heartbeat implements POST /api/heartbeat. It never creates a device or
// changes its uuid: unknown ids are asked for sysinfo (which may create the
// device), and beats whose uuid does not match the stored one are ignored.
func (h *handler) heartbeat(w http.ResponseWriter, r *http.Request) {
	var req heartbeatRequest
	if err := decodeBody(w, r, &req, maxBody); err != nil || !validID(req.ID) || len(req.UUID) > maxUUIDLen ||
		len(req.Conns) > 1000 {
		writeError(w, http.StatusBadRequest, "Invalid request")
		return
	}
	ctx := r.Context()
	yes := true
	var resp heartbeatResponse
	d, err := h.st.Devices.GetByRustdeskID(ctx, req.ID)
	switch {
	case errors.Is(err, store.ErrNotFound):
		if req.UUID != "" {
			resp.Sysinfo = &yes
		}
		writeJSON(w, http.StatusOK, resp)
		return
	case err != nil:
		h.log.ErrorContext(ctx, "heartbeat: load device", slog.Any("err", err))
		internalError(w)
		return
	}
	if req.UUID == "" || (d.UUID != "" && d.UUID != req.UUID) {
		h.log.DebugContext(ctx, "heartbeat: uuid mismatch", slog.String("id", req.ID))
		writeJSON(w, http.StatusOK, resp)
		return
	}
	if _, err := h.st.Devices.TouchHeartbeat(ctx, d.ID, httpx.ClientIPString(r), heartbeatWriteInterval); err != nil {
		h.log.WarnContext(ctx, "heartbeat: touch", slog.Any("err", err))
	}
	if d.UUID == "" || d.SysinfoPending() {
		resp.Sysinfo = &yes
	}
	resp.Disconnect = h.Kicks.Take(d.RustdeskID, req.Conns)
	if h.Strategies != nil {
		s, err := h.Strategies.StrategyFor(ctx, d)
		if err != nil {
			h.log.WarnContext(ctx, "heartbeat: strategy", slog.Any("err", err))
		} else if s != nil && s.ModifiedAt != req.ModifiedAt {
			m := s.ModifiedAt
			resp.ModifiedAt = &m
			resp.Strategy = &strategyJSON{ConfigOptions: nonNilMap(s.ConfigOptions), Extra: nonNilMap(s.Extra)}
		}
	}
	writeJSON(w, http.StatusOK, resp)
}

func nonNilMap(m map[string]string) map[string]string {
	if m == nil {
		return map[string]string{}
	}
	return m
}

type sysinfoRequest struct {
	ID       string  `json:"id"`
	UUID     string  `json:"uuid"`
	CPU      string  `json:"cpu"`
	Memory   string  `json:"memory"`
	OS       string  `json:"os"`
	Hostname string  `json:"hostname"`
	Username *string `json:"username"`
	Version  string  `json:"version"`

	// Deployment presets (custom-client options, keys in the client's
	// config/keys.rs). preset-device-username and preset-device-name need no
	// handling: the client already sends them as username/hostname.
	PresetUserName        string `json:"preset-user-name"`
	PresetDeviceGroupName string `json:"preset-device-group-name"`
	PresetNote            string `json:"preset-note"`
	PresetStrategyName    string `json:"preset-strategy-name"`
	PresetABName          string `json:"preset-address-book-name"`
	PresetABTag           string `json:"preset-address-book-tag"`
	PresetABAlias         string `json:"preset-address-book-alias"`
	PresetABPassword      string `json:"preset-address-book-password"`
	PresetABNote          string `json:"preset-address-book-note"`
}

var errUUIDMismatch = errors.New("uuid mismatch")

// sysinfo implements POST /api/sysinfo (plain-text answer). It may create the
// device. The uuid may change only while the device has no owner, when no
// uuid was stored yet, or when the request carries a valid Bearer token of
// the owner. On the device's first sysinfo (a new device, or one registered
// earlier by `rustdesk --deploy` without sysinfo) the deployment presets are
// honoured: preset-user-name, preset-device-group-name and preset-note fill
// unset fields (existing users and groups only), and preset-address-book-*
// add the device to the named shared address book. preset-strategy-name is
// ignored (no strategies yet).
func (h *handler) sysinfo(w http.ResponseWriter, r *http.Request) {
	var req sysinfoRequest
	if err := decodeBody(w, r, &req, maxBody); err != nil || !validID(req.ID) || req.UUID == "" ||
		len(req.UUID) > maxUUIDLen {
		writeError(w, http.StatusBadRequest, "Invalid request")
		return
	}
	ctx := r.Context()
	info := store.Sysinfo{
		UUID: req.UUID, Hostname: clip(req.Hostname, 255), OS: clip(req.OS, 512), CPU: clip(req.CPU, 512),
		Memory: clip(req.Memory, 64), ClientVersion: clip(req.Version, 64),
	}
	if req.Username != nil {
		s := clip(*req.Username, 255)
		info.OSUsername = &s
	}
	// The client sends no Authorization header here; a token is optional proof
	// of ownership for uuid changes.
	var bearerUser int64
	if r.Header.Get("Authorization") != "" {
		if s, err := h.authenticate(r, false); err == nil {
			bearerUser = s.user.ID
		}
	}
	err := h.st.Tx(ctx, func(tx *store.Store) error {
		d, err := tx.Devices.GetByRustdeskID(ctx, req.ID)
		if errors.Is(err, store.ErrNotFound) {
			return h.createDevice(ctx, tx, &req, info)
		}
		if err != nil {
			return err
		}
		if d.UUID != "" && d.UUID != req.UUID && d.OwnerUserID != nil && *d.OwnerUserID != bearerUser {
			return errUUIDMismatch
		}
		first := d.SysinfoAt == nil
		if err := tx.Devices.UpdateSysinfo(ctx, d.ID, info); err != nil || !first {
			return err
		}
		// First sysinfo of a pre-registered (deployed) device: presets apply.
		d.Hostname = info.Hostname
		if info.OSUsername != nil {
			d.OSUsername = *info.OSUsername
		}
		if err := h.applyPresets(ctx, tx, d, &req); err != nil {
			return err
		}
		if err := tx.Devices.Update(ctx, d); err != nil {
			return err
		}
		return h.applyABPresetFromSysinfo(ctx, tx, d, &req)
	})
	switch {
	case errors.Is(err, errUUIDMismatch):
		h.log.InfoContext(ctx, "sysinfo: uuid change refused", slog.String("id", req.ID),
			slog.String("ip", httpx.ClientIPString(r)))
		writeError(w, http.StatusForbidden, "Device uuid mismatch")
	case err != nil:
		h.log.ErrorContext(ctx, "sysinfo", slog.Any("err", err))
		internalError(w)
	default:
		writeText(w, http.StatusOK, sysinfoUpdated)
	}
}

func (h *handler) createDevice(ctx context.Context, tx *store.Store, req *sysinfoRequest, info store.Sysinfo) error {
	now := h.Now()
	d := &store.Device{
		RustdeskID: req.ID, UUID: info.UUID, Hostname: info.Hostname, OS: info.OS, CPU: info.CPU,
		Memory: info.Memory, ClientVersion: info.ClientVersion, SysinfoAt: &now,
	}
	if info.OSUsername != nil {
		d.OSUsername = *info.OSUsername
	}
	if err := h.applyPresets(ctx, tx, d, req); err != nil {
		return err
	}
	if err := tx.Devices.Create(ctx, d); err != nil {
		return err
	}
	return h.applyABPresetFromSysinfo(ctx, tx, d, req)
}

// applyPresets fills the unset owner, device group and note of d on its first
// sysinfo: the owner is the user already logged in on this device (newest
// client token), else preset-user-name; unknown users and groups are skipped.
func (h *handler) applyPresets(ctx context.Context, tx *store.Store, d *store.Device, req *sysinfoRequest) error {
	if d.OwnerUserID == nil {
		if owner, err := tx.Devices.GuessOwner(ctx, req.ID, req.UUID); err == nil {
			d.OwnerUserID = &owner
		} else if !errors.Is(err, store.ErrNotFound) {
			return err
		}
	}
	if d.OwnerUserID == nil && strings.TrimSpace(req.PresetUserName) != "" {
		u, err := tx.Users.GetByUsername(ctx, strings.TrimSpace(req.PresetUserName))
		if err == nil && u.IsActive() {
			d.OwnerUserID = &u.ID
		} else if err != nil && !errors.Is(err, store.ErrNotFound) {
			return err
		}
	}
	if d.DeviceGroupID == nil && strings.TrimSpace(req.PresetDeviceGroupName) != "" {
		g, err := tx.DeviceGroups.GetByName(ctx, strings.TrimSpace(req.PresetDeviceGroupName))
		if err == nil {
			d.DeviceGroupID = &g.ID
		} else if !errors.Is(err, store.ErrNotFound) {
			return err
		}
	}
	if d.Note == "" && req.PresetNote != "" {
		d.Note = clip(req.PresetNote, maxField)
	}
	if req.PresetStrategyName != "" {
		h.log.DebugContext(ctx, "sysinfo: preset-strategy-name ignored (strategies not supported)",
			slog.String("id", req.ID))
	}
	return nil
}

// applyABPresetFromSysinfo applies preset-address-book-*; a missing or
// ambiguous book is logged and skipped (the device is still registered).
func (h *handler) applyABPresetFromSysinfo(ctx context.Context, tx *store.Store, d *store.Device, req *sysinfoRequest) error {
	p := abPreset{Name: req.PresetABName, Tag: req.PresetABTag}
	if req.PresetABAlias != "" {
		p.Alias = &req.PresetABAlias
	}
	if req.PresetABPassword != "" {
		p.Password = &req.PresetABPassword
	}
	if req.PresetABNote != "" {
		p.Note = &req.PresetABNote
	}
	err := applyABPreset(ctx, tx, d, p)
	var ae assignError
	if errors.As(err, &ae) {
		h.log.InfoContext(ctx, "sysinfo: address book preset skipped", slog.String("id", d.RustdeskID),
			slog.String("reason", string(ae)))
		return nil
	}
	return err
}

// sysinfoVer implements POST /api/sysinfo_ver (client 1.4.0): an opaque
// version string; it changes on restart so clients re-upload sysinfo.
func (h *handler) sysinfoVer(w http.ResponseWriter, _ *http.Request) {
	writeText(w, http.StatusOK, fmt.Sprintf("%s\n%d", buildinfo.Version, h.startedAt.Unix()))
}
