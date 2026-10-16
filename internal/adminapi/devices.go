package adminapi

import (
	"context"
	"encoding/csv"
	"errors"
	"log/slog"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/bropines/rdpanel/internal/events"
	"github.com/bropines/rdpanel/internal/httpx"
	"github.com/bropines/rdpanel/internal/store"
)

// deviceDTO is components.schemas.Device.
type deviceDTO struct {
	ID              int64        `json:"id"`
	RustdeskID      string       `json:"rustdesk_id"`
	UUID            *string      `json:"uuid"`
	Hostname        string       `json:"hostname"`
	OS              string       `json:"os"`
	CPU             string       `json:"cpu"`
	Memory          string       `json:"memory"`
	OSUsername      string       `json:"os_username"`
	ClientVersion   string       `json:"client_version"`
	Alias           string       `json:"alias"`
	Note            string       `json:"note"`
	OwnerUserID     *int64       `json:"owner_user_id"`
	DeviceGroupID   *int64       `json:"device_group_id"`
	Owner           *userRefDTO  `json:"owner"`
	DeviceGroup     *groupRefDTO `json:"device_group"`
	OSFamily        string       `json:"os_family"`
	Online          bool         `json:"online"`
	OnlineSince     *time.Time   `json:"online_since"`
	LastSeenAt      *time.Time   `json:"last_seen_at"`
	LastSeenIP      *string      `json:"last_seen_ip"`
	LastHeartbeatAt *time.Time   `json:"last_heartbeat_at"`
	SysinfoAt       *time.Time   `json:"sysinfo_at"`
	CreatedAt       time.Time    `json:"created_at"`
	UpdatedAt       time.Time    `json:"updated_at"`
}

// deviceDetailDTO is components.schemas.DeviceDetail.
type deviceDetailDTO struct {
	deviceDTO
	RecentConnections []deviceConnDTO `json:"recent_connections"`
	AddressBooks      []groupRefDTO   `json:"address_books"`
}

// deviceConnDTO is components.schemas.ConnectionAudit.
type deviceConnDTO struct {
	ID                   int64      `json:"id"`
	GUID                 string     `json:"guid"`
	DeviceID             *int64     `json:"device_id"`
	DeviceRustdeskID     string     `json:"device_rustdesk_id"`
	DeviceAlias          *string    `json:"device_alias"`
	ControllerRustdeskID *string    `json:"controller_rustdesk_id"`
	ControllerName       *string    `json:"controller_name"`
	ControllerIP         *string    `json:"controller_ip"`
	ConnType             int        `json:"conn_type"`
	SessionID            *string    `json:"session_id"`
	Note                 *string    `json:"note"`
	StartedAt            time.Time  `json:"started_at"`
	EndedAt              *time.Time `json:"ended_at"`
}

// presenceOf returns the live presence of d (heartbeat fallback without a
// presence reader).
func (a *resources) presenceOf(d *store.Device, now time.Time) (bool, *time.Time) {
	if a.d.Presence != nil {
		online, since, known := a.d.Presence.DevicePresence(d.RustdeskID)
		if !known {
			return false, nil
		}
		return online, &since
	}
	online := d.LastHeartbeatAt != nil && !d.LastHeartbeatAt.Before(now.Add(-heartbeatOnlineWindow))
	return online, nil
}

// onlineSet fills the presence inputs of a device search.
func (a *resources) onlineSet(p *store.DeviceSearch) {
	if a.d.Presence != nil {
		ids := a.d.Presence.OnlineIDs()
		if ids == nil {
			ids = []string{}
		}
		p.OnlineIDs = ids
		return
	}
	p.HeartbeatSince = a.d.Store.Now().Add(-heartbeatOnlineWindow)
}

// deviceDTOs converts devices, resolving owner refs in one query.
func (a *resources) deviceDTOs(ctx context.Context, devs []*store.Device) ([]deviceDTO, error) {
	var owners []int64
	for _, d := range devs {
		if d.OwnerUserID != nil && !slices.Contains(owners, *d.OwnerUserID) {
			owners = append(owners, *d.OwnerUserID)
		}
	}
	refs, err := a.d.Store.Users.Refs(ctx, owners)
	if err != nil {
		return nil, err
	}
	now := a.d.Store.Now()
	out := make([]deviceDTO, 0, len(devs))
	for _, d := range devs {
		out = append(out, a.newDeviceDTO(d, refs, now))
	}
	return out, nil
}

