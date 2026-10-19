package adminapi

import (
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/bropines/rdpanel/internal/httpx"
	"github.com/bropines/rdpanel/internal/store"
)

type connectionACLDTO struct {
	ID              int64           `json:"id"`
	Name            string          `json:"name"`
	Note            string          `json:"note"`
	UserID          *int64          `json:"user_id"`
	UserName        string          `json:"user_name,omitempty"`
	UserGroupID     *int64          `json:"user_group_id"`
	UserGroupName   string          `json:"user_group_name,omitempty"`
	DeviceID        *int64          `json:"device_id"`
	DeviceName      string          `json:"device_name,omitempty"`
	DeviceGroupID   *int64          `json:"device_group_id"`
	DeviceGroupName string          `json:"device_group_name,omitempty"`
	ControlRoleID   *int64          `json:"control_role_id"`
	ControlRoleName string          `json:"control_role_name,omitempty"`
	Action          store.ACLAction `json:"action"`
	Priority        int             `json:"priority"`
	Enabled         bool            `json:"enabled"`
	CreatedAt       time.Time       `json:"created_at"`
	UpdatedAt       time.Time       `json:"updated_at"`
}

func newConnectionACLDTO(a *store.ConnectionACL) connectionACLDTO {
	return connectionACLDTO{
		ID:              a.ID,
		Name:            a.Name,
		Note:            a.Note,
		UserID:          a.UserID,
		UserName:        a.UserName,
		UserGroupID:     a.UserGroupID,
		UserGroupName:   a.UserGroupName,
		DeviceID:        a.DeviceID,
		DeviceName:      a.DeviceName,
		DeviceGroupID:   a.DeviceGroupID,
		DeviceGroupName: a.DeviceGroupName,
		ControlRoleID:   a.ControlRoleID,
		ControlRoleName: a.ControlRoleName,
		Action:          a.Action,
		Priority:        a.Priority,
		Enabled:         a.Enabled,
		CreatedAt:       a.CreatedAt,
		UpdatedAt:       a.UpdatedAt,
	}
}

type connectionACLInput struct {
	Name          *string          `json:"name"`
	Note          *string          `json:"note"`
	UserID        *int64           `json:"user_id"`
	UserGroupID   *int64           `json:"user_group_id"`
	DeviceID      *int64           `json:"device_id"`
	DeviceGroupID *int64           `json:"device_group_id"`
	ControlRoleID *int64           `json:"control_role_id"`
	Action        *store.ACLAction `json:"action"`
	Priority      *int             `json:"priority"`
	Enabled       *bool            `json:"enabled"`
}

func (in *connectionACLInput) validate() map[string]string {
	f := map[string]string{}
	if in.Name != nil {
		*in.Name = strings.TrimSpace(*in.Name)
		if *in.Name == "" || runes(*in.Name) > 64 {
			f["name"] = "must be 1-64 characters"
		}
	} else {
		f["name"] = "required"
	}
	if in.Note != nil && runes(*in.Note) > 4096 {
		f["note"] = "must be at most 4096 characters"
	}
	if in.Action != nil {
		if *in.Action != store.ACLActionAllow && *in.Action != store.ACLActionDeny {
			f["action"] = "must be 'allow' or 'deny'"
		}
	} else {
		f["action"] = "required"
	}
	if in.Priority != nil && (*in.Priority < 1 || *in.Priority > 1000000) {
		f["priority"] = "must be between 1 and 1000000"
	}
	if in.UserID != nil && in.UserGroupID != nil {
		f["user_id"] = "cannot specify both user_id and user_group_id"
	}
	if in.DeviceID != nil && in.DeviceGroupID != nil {
		f["device_id"] = "cannot specify both device_id and device_group_id"
	}
	return f
}

type connectionACLPatchInput struct {
	Name          *string          `json:"name"`
	Note          *string          `json:"note"`
	UserID        opt[int64]       `json:"user_id"`
	UserGroupID   opt[int64]       `json:"user_group_id"`
	DeviceID      opt[int64]       `json:"device_id"`
	DeviceGroupID opt[int64]       `json:"device_group_id"`
	ControlRoleID opt[int64]       `json:"control_role_id"`
	Action        *store.ACLAction `json:"action"`
	Priority      *int             `json:"priority"`
	Enabled       *bool            `json:"enabled"`
}

