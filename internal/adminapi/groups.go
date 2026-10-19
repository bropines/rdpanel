package adminapi

import (
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/bropines/rdpanel/internal/httpx"
	"github.com/bropines/rdpanel/internal/store"
)

// userGroupDTO is components.schemas.UserGroup.
type userGroupDTO struct {
	ID          int64     `json:"id"`
	Name        string    `json:"name"`
	Visibility  string    `json:"visibility"`
	Note        string    `json:"note"`
	MemberCount int       `json:"member_count"`
	CreatedAt   time.Time `json:"created_at"`
	UpdatedAt   time.Time `json:"updated_at"`
}

func newUserGroupDTO(g *store.UserGroupRow) userGroupDTO {
	return userGroupDTO{ID: g.ID, Name: g.Name, Visibility: string(g.Visibility), Note: g.Note,
		MemberCount: g.MemberCount, CreatedAt: g.CreatedAt, UpdatedAt: g.UpdatedAt}
}

// deviceGroupDTO is components.schemas.DeviceGroup.
type deviceGroupDTO struct {
	ID          int64        `json:"id"`
	Name        string       `json:"name"`
	Note        string       `json:"note"`
	StrategyID  *int64       `json:"strategy_id"`
	Strategy    *groupRefDTO `json:"strategy"`
	DeviceCount int          `json:"device_count"`
	CreatedAt   time.Time    `json:"created_at"`
	UpdatedAt   time.Time    `json:"updated_at"`
}

func newDeviceGroupDTO(g *store.DeviceGroupRow) deviceGroupDTO {
	dto := deviceGroupDTO{
		ID:          g.ID,
		Name:        g.Name,
		Note:        g.Note,
		StrategyID:  g.StrategyID,
		DeviceCount: g.DeviceCount,
		CreatedAt:   g.CreatedAt,
		UpdatedAt:   g.UpdatedAt,
	}
	if g.StrategyID != nil {
		dto.Strategy = &groupRefDTO{ID: *g.StrategyID, Name: g.StrategyName}
	}
	return dto
}

// groupInput is UserGroupInput/Patch and DeviceGroupInput/Patch.
type groupInput struct {
	Name       *string    `json:"name"`
	Visibility *string    `json:"visibility"`
	Note       *string    `json:"note"`
	StrategyID opt[int64] `json:"strategy_id"`
}

func (in *groupInput) validate(create, withVisibility bool) map[string]string {
	f := map[string]string{}
	if in.Name != nil {
		*in.Name = strings.TrimSpace(*in.Name)
		if *in.Name == "" || runes(*in.Name) > 128 {
			f["name"] = "must be 1-128 characters"
		}
	} else if create {
		f["name"] = "required"
	}
	if in.Visibility != nil {
		if !withVisibility {
			f["visibility"] = "unknown field"
		} else if *in.Visibility != string(store.VisibilitySelf) && *in.Visibility != string(store.VisibilityShared) {
			f["visibility"] = "must be self or shared"
		}
	}
	if in.Note != nil && runes(*in.Note) > 4096 {
		f["note"] = "must be at most 4096 characters"
	}
	return f
}

func groupSearch(w http.ResponseWriter, r *http.Request, countSort string) (store.GroupSearch, paging, bool) {
	q := newQuery(r)
	pg := q.paging()
	p := store.GroupSearch{Query: q.search(), Limit: pg.Size, Offset: pg.offset()}
	p.Sort, p.Desc = q.sort("name", "name", countSort, "created_at")
	if p.Sort == countSort {
		p.Sort = "count"
	}
	return p, pg, q.ok(w)
}

func (a *resources) groupConflict(w http.ResponseWriter, r *http.Request, what string, err error) {
	if errors.Is(err, store.ErrConflict) {
		conflict(w, "conflict", "a group with this name already exists")
		return
	}
	a.writeStoreErr(w, r, what, err)
}

// ---- user groups ----