func (a *resources) newDeviceDTO(d *store.Device, refs map[int64]store.UserRef, now time.Time) deviceDTO {
	o := deviceDTO{ID: d.ID, RustdeskID: d.RustdeskID, UUID: strPtr(d.UUID), Hostname: d.Hostname, OS: d.OS,
		CPU: d.CPU, Memory: d.Memory, OSUsername: d.OSUsername, ClientVersion: d.ClientVersion, Alias: d.Alias,
		Note: d.Note, OwnerUserID: d.OwnerUserID, DeviceGroupID: d.DeviceGroupID, OSFamily: store.OSFamily(d.OS),
		LastSeenAt: d.LastSeenAt, LastSeenIP: strPtr(d.LastSeenIP), LastHeartbeatAt: d.LastHeartbeatAt,
		SysinfoAt: d.SysinfoAt, CreatedAt: d.CreatedAt, UpdatedAt: d.UpdatedAt}
	if d.OwnerUserID != nil {
		ref, ok := refs[*d.OwnerUserID]
		if !ok {
			ref = store.UserRef{ID: *d.OwnerUserID, Username: d.OwnerUsername}
		}
		r := newUserRef(ref)
		o.Owner = &r
	}
	if d.DeviceGroupID != nil {
		o.DeviceGroup = &groupRefDTO{ID: *d.DeviceGroupID, Name: d.DeviceGroupName}
	}
	o.Online, o.OnlineSince = a.presenceOf(d, now)
	return o
}

// deviceFilter parses the shared device list/export filters and applies the
// caller's scope (users only see their own devices).
func (a *resources) deviceFilter(q *query, user *store.User) store.DeviceSearch {
	p := store.DeviceSearch{Query: q.search(), Online: q.boolean("online"), Version: q.str("version"),
		SeenAfter: q.time("seen_after")}
	p.OSFamily = q.enum("os", store.OSFamilies...)
	switch owner := q.str("owner"); owner {
	case "":
	case "me":
		p.OwnerUserID = &user.ID
	case "none":
		p.OwnerNone = true
	default:
		if id, ok := idParam(owner); ok {
			p.OwnerUserID = &id
		} else {
			q.fail("owner", "must be a user id, me or none")
		}
	}
	switch g := q.str("device_group"); g {
	case "":
	case "none":
		p.GroupNone = true
	default:
		if id, ok := idParam(g); ok {
			p.DeviceGroupID = &id
		} else {
			q.fail("device_group", "must be a device group id or none")
		}
	}
	if !user.IsAdmin() {
		if p.OwnerNone || (p.OwnerUserID != nil && *p.OwnerUserID != user.ID) {
			none := int64(0) // matches nothing
			p.OwnerUserID, p.OwnerNone = &none, false
		} else {
			p.OwnerUserID = &user.ID
		}
	}
	a.onlineSet(&p)
	return p
}

var deviceSorts = []string{"rustdesk_id", "hostname", "alias", "os", "client_version", "last_seen_at", "created_at", "online"}