func (in *connectionACLPatchInput) validate() map[string]string {
	f := map[string]string{}
	if in.Name != nil {
		*in.Name = strings.TrimSpace(*in.Name)
		if *in.Name == "" || runes(*in.Name) > 64 {
			f["name"] = "must be 1-64 characters"
		}
	}
	if in.Note != nil && runes(*in.Note) > 4096 {
		f["note"] = "must be at most 4096 characters"
	}
	if in.Action != nil && *in.Action != store.ACLActionAllow && *in.Action != store.ACLActionDeny {
		f["action"] = "must be 'allow' or 'deny'"
	}
	if in.Priority != nil && (*in.Priority < 1 || *in.Priority > 1000000) {
		f["priority"] = "must be between 1 and 1000000"
	}
	if in.UserID.Set && !in.UserID.Null && in.UserGroupID.Set && !in.UserGroupID.Null {
		f["user_id"] = "cannot specify both user_id and user_group_id"
	}
	if in.DeviceID.Set && !in.DeviceID.Null && in.DeviceGroupID.Set && !in.DeviceGroupID.Null {
		f["device_id"] = "cannot specify both device_id and device_group_id"
	}
	return f
}

func (a *resources) handleListACLs(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	rules, err := a.d.Store.ConnectionACLs.List(ctx)
	if err != nil {
		a.fail500(w, r, "list acls", err)
		return
	}
	out := make([]connectionACLDTO, 0, len(rules))
	for _, rule := range rules {
		out = append(out, newConnectionACLDTO(rule))
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"items": out})
}

func (a *resources) handleCreateACL(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	var in connectionACLInput
	if !decodeJSON(w, r, &in) {
		return
	}
	if f := in.validate(); len(f) > 0 {
		validationFailed(w, "invalid acl rule", f)
		return
	}
	rule := &store.ConnectionACL{
		Name:          *in.Name,
		UserID:        in.UserID,
		UserGroupID:   in.UserGroupID,
		DeviceID:      in.DeviceID,
		DeviceGroupID: in.DeviceGroupID,
		ControlRoleID: in.ControlRoleID,
		Action:        *in.Action,
		Priority:      100,
		Enabled:       true,
	}
	if in.Note != nil {
		rule.Note = *in.Note
	}
	if in.Priority != nil {
		rule.Priority = *in.Priority
	}
	if in.Enabled != nil {
		rule.Enabled = *in.Enabled
	}

	if err := a.d.Store.ConnectionACLs.Create(ctx, rule); err != nil {
		a.writeStoreErr(w, r, "create acl rule", err)
		return
	}
	row, err := a.d.Store.ConnectionACLs.Get(ctx, rule.ID)
	if err != nil {
		a.fail500(w, r, "create acl rule", err)
		return
	}
	a.audit(ctx, r, "acl.create", "acl", idStr(rule.ID), map[string]any{"name": rule.Name, "action": rule.Action})
	httpx.WriteJSON(w, http.StatusCreated, newConnectionACLDTO(row))
}

