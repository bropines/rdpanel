package adminapi

import (
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/bropines/rdpanel/internal/httpx"
	"github.com/bropines/rdpanel/internal/store"
)

type controlRoleDTO struct {
	ID                 int64     `json:"id"`
	Name               string    `json:"name"`
	Note               string    `json:"note"`
	AllowKeyboardMouse bool      `json:"allow_keyboard_mouse"`
	AllowClipboard     bool      `json:"allow_clipboard"`
	AllowFileTransfer  bool      `json:"allow_file_transfer"`
	AllowAudio         bool      `json:"allow_audio"`
	AllowTCPTunneling  bool      `json:"allow_tcp_tunneling"`
	AllowTerminal      bool      `json:"allow_terminal"`
	AllowRestart       bool      `json:"allow_restart"`
	IsDefault          bool      `json:"is_default"`
	RuleCount          int       `json:"rule_count"`
	CreatedAt          time.Time `json:"created_at"`
	UpdatedAt          time.Time `json:"updated_at"`
}

func newControlRoleDTO(cr *store.ControlRole) controlRoleDTO {
	return controlRoleDTO{
		ID:                 cr.ID,
		Name:               cr.Name,
		Note:               cr.Note,
		AllowKeyboardMouse: cr.AllowKeyboardMouse,
		AllowClipboard:     cr.AllowClipboard,
		AllowFileTransfer:  cr.AllowFileTransfer,
		AllowAudio:         cr.AllowAudio,
		AllowTCPTunneling:  cr.AllowTCPTunneling,
		AllowTerminal:      cr.AllowTerminal,
		AllowRestart:       cr.AllowRestart,
		IsDefault:          cr.IsDefault,
		RuleCount:          cr.RuleCount,
		CreatedAt:          cr.CreatedAt,
		UpdatedAt:          cr.UpdatedAt,
	}
}

type controlRoleInput struct {
	Name               *string `json:"name"`
	Note               *string `json:"note"`
	AllowKeyboardMouse *bool   `json:"allow_keyboard_mouse"`
	AllowClipboard     *bool   `json:"allow_clipboard"`
	AllowFileTransfer  *bool   `json:"allow_file_transfer"`
	AllowAudio         *bool   `json:"allow_audio"`
	AllowTCPTunneling  *bool   `json:"allow_tcp_tunneling"`
	AllowTerminal      *bool   `json:"allow_terminal"`
	AllowRestart       *bool   `json:"allow_restart"`
	IsDefault          *bool   `json:"is_default"`
}

func (in *controlRoleInput) validate(create bool) map[string]string {
	f := map[string]string{}
	if in.Name != nil {
		*in.Name = strings.TrimSpace(*in.Name)
		if *in.Name == "" || runes(*in.Name) > 64 {
			f["name"] = "must be 1-64 characters"
		}
	} else if create {
		f["name"] = "required"
	}
	if in.Note != nil && runes(*in.Note) > 4096 {
		f["note"] = "must be at most 4096 characters"
	}
	return f
}

func (a *resources) handleListControlRoles(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	roles, err := a.d.Store.ControlRoles.List(ctx)
	if err != nil {
		a.fail500(w, r, "list control roles", err)
		return
	}
	out := make([]controlRoleDTO, 0, len(roles))
	for _, cr := range roles {
		out = append(out, newControlRoleDTO(cr))
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"items": out})
}

func (a *resources) handleCreateControlRole(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	var in controlRoleInput
	if !decodeJSON(w, r, &in) {
		return
	}
	if f := in.validate(true); len(f) > 0 {
		validationFailed(w, "invalid control role", f)
		return
	}
	cr := &store.ControlRole{
		Name: *in.Name,
	}
	if in.Note != nil {
		cr.Note = *in.Note
	}
	if in.AllowKeyboardMouse != nil {
		cr.AllowKeyboardMouse = *in.AllowKeyboardMouse
	}
	if in.AllowClipboard != nil {
		cr.AllowClipboard = *in.AllowClipboard
	}
	if in.AllowFileTransfer != nil {
		cr.AllowFileTransfer = *in.AllowFileTransfer
	}
	if in.AllowAudio != nil {
		cr.AllowAudio = *in.AllowAudio
	}
	if in.AllowTCPTunneling != nil {
		cr.AllowTCPTunneling = *in.AllowTCPTunneling
	}
	if in.AllowTerminal != nil {
		cr.AllowTerminal = *in.AllowTerminal
	}
	if in.AllowRestart != nil {
		cr.AllowRestart = *in.AllowRestart
	}
	if in.IsDefault != nil {
		cr.IsDefault = *in.IsDefault
	}

	if err := a.d.Store.ControlRoles.Create(ctx, cr); err != nil {
		if errors.Is(err, store.ErrConflict) {
			conflict(w, "conflict", "a control role with this name already exists")
			return
		}
		a.writeStoreErr(w, r, "create control role", err)
		return
	}
	row, err := a.d.Store.ControlRoles.Get(ctx, cr.ID)
	if err != nil {
		a.fail500(w, r, "create control role", err)
		return
	}
	a.audit(ctx, r, "control_role.create", "control_role", idStr(cr.ID), map[string]any{"name": cr.Name})
	httpx.WriteJSON(w, http.StatusCreated, newControlRoleDTO(row))
}