func (a *resources) handleListDevices(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	q := newQuery(r)
	pg := q.paging()
	p := a.deviceFilter(q, CurrentUser(ctx))
	p.Sort, p.Desc = q.sort("-last_seen_at", deviceSorts...)
	if !q.ok(w) {
		return
	}
	p.Limit, p.Offset = pg.Size, pg.offset()
	devs, total, err := a.d.Store.Devices.Search(ctx, p)
	if err != nil {
		a.fail500(w, r, "list devices", err)
		return
	}
	out, err := a.deviceDTOs(ctx, devs)
	if err != nil {
		a.fail500(w, r, "list devices", err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, newListPage(out, total, pg))
}

type facetDTO struct {
	Value string `json:"value"`
	Count int    `json:"count"`
}

type deviceFacetsDTO struct {
	Total    int        `json:"total"`
	OS       []facetDTO `json:"os"`
	Versions []facetDTO `json:"versions"`
}

func facetDTOs(in []store.Facet) []facetDTO {
	out := make([]facetDTO, 0, len(in))
	for _, f := range in {
		out = append(out, facetDTO{Value: f.Value, Count: f.Count})
	}
	return out
}

func (a *resources) handleDeviceFacets(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	user := CurrentUser(ctx)
	var p store.DeviceSearch
	if !user.IsAdmin() {
		p.OwnerUserID = &user.ID
	}
	total, oses, versions, err := a.d.Store.Devices.Facets(ctx, p)
	if err != nil {
		a.fail500(w, r, "device facets", err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, deviceFacetsDTO{Total: total, OS: facetDTOs(oses), Versions: facetDTOs(versions)})
}

// csvSafe neutralises spreadsheet formula injection: cells starting with
// = + - @ (or a tab / carriage return) are prefixed with a single quote.
func csvSafe(s string) string {
	if s != "" && strings.ContainsRune("=+-@\t\r", rune(s[0])) {
		return "'" + s
	}
	return s
}

func fmtTime(t *time.Time) string {
	if t == nil {
		return ""
	}
	return t.UTC().Format(time.RFC3339)
}

func (a *resources) handleExportDevices(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	q := newQuery(r)
	p := a.deviceFilter(q, CurrentUser(ctx))
	p.Sort = "rustdesk_id"
	if !q.ok(w) {
		return
	}
	now := a.d.Store.Now()
	h := w.Header()
	h.Set("Content-Type", "text/csv; charset=utf-8")
	h.Set("Content-Disposition", `attachment; filename="devices-`+now.UTC().Format("2006-01-02")+`.csv"`)
	h.Set("Cache-Control", "no-store")
	h.Set("X-Content-Type-Options", "nosniff")
	cw := csv.NewWriter(w)
	_ = cw.Write([]string{"rustdesk_id", "alias", "hostname", "os", "client_version", "owner", "group", "online",
		"last_seen_at", "last_seen_ip", "note"})
	rc := http.NewResponseController(w)
	n := 0
	err := a.d.Store.Devices.SearchEach(ctx, p, func(d *store.Device) error {
		online, _ := a.presenceOf(d, now)
		rec := []string{d.RustdeskID, d.Alias, d.Hostname, d.OS, d.ClientVersion, d.OwnerUsername, d.DeviceGroupName,
			strconv.FormatBool(online), fmtTime(d.LastSeenAt), d.LastSeenIP, d.Note}
		for i := range rec {
			rec[i] = csvSafe(rec[i])
		}
		if err := cw.Write(rec); err != nil {
			return err
		}
		if n++; n%500 == 0 {
			cw.Flush()
			_ = rc.Flush()
		}
		return cw.Error()
	})
	cw.Flush()
	if err != nil {
		// Headers are already sent; the truncated body is the best we can do.
		a.d.Log.ErrorContext(ctx, "export devices", slog.Any("err", err))
	}
}

// loadDevice loads {device_id} visible to the caller (404 otherwise).
func (a *resources) loadDevice(w http.ResponseWriter, r *http.Request) *store.Device {
	id, ok := pathID(w, r, "device_id")
	if !ok {
		return nil
	}
	d, err := a.d.Store.Devices.Get(r.Context(), id)
	if errors.Is(err, store.ErrNotFound) {
		httpx.NotFound(w, r)
		return nil
	}
	if err != nil {
		a.fail500(w, r, "load device", err)
		return nil
	}
	if u := CurrentUser(r.Context()); !u.IsAdmin() && (d.OwnerUserID == nil || *d.OwnerUserID != u.ID) {
		httpx.NotFound(w, r)
		return nil
	}
	return d
}

func (a *resources) handleGetDevice(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	d := a.loadDevice(w, r)
	if d == nil {
		return
	}
	dtos, err := a.deviceDTOs(ctx, []*store.Device{d})
	if err != nil {
		a.fail500(w, r, "get device", err)
		return
	}
	out := deviceDetailDTO{deviceDTO: dtos[0], RecentConnections: []deviceConnDTO{}, AddressBooks: []groupRefDTO{}}
	conns, err := a.d.Store.ConnectionAudits.List(ctx, store.ClientAuditFilter{DeviceID: d.RustdeskID, Limit: 10})
	if err != nil {
		a.fail500(w, r, "get device", err)
		return
	}
	for _, c := range conns {
		out.RecentConnections = append(out.RecentConnections, deviceConnDTO{ID: c.ID, GUID: c.GUID, DeviceID: &d.ID,
			DeviceRustdeskID: c.DeviceID, DeviceAlias: strPtr(d.Alias), ControllerRustdeskID: strPtr(c.ControllerRustdeskID),
			ControllerName: strPtr(c.ControllerName), ControllerIP: strPtr(c.ControllerIP), ConnType: c.ConnType,
			SessionID: strPtr(c.SessionID), Note: strPtr(c.Note), StartedAt: c.StartedAt, EndedAt: c.EndedAt})
	}
	var visibleTo *int64
	if u := CurrentUser(ctx); !u.IsAdmin() {
		visibleTo = &u.ID
	}
	books, err := a.d.Store.AddressBooks.BooksContaining(ctx, d.RustdeskID, visibleTo)
	if err != nil {
		a.fail500(w, r, "get device", err)
		return
	}
	for _, b := range books {
		name := b.Name
		if name == "" && b.IsPersonal {
			name = "Personal (" + b.OwnerUsername + ")"
		}
		out.AddressBooks = append(out.AddressBooks, groupRefDTO{ID: b.ID, Name: name})
	}
	httpx.WriteJSON(w, http.StatusOK, out)
}

// deviceUpdate is components.schemas.DeviceUpdate.
type deviceUpdate struct {
	Alias         *string    `json:"alias"`
	Note          *string    `json:"note"`
	OwnerUserID   opt[int64] `json:"owner_user_id"`
	DeviceGroupID opt[int64] `json:"device_group_id"`
}

// checkRefs validates optional owner / device group references.
func (a *resources) checkRefs(ctx context.Context, owner, group *int64, fields map[string]string) error {
	if owner != nil {
		if _, err := a.d.Store.Users.GetByID(ctx, *owner); errors.Is(err, store.ErrNotFound) {
			fields["owner_user_id"] = "user does not exist"
		} else if err != nil {
			return err
		}
	}
	if group != nil {
		if _, err := a.d.Store.DeviceGroups.Get(ctx, *group); errors.Is(err, store.ErrNotFound) {
			fields["device_group_id"] = "device group does not exist"
		} else if err != nil {
			return err
		}
	}
	return nil
}

func (a *resources) handleUpdateDevice(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	user := CurrentUser(ctx)
	d := a.loadDevice(w, r)
	if d == nil {
		return
	}
	var in deviceUpdate
	if !decodeJSON(w, r, &in) {
		return
	}
	if !user.IsAdmin() && (in.OwnerUserID.Set || in.DeviceGroupID.Set) {
		forbidden(w, "only administrators can change the owner or group")
		return
	}
	fields := map[string]string{}
	if in.Alias != nil && runes(*in.Alias) > 128 {
		fields["alias"] = "must be at most 128 characters"
	}
	if in.Note != nil && runes(*in.Note) > 4096 {
		fields["note"] = "must be at most 4096 characters"
	}
	var owner, group *int64
	if in.OwnerUserID.Set {
		owner = in.OwnerUserID.ptr()
	}
	if in.DeviceGroupID.Set {
		group = in.DeviceGroupID.ptr()
	}
	if err := a.checkRefs(ctx, owner, group, fields); err != nil {
		a.fail500(w, r, "update device", err)
		return
	}
	if len(fields) > 0 {
		validationFailed(w, "invalid device update", fields)
		return
	}
	ch := diff{}
	prevOwner := d.OwnerUserID
	if in.Alias != nil {
		ch.val("alias", d.Alias, *in.Alias)
		d.Alias = *in.Alias
	}
	if in.Note != nil {
		ch.text("note", d.Note, *in.Note)
		d.Note = *in.Note
	}
	if in.OwnerUserID.Set {
		if !eqID(d.OwnerUserID, owner) {
			ch["owner_user_id"] = []any{ptrVal(d.OwnerUserID), ptrVal(owner)}
		}
		d.OwnerUserID = owner
	}
	if in.DeviceGroupID.Set {
		if !eqID(d.DeviceGroupID, group) {
			ch["device_group_id"] = []any{ptrVal(d.DeviceGroupID), ptrVal(group)}
		}
		d.DeviceGroupID = group
	}
	if err := a.d.Store.Devices.Update(ctx, d); err != nil {
		a.writeStoreErr(w, r, "update device", err)
		return
	}
	fresh, err := a.d.Store.Devices.Get(ctx, d.ID)
	if err != nil {
		a.fail500(w, r, "update device", err)
		return
	}
	dtos, err := a.deviceDTOs(ctx, []*store.Device{fresh})
	if err != nil {
		a.fail500(w, r, "update device", err)
		return
	}
	if len(ch) > 0 {
		a.audit(ctx, r, "device.update", "device", idStr(d.ID), map[string]any{"rustdesk_id": d.RustdeskID, "changes": ch})
	}
	a.publishDevice(dtos[0], prevOwner)
	httpx.WriteJSON(w, http.StatusOK, dtos[0])
}

// publishDevice emits device.updated to admins and the (previous) owner.
func (a *resources) publishDevice(d deviceDTO, prevOwner *int64) {
	if a.d.Events == nil {
		return
	}
	aud := events.Admins
	var ids []int64
	for _, id := range []*int64{d.OwnerUserID, prevOwner} {
		if id != nil && !slices.Contains(ids, *id) {
			ids = append(ids, *id)
		}
	}
	if len(ids) > 0 {
		aud = events.Audience{UserIDs: ids}
	}
	a.d.Events.Publish(events.DeviceUpdated, d, aud)
}

// deleteDevice revokes the client tokens bound to d and deletes it.
func deleteDevice(ctx context.Context, tx *store.Store, d *store.Device) (int64, error) {
	n, err := tx.AuthTokens.RevokeForDevice(ctx, d.UUID, "device deleted")
	if err != nil {
		return 0, err
	}
	return n, tx.Devices.Delete(ctx, d.ID)
}

func (a *resources) handleDeleteDevice(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	d := a.loadDevice(w, r)
	if d == nil {
		return
	}
	var revoked int64
	err := a.d.Store.Tx(ctx, func(tx *store.Store) error {
		var err error
		revoked, err = deleteDevice(ctx, tx, d)
		return err
	})
	if err != nil {
		a.writeStoreErr(w, r, "delete device", err)
		return
	}
	a.audit(ctx, r, "device.delete", "device", idStr(d.ID), map[string]any{"rustdesk_id": d.RustdeskID,
		"revoked_tokens": revoked})
	w.WriteHeader(http.StatusNoContent)
}

func (a *resources) handleKickDevice(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	d := a.loadDevice(w, r)
	if d == nil {
		return
	}
	if a.d.Kicks == nil {
		httpx.WriteError(w, http.StatusServiceUnavailable, "server_unavailable", "kick queue unavailable")
		return
	}
	a.d.Kicks.Kick(d.RustdeskID)
	a.audit(ctx, r, "device.kick", "device", idStr(d.ID), map[string]any{"rustdesk_id": d.RustdeskID})
	w.WriteHeader(http.StatusAccepted)
}

// deviceBulkRequest is components.schemas.DeviceBulkRequest.
type deviceBulkRequest struct {
	Action        string     `json:"action"`
	DeviceIDs     []int64    `json:"device_ids"`
	OwnerUserID   opt[int64] `json:"owner_user_id"`
	DeviceGroupID opt[int64] `json:"device_group_id"`
}

func (a *resources) handleBulkDevices(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	var in deviceBulkRequest
	if !decodeJSON(w, r, &in) {
		return
	}
	fields := map[string]string{}
	switch in.Action {
	case "assign_owner", "assign_group", "kick", "delete":
	default:
		fields["action"] = "must be one of assign_owner, assign_group, kick, delete"
	}
	if len(in.DeviceIDs) == 0 || len(in.DeviceIDs) > 1000 {
		fields["device_ids"] = "must contain 1 to 1000 ids"
	}
	var owner, group *int64
	if in.Action == "assign_owner" && in.OwnerUserID.Set {
		owner = in.OwnerUserID.ptr()
	}
	if in.Action == "assign_group" && in.DeviceGroupID.Set {
		group = in.DeviceGroupID.ptr()
	}
	if err := a.checkRefs(ctx, owner, group, fields); err != nil {
		a.fail500(w, r, "bulk devices", err)
		return
	}
	if len(fields) > 0 {
		validationFailed(w, "invalid bulk request", fields)
		return
	}
	if in.Action == "kick" && a.d.Kicks == nil {
		httpx.WriteError(w, http.StatusServiceUnavailable, "server_unavailable", "kick queue unavailable")
		return
	}
	ids := slices.Compact(slices.Sorted(slices.Values(in.DeviceIDs)))
	res := bulkResultDTO{}
	var (
		changed  []*store.Device
		prev     = map[int64]*int64{}
		rustIDs  []string
		revoked  int64
		affected []int64
	)
	err := a.d.Store.Tx(ctx, func(tx *store.Store) error {
		res, changed, rustIDs, affected, revoked = bulkResultDTO{}, nil, nil, nil, 0
		for _, id := range ids {
			d, err := tx.Devices.Get(ctx, id)
			if errors.Is(err, store.ErrNotFound) {
				res.Errors = append(res.Errors, bulkErrorDTO{ID: id, Code: "not_found", Message: "device not found"})
				continue
			}
			if err != nil {
				return err
			}
			switch in.Action {
			case "assign_owner":
				if eqID(d.OwnerUserID, owner) {
					res.Skipped++
					continue
				}
				prev[d.ID], d.OwnerUserID = d.OwnerUserID, owner
				err = tx.Devices.Update(ctx, d)
				changed = append(changed, d)
			case "assign_group":
				if eqID(d.DeviceGroupID, group) {
					res.Skipped++
					continue
				}
				prev[d.ID], d.DeviceGroupID = d.OwnerUserID, group
				err = tx.Devices.Update(ctx, d)
				changed = append(changed, d)
			case "kick":
				rustIDs = append(rustIDs, d.RustdeskID)
			case "delete":
				var n int64
				n, err = deleteDevice(ctx, tx, d)
				revoked += n
			}
			if err != nil {
				return err
			}
			res.Affected++
			affected = append(affected, d.ID)
		}
		return nil
	})
	if err != nil {
		a.writeStoreErr(w, r, "bulk devices", err)
		return
	}
	for _, id := range rustIDs {
		a.d.Kicks.Kick(id)
	}
	if len(changed) > 0 {
		fresh := make([]*store.Device, 0, len(changed))
		for _, d := range changed {
			if f, err := a.d.Store.Devices.Get(ctx, d.ID); err == nil {
				fresh = append(fresh, f)
			}
		}
		if dtos, err := a.deviceDTOs(ctx, fresh); err == nil {
			for _, d := range dtos {
				a.publishDevice(d, prev[d.ID])
			}
		}
	}
	if res.Affected > 0 {
		details := map[string]any{"count": res.Affected, "device_ids": firstN(affected, 100)}
		switch in.Action {
		case "assign_owner":
			details["owner_user_id"] = ptrVal(owner)
		case "assign_group":
			details["device_group_id"] = ptrVal(group)
		case "delete":
			details["revoked_tokens"] = revoked
		}
		a.audit(ctx, r, "device.bulk_"+in.Action, "device", "", details)
	}
	httpx.WriteJSON(w, http.StatusOK, res)
}

func firstN[T any](s []T, n int) []T {
	if len(s) > n {
		return s[:n]
	}
	return s
}

// fail500 logs err and writes 500.
func (a *resources) fail500(w http.ResponseWriter, r *http.Request, what string, err error) {
	a.d.Log.ErrorContext(r.Context(), what, slog.Any("err", err))
	internalError(w)
}

// writeStoreErr maps repository errors: not found 404, conflict 409,
// broken reference 400, anything else 500.
func (a *resources) writeStoreErr(w http.ResponseWriter, r *http.Request, what string, err error) {
	switch {
	case errors.Is(err, store.ErrNotFound):
		httpx.NotFound(w, r)
	case errors.Is(err, store.ErrConflict):
		conflict(w, "conflict", "a resource with this name already exists")
	case errors.Is(err, store.ErrReferenced), errors.Is(err, store.ErrInvalid):
		badRequest(w, "invalid reference or value")
	default:
		a.fail500(w, r, what, err)
	}
}