func (a *resources) handleGetACL(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	id, ok := pathID(w, r, "acl_id")
	if !ok {
		return
	}
	rule, err := a.d.Store.ConnectionACLs.Get(ctx, id)
	if errors.Is(err, store.ErrNotFound) {
		httpx.NotFound(w, r)
		return
	}
	if err != nil {
		a.fail500(w, r, "get acl rule", err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, newConnectionACLDTO(rule))
}

func (a *resources) handleUpdateACL(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	id, ok := pathID(w, r, "acl_id")
	if !ok {
		return
	}
	var in connectionACLPatchInput
	if !decodeJSON(w, r, &in) {
		return
	}
	if f := in.validate(); len(f) > 0 {
		validationFailed(w, "invalid acl rule", f)
		return
	}

	var rule *store.ConnectionACL
	err := a.d.Store.Tx(ctx, func(tx *store.Store) error {
		var err error
		rule, err = tx.ConnectionACLs.Get(ctx, id)
		if err != nil {
			return err
		}
		if in.Name != nil {
			rule.Name = *in.Name
		}
		if in.Note != nil {
			rule.Note = *in.Note
		}
		if in.UserID.Set {
			rule.UserID = in.UserID.ptr()
			if rule.UserID != nil {
				rule.UserGroupID = nil
			}
		}
		if in.UserGroupID.Set {
			rule.UserGroupID = in.UserGroupID.ptr()
			if rule.UserGroupID != nil {
				rule.UserID = nil
			}
		}
		if in.DeviceID.Set {
			rule.DeviceID = in.DeviceID.ptr()
			if rule.DeviceID != nil {
				rule.DeviceGroupID = nil
			}
		}
		if in.DeviceGroupID.Set {
			rule.DeviceGroupID = in.DeviceGroupID.ptr()
			if rule.DeviceGroupID != nil {
				rule.DeviceID = nil
			}
		}
		if in.ControlRoleID.Set {
			rule.ControlRoleID = in.ControlRoleID.ptr()
		}
		if in.Action != nil {
			rule.Action = *in.Action
		}
		if in.Priority != nil {
			rule.Priority = *in.Priority
		}
		if in.Enabled != nil {
			rule.Enabled = *in.Enabled
		}
		return tx.ConnectionACLs.Update(ctx, rule)
	})
	if errors.Is(err, store.ErrNotFound) {
		httpx.NotFound(w, r)
		return
	}
	if err != nil {
		a.writeStoreErr(w, r, "update acl rule", err)
		return
	}
	row, err := a.d.Store.ConnectionACLs.Get(ctx, id)
	if err != nil {
		a.fail500(w, r, "update acl rule", err)
		return
	}
	a.audit(ctx, r, "acl.update", "acl", idStr(id), map[string]any{"name": row.Name})
	httpx.WriteJSON(w, http.StatusOK, newConnectionACLDTO(row))
}

func (a *resources) handleDeleteACL(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	id, ok := pathID(w, r, "acl_id")
	if !ok {
		return
	}
	rule, err := a.d.Store.ConnectionACLs.Get(ctx, id)
	if errors.Is(err, store.ErrNotFound) {
		httpx.NotFound(w, r)
		return
	}
	if err != nil {
		a.fail500(w, r, "delete acl rule", err)
		return
	}
	if err := a.d.Store.ConnectionACLs.Delete(ctx, id); err != nil {
		if errors.Is(err, store.ErrNotFound) {
			httpx.NotFound(w, r)
			return
		}
		a.writeStoreErr(w, r, "delete acl rule", err)
		return
	}
	a.audit(ctx, r, "acl.delete", "acl", idStr(id), map[string]any{"name": rule.Name})
	w.WriteHeader(http.StatusNoContent)
}

type aclTestRequest struct {
	UserID   int64 `json:"user_id"`
	DeviceID int64 `json:"device_id"`
}

type aclTestResultDTO struct {
	Decision      store.ACLDecision `json:"decision"`
	MatchedRule   *connectionACLDTO `json:"matched_rule"`
	EffectiveRole *controlRoleDTO   `json:"effective_role"`
}

func (a *resources) handleTestACL(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	var in aclTestRequest
	if !decodeJSON(w, r, &in) {
		return
	}
	if in.UserID <= 0 || in.DeviceID <= 0 {
		validationFailed(w, "invalid test request", map[string]string{
			"user_id":   "must be positive",
			"device_id": "must be positive",
		})
		return
	}

	decision, matchedRule, effectiveRole, err := a.d.Store.ConnectionACLs.Evaluate(ctx, in.UserID, in.DeviceID)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			validationFailed(w, "entity not found", map[string]string{"error": err.Error()})
			return
		}
		a.fail500(w, r, "evaluate acl", err)
		return
	}

	res := aclTestResultDTO{
		Decision: decision,
	}
	if matchedRule != nil {
		dto := newConnectionACLDTO(matchedRule)
		res.MatchedRule = &dto
	}
	if effectiveRole != nil {
		dto := newControlRoleDTO(effectiveRole)
		res.EffectiveRole = &dto
	}

	httpx.WriteJSON(w, http.StatusOK, res)
}