func (a *resources) handleGetControlRole(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	id, ok := pathID(w, r, "role_id")
	if !ok {
		return
	}
	cr, err := a.d.Store.ControlRoles.Get(ctx, id)
	if errors.Is(err, store.ErrNotFound) {
		httpx.NotFound(w, r)
		return
	}
	if err != nil {
		a.fail500(w, r, "get control role", err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, newControlRoleDTO(cr))
}

func (a *resources) handleUpdateControlRole(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	id, ok := pathID(w, r, "role_id")
	if !ok {
		return
	}
	var in controlRoleInput
	if !decodeJSON(w, r, &in) {
		return
	}
	if f := in.validate(false); len(f) > 0 {
		validationFailed(w, "invalid control role", f)
		return
	}

	var cr *store.ControlRole
	err := a.d.Store.Tx(ctx, func(tx *store.Store) error {
		var err error
		cr, err = tx.ControlRoles.Get(ctx, id)
		if err != nil {
			return err
		}
		if in.Name != nil {
			cr.Name = *in.Name
		}
		if in.Note != nil {
			cr.Note = *in.Note
		}
		if in.AllowKeyboardMouse != nil {
			cr.AllowKeyboardMouse = *in.AllowKeyboardMouse
		}
		if in.AllowClipboard != nil {
			cr.AllowClipboard = *in.AllowClipboard
		}
		if in.AllowFileTransfer != nil {
			cr.AllowFileTransfer = *in.AllowFileTransfer
		}
		if in.AllowAudio != nil {
			cr.AllowAudio = *in.AllowAudio
		}
		if in.AllowTCPTunneling != nil {
			cr.AllowTCPTunneling = *in.AllowTCPTunneling
		}
		if in.AllowTerminal != nil {
			cr.AllowTerminal = *in.AllowTerminal
		}
		if in.AllowRestart != nil {
			cr.AllowRestart = *in.AllowRestart
		}
		if in.IsDefault != nil {
			cr.IsDefault = *in.IsDefault
		}
		return tx.ControlRoles.Update(ctx, cr)
	})
	if errors.Is(err, store.ErrNotFound) {
		httpx.NotFound(w, r)
		return
	}
	if errors.Is(err, store.ErrConflict) {
		conflict(w, "conflict", "a control role with this name already exists")
		return
	}
	if err != nil {
		a.writeStoreErr(w, r, "update control role", err)
		return
	}
	row, err := a.d.Store.ControlRoles.Get(ctx, id)
	if err != nil {
		a.fail500(w, r, "update control role", err)
		return
	}
	a.audit(ctx, r, "control_role.update", "control_role", idStr(id), map[string]any{"name": row.Name})
	httpx.WriteJSON(w, http.StatusOK, newControlRoleDTO(row))
}

func (a *resources) handleDeleteControlRole(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	id, ok := pathID(w, r, "role_id")
	if !ok {
		return
	}
	cr, err := a.d.Store.ControlRoles.Get(ctx, id)
	if errors.Is(err, store.ErrNotFound) {
		httpx.NotFound(w, r)
		return
	}
	if err != nil {
		a.fail500(w, r, "delete control role", err)
		return
	}
	if cr.IsDefault {
		validationFailed(w, "cannot delete default control role", map[string]string{"role_id": "cannot delete default control role"})
		return
	}
	if err := a.d.Store.ControlRoles.Delete(ctx, id); err != nil {
		if errors.Is(err, store.ErrNotFound) {
			httpx.NotFound(w, r)
			return
		}
		if errors.Is(err, store.ErrRoleInUse) {
			// Deleting it would silently switch its rules to the default role.
			conflict(w, "role_in_use", "the control role is used by connection rules; change or delete those rules first")
			return
		}
		if errors.Is(err, store.ErrInvalid) {
			validationFailed(w, err.Error(), map[string]string{"role_id": err.Error()})
			return
		}
		a.writeStoreErr(w, r, "delete control role", err)
		return
	}
	a.audit(ctx, r, "control_role.delete", "control_role", idStr(id), map[string]any{"name": cr.Name})
	w.WriteHeader(http.StatusNoContent)
}

func (a *resources) handleSetDefaultControlRole(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	id, ok := pathID(w, r, "role_id")
	if !ok {
		return
	}
	if err := a.d.Store.ControlRoles.SetDefault(ctx, id); err != nil {
		if errors.Is(err, store.ErrNotFound) {
			httpx.NotFound(w, r)
			return
		}
		a.writeStoreErr(w, r, "set default control role", err)
		return
	}
	cr, err := a.d.Store.ControlRoles.Get(ctx, id)
	if err != nil {
		a.fail500(w, r, "set default control role", err)
		return
	}
	a.audit(ctx, r, "control_role.set_default", "control_role", idStr(id), map[string]any{"name": cr.Name})
	httpx.WriteJSON(w, http.StatusOK, newControlRoleDTO(cr))
}