func (a *resources) handleListUserGroups(w http.ResponseWriter, r *http.Request) {
	p, pg, ok := groupSearch(w, r, "member_count")
	if !ok {
		return
	}
	rows, total, err := a.d.Store.UserGroups.Search(r.Context(), p)
	if err != nil {
		a.fail500(w, r, "list user groups", err)
		return
	}
	out := make([]userGroupDTO, 0, len(rows))
	for _, g := range rows {
		out = append(out, newUserGroupDTO(g))
	}
	httpx.WriteJSON(w, http.StatusOK, newListPage(out, total, pg))
}

func (a *resources) handleCreateUserGroup(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	var in groupInput
	if !decodeJSON(w, r, &in) {
		return
	}
	if f := in.validate(true, true); len(f) > 0 {
		validationFailed(w, "invalid user group", f)
		return
	}
	g := &store.UserGroup{Name: *in.Name, Visibility: store.VisibilitySelf}
	if in.Visibility != nil {
		g.Visibility = store.GroupVisibility(*in.Visibility)
	}
	if in.Note != nil {
		g.Note = *in.Note
	}
	if err := a.d.Store.UserGroups.Create(ctx, g); err != nil {
		a.groupConflict(w, r, "create user group", err)
		return
	}
	row, err := a.d.Store.UserGroups.GetRow(ctx, g.ID)
	if err != nil {
		a.fail500(w, r, "create user group", err)
		return
	}
	a.audit(ctx, r, "user_group.create", "user_group", idStr(g.ID), map[string]any{"name": g.Name,
		"visibility": string(g.Visibility)})
	httpx.WriteJSON(w, http.StatusCreated, newUserGroupDTO(row))
}

func (a *resources) handleUpdateUserGroup(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	id, ok := pathID(w, r, "group_id")
	if !ok {
		return
	}
	var in groupInput
	if !decodeJSON(w, r, &in) {
		return
	}
	if f := in.validate(false, true); len(f) > 0 {
		validationFailed(w, "invalid user group", f)
		return
	}
	ch := diff{}
	err := a.d.Store.Tx(ctx, func(tx *store.Store) error {
		g, err := tx.UserGroups.Get(ctx, id)
		if err != nil {
			return err
		}
		old := *g
		if in.Name != nil {
			g.Name = *in.Name
		}
		if in.Visibility != nil {
			g.Visibility = store.GroupVisibility(*in.Visibility)
		}
		if in.Note != nil {
			g.Note = *in.Note
		}
		ch.val("name", old.Name, g.Name)
		ch.val("visibility", string(old.Visibility), string(g.Visibility))
		ch.text("note", old.Note, g.Note)
		return tx.UserGroups.Update(ctx, g)
	})
	if err != nil {
		a.groupConflict(w, r, "update user group", err)
		return
	}
	row, err := a.d.Store.UserGroups.GetRow(ctx, id)
	if err != nil {
		a.fail500(w, r, "update user group", err)
		return
	}
	if len(ch) > 0 {
		a.audit(ctx, r, "user_group.update", "user_group", idStr(id), map[string]any{"name": row.Name, "changes": ch})
	}
	httpx.WriteJSON(w, http.StatusOK, newUserGroupDTO(row))
}

func (a *resources) handleDeleteUserGroup(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	id, ok := pathID(w, r, "group_id")
	if !ok {
		return
	}
	g, err := a.d.Store.UserGroups.Get(ctx, id)
	if err == nil {
		err = a.d.Store.UserGroups.Delete(ctx, id)
	}
	if err != nil {
		a.writeStoreErr(w, r, "delete user group", err)
		return
	}
	a.audit(ctx, r, "user_group.delete", "user_group", idStr(id), map[string]any{"name": g.Name})
	w.WriteHeader(http.StatusNoContent)
}

// ---- device groups ----

func (a *resources) handleListDeviceGroups(w http.ResponseWriter, r *http.Request) {
	p, pg, ok := groupSearch(w, r, "device_count")
	if !ok {
		return
	}
	rows, total, err := a.d.Store.DeviceGroups.Search(r.Context(), p)
	if err != nil {
		a.fail500(w, r, "list device groups", err)
		return
	}
	out := make([]deviceGroupDTO, 0, len(rows))
	for _, g := range rows {
		out = append(out, newDeviceGroupDTO(g))
	}
	httpx.WriteJSON(w, http.StatusOK, newListPage(out, total, pg))
}

func (a *resources) handleCreateDeviceGroup(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	var in groupInput
	if !decodeJSON(w, r, &in) {
		return
	}
	if f := in.validate(true, false); len(f) > 0 {
		validationFailed(w, "invalid device group", f)
		return
	}
	g := &store.DeviceGroup{Name: *in.Name}
	if in.Note != nil {
		g.Note = *in.Note
	}
	if in.StrategyID.Set {
		if strat := in.StrategyID.ptr(); strat != nil {
			if _, err := a.d.Store.ClientStrategies.Get(ctx, *strat); err != nil {
				validationFailed(w, "invalid device group", map[string]string{"strategy_id": "strategy does not exist"})
				return
			}
			g.StrategyID = strat
		}
	}
	if err := a.d.Store.DeviceGroups.Create(ctx, g); err != nil {
		a.groupConflict(w, r, "create device group", err)
		return
	}
	row, err := a.d.Store.DeviceGroups.GetRow(ctx, g.ID)
	if err != nil {
		a.fail500(w, r, "create device group", err)
		return
	}
	a.audit(ctx, r, "device_group.create", "device_group", idStr(g.ID), map[string]any{"name": g.Name})
	httpx.WriteJSON(w, http.StatusCreated, newDeviceGroupDTO(row))
}

func (a *resources) handleUpdateDeviceGroup(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	id, ok := pathID(w, r, "group_id")
	if !ok {
		return
	}
	var in groupInput
	if !decodeJSON(w, r, &in) {
		return
	}
	if f := in.validate(false, false); len(f) > 0 {
		validationFailed(w, "invalid device group", f)
		return
	}
	ch := diff{}
	err := a.d.Store.Tx(ctx, func(tx *store.Store) error {
		g, err := tx.DeviceGroups.Get(ctx, id)
		if err != nil {
			return err
		}
		old := *g
		if in.Name != nil {
			g.Name = *in.Name
		}
		if in.Note != nil {
			g.Note = *in.Note
		}
		if in.StrategyID.Set {
			strat := in.StrategyID.ptr()
			if strat != nil {
				if _, err := tx.ClientStrategies.Get(ctx, *strat); err != nil {
					return errors.New("strategy does not exist")
				}
			}
			g.StrategyID = strat
			if !eqID(old.StrategyID, g.StrategyID) {
				ch["strategy_id"] = []any{ptrVal(old.StrategyID), ptrVal(g.StrategyID)}
			}
		}
		ch.val("name", old.Name, g.Name)
		ch.text("note", old.Note, g.Note)
		return tx.DeviceGroups.Update(ctx, g)
	})
	if err != nil {
		a.groupConflict(w, r, "update device group", err)
		return
	}
	row, err := a.d.Store.DeviceGroups.GetRow(ctx, id)
	if err != nil {
		a.fail500(w, r, "update device group", err)
		return
	}
	if len(ch) > 0 {
		a.audit(ctx, r, "device_group.update", "device_group", idStr(id), map[string]any{"name": row.Name, "changes": ch})
	}
	httpx.WriteJSON(w, http.StatusOK, newDeviceGroupDTO(row))
}

func (a *resources) handleDeleteDeviceGroup(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	id, ok := pathID(w, r, "group_id")
	if !ok {
		return
	}
	g, err := a.d.Store.DeviceGroups.Get(ctx, id)
	if err == nil {
		err = a.d.Store.DeviceGroups.Delete(ctx, id)
	}
	if err != nil {
		a.writeStoreErr(w, r, "delete device group", err)
		return
	}
	a.audit(ctx, r, "device_group.delete", "device_group", idStr(id), map[string]any{"name": g.Name})
	w.WriteHeader(http.StatusNoContent)
}
