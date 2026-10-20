package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/bropines/rdpanel/internal/db"
)

// OS families reported by the admin API (components.schemas.OsFamily).
const (
	OSWindows = "windows"
	OSMacOS   = "macos"
	OSLinux   = "linux"
	OSAndroid = "android"
	OSIOS     = "ios"
	OSOther   = "other"
)

// osFamilies maps the free-form sysinfo "os" string to a family: the first
// family with a matching (lower-case substring) pattern wins.
var osFamilies = []struct {
	family   string
	patterns []string
}{
	{OSWindows, []string{"windows"}},
	{OSMacOS, []string{"macos", "mac os", "darwin", "os x"}},
	{OSAndroid, []string{"android"}},
	{OSIOS, []string{"ios", "iphone", "ipad"}},
	{OSLinux, []string{"linux", "ubuntu", "debian", "fedora", "centos", "red hat", "arch", "manjaro", "opensuse",
		"alpine", "raspbian", "gentoo", "nixos"}},
}

// OSFamilies lists every valid family value.
var OSFamilies = []string{OSWindows, OSMacOS, OSLinux, OSAndroid, OSIOS, OSOther}

// OSFamily classifies a sysinfo "os" string (same rules as the SQL filter).
func OSFamily(os string) string {
	l := strings.ToLower(os)
	for _, f := range osFamilies {
		for _, p := range f.patterns {
			if strings.Contains(l, p) {
				return f.family
			}
		}
	}
	return OSOther
}

// osFamilyExpr is the SQL equivalent of OSFamily for column col. The
// patterns are constants without LIKE metacharacters or quotes.
func osFamilyExpr(col string) string {
	var b strings.Builder
	b.WriteString("(CASE")
	for _, f := range osFamilies {
		b.WriteString(" WHEN ")
		for i, p := range f.patterns {
			if i > 0 {
				b.WriteString(" OR ")
			}
			fmt.Fprintf(&b, "lower(%s) LIKE '%%%s%%'", col, p)
		}
		fmt.Fprintf(&b, " THEN '%s'", f.family)
	}
	fmt.Fprintf(&b, " ELSE '%s' END)", OSOther)
	return b.String()
}

// DeviceSearch filters, sorts and pages Devices.Search.
type DeviceSearch struct {
	// OwnerUserID limits to devices owned by this user; OwnerNone to unassigned devices.
	OwnerUserID *int64
	OwnerNone   bool
	// DeviceGroupID limits to one device group; GroupNone to devices without a group.
	DeviceGroupID *int64
	GroupNone     bool
	// StrategyID limits to one strategy; StrategyNone to devices without a direct strategy override.
	StrategyID   *int64
	StrategyNone bool
	// OSFamily is one of OSFamilies ("" = any).
	OSFamily string
	// Version is an exact client version ("" = any).
	Version   string
	SeenAfter *time.Time
	// Query matches RustDesk ID, hostname, alias, OS username or note (substring).
	Query string
	// Online filters by live presence (nil = any).
	Online *bool
	// OnlineIDs is the set of RustDesk IDs currently online (live presence).
	// When nil, a device counts as online when its last heartbeat is at or
	// after HeartbeatSince (fallback when presence is unavailable).
	OnlineIDs      []string
	HeartbeatSince time.Time
	// Sort is one of DeviceSortKeys ("" = last_seen_at); Desc reverses it.
	Sort   string
	Desc   bool
	Limit  int
	Offset int
}

// DeviceSortKeys are the accepted DeviceSearch.Sort values.
var DeviceSortKeys = []string{"rustdesk_id", "hostname", "alias", "os", "client_version", "last_seen_at", "created_at", "online"}

func (p *DeviceSearch) onlineExpr(dl db.Dialect) (string, []any) {
	if p.OnlineIDs != nil {
		raw, _ := json.Marshal(p.OnlineIDs)
		return "(" + dl.InJSONArray("d.rustdesk_id", "") + ")", []any{string(raw)}
	}
	return "(coalesce(d.last_heartbeat_at, 0) >= ?)", []any{toMs(p.HeartbeatSince)}
}

func (p *DeviceSearch) where(dl db.Dialect) (string, []any) {
	var (
		conds []string
		args  []any
	)
	add := func(c string, a ...any) { conds, args = append(conds, c), append(args, a...) }
	switch {
	case p.OwnerNone:
		add("d.owner_user_id IS NULL")
	case p.OwnerUserID != nil:
		add("d.owner_user_id = ?", *p.OwnerUserID)
	}
	switch {
	case p.GroupNone:
		add("d.device_group_id IS NULL")
	case p.DeviceGroupID != nil:
		add("d.device_group_id = ?", *p.DeviceGroupID)
	}
	switch {
	case p.StrategyNone:
		add("d.strategy_id IS NULL")
	case p.StrategyID != nil:
		add("d.strategy_id = ?", *p.StrategyID)
	}
	if p.OSFamily != "" {
		add(osFamilyExpr("d.os")+" = ?", p.OSFamily)
	}
	if p.Version != "" {
		add("d.client_version = ?", p.Version)
	}
	if p.SeenAfter != nil {
		add("d.last_seen_at >= ?", toMs(*p.SeenAfter))
	}
	if q := strings.TrimSpace(p.Query); q != "" {
		like := "%" + escapeLike(q) + "%"
		add(`(d.rustdesk_id LIKE ? ESCAPE '\' OR d.hostname LIKE ? ESCAPE '\' OR d.alias LIKE ? ESCAPE '\'
			OR d.os_username LIKE ? ESCAPE '\' OR d.note LIKE ? ESCAPE '\')`, like, like, like, like, like)
	}
	if p.Online != nil {
		expr, a := p.onlineExpr(dl)
		if *p.Online {
			add(expr, a...)
		} else {
			add("NOT "+expr, a...)
		}
	}
	if len(conds) == 0 {
		return "", nil
	}
	return " WHERE " + strings.Join(conds, " AND "), args
}

func (p *DeviceSearch) orderBy(dl db.Dialect) (string, []any, error) {
	var (
		expr string
		args []any
	)
	switch p.Sort {
	case "", "last_seen_at":
		expr = "coalesce(d.last_seen_at, 0)"
	case "rustdesk_id":
		expr = "d.rustdesk_id"
	case "hostname":
		expr = "d.hostname COLLATE NOCASE"
	case "alias":
		expr = "d.alias COLLATE NOCASE"
	case "os":
		expr = "d.os COLLATE NOCASE"
	case "client_version":
		expr = "d.client_version"
	case "created_at":
		expr = "d.created_at"
	case "online":
		expr, args = p.onlineExpr(dl)
	default:
		return "", nil, fmt.Errorf("%w: sort %q", ErrInvalid, p.Sort)
	}
	dir := " ASC"
	if p.Desc {
		dir = " DESC"
	}
	return " ORDER BY " + expr + dir + ", d.id" + dir, args, nil
}

// Search returns a page of devices matching p and the total match count.
func (r *Devices) Search(ctx context.Context, p DeviceSearch) ([]*Device, int, error) {
	cond, args := p.where(db.DialectOf(r.q))
	order, oargs, err := p.orderBy(db.DialectOf(r.q))
	if err != nil {
		return nil, 0, err
	}
	var total int
	if err := r.q.QueryRowContext(ctx, `SELECT count(*) FROM devices d`+cond, args...).Scan(&total); err != nil {
		return nil, 0, mapErr(err)
	}
	args = append(append(args, oargs...), clampLimit(p.Limit, 50, 1000), max(p.Offset, 0))
	rows, err := r.q.QueryContext(ctx, `SELECT `+deviceColumns+deviceFrom+cond+order+` LIMIT ? OFFSET ?`, args...)
	if err != nil {
		return nil, 0, mapErr(err)
	}
	defer rows.Close()
	var out []*Device
	for rows.Next() {
		d, err := scanDevice(rows)
		if err != nil {
			return nil, 0, err
		}
		out = append(out, d)
	}
	return out, total, mapErr(rows.Err())
}

// SearchEach calls fn for every device matching p (Limit/Offset ignored), in
// p's order, stopping at the first error.
func (r *Devices) SearchEach(ctx context.Context, p DeviceSearch, fn func(*Device) error) error {
	cond, args := p.where(db.DialectOf(r.q))
	order, oargs, err := p.orderBy(db.DialectOf(r.q))
	if err != nil {
		return err
	}
	rows, err := r.q.QueryContext(ctx, `SELECT `+deviceColumns+deviceFrom+cond+order, append(args, oargs...)...)
	if err != nil {
		return mapErr(err)
	}
	defer rows.Close()
	for rows.Next() {
		d, err := scanDevice(rows)
		if err != nil {
			return err
		}
		if err := fn(d); err != nil {
			return err
		}
	}
	return mapErr(rows.Err())
}

// Count returns the number of devices matching p.
func (r *Devices) Count(ctx context.Context, p DeviceSearch) (int, error) {
	cond, args := p.where(db.DialectOf(r.q))
	var n int
	err := r.q.QueryRowContext(ctx, `SELECT count(*) FROM devices d`+cond, args...).Scan(&n)
	return n, mapErr(err)
}

// Facet is one value of a facet with its device count.
type Facet struct {
	Value string
	Count int
}

// Facets returns the OS families and client versions (non-empty) of the
// devices matching p, by count descending.
func (r *Devices) Facets(ctx context.Context, p DeviceSearch) (total int, oses, versions []Facet, err error) {
	cond, args := p.where(db.DialectOf(r.q))
	if err = r.q.QueryRowContext(ctx, `SELECT count(*) FROM devices d`+cond, args...).Scan(&total); err != nil {
		return 0, nil, nil, mapErr(err)
	}
	collect := func(expr, extra string) ([]Facet, error) {
		c := cond
		if extra != "" {
			if c == "" {
				c = " WHERE " + extra
			} else {
				c += " AND " + extra
			}
		}
		rows, err := r.q.QueryContext(ctx, `SELECT `+expr+` AS v, count(*) AS n FROM devices d`+c+`
			GROUP BY v ORDER BY n DESC, v`, args...)
		if err != nil {
			return nil, mapErr(err)
		}
		defer rows.Close()
		out := []Facet{}
		for rows.Next() {
			var f Facet
			if err := rows.Scan(&f.Value, &f.Count); err != nil {
				return nil, mapErr(err)
			}
			out = append(out, f)
		}
		return out, mapErr(rows.Err())
	}
	if oses, err = collect(osFamilyExpr("d.os"), ""); err != nil {
		return 0, nil, nil, err
	}
	if versions, err = collect("d.client_version", "d.client_version <> ''"); err != nil {
		return 0, nil, nil, err
	}
	return total, oses, versions, nil
}

// ---- device groups (admin CRUD) ----

// DeviceGroupRow is a device group with its device count.
type DeviceGroupRow struct {
	*DeviceGroup
	DeviceCount  int
	StrategyName string
}

const deviceGroupRowSelect = `SELECT g.id, g.name, g.note, g.strategy_id, g.created_at, g.updated_at,
	(SELECT count(*) FROM devices d WHERE d.device_group_id = g.id) AS device_count,
	coalesce(st.name, '') AS strategy_name
	FROM device_groups g
	LEFT JOIN client_strategies st ON st.id = g.strategy_id`

func scanDeviceGroupRow(row interface{ Scan(...any) error }) (*DeviceGroupRow, error) {
	var (
		g                    DeviceGroup
		stratID              sql.NullInt64
		n                    int
		createdAt, updatedAt int64
		stratName            string
	)
	if err := row.Scan(&g.ID, &g.Name, &g.Note, &stratID, &createdAt, &updatedAt, &n, &stratName); err != nil {
		return nil, mapErr(err)
	}
	g.StrategyID = ptrID(stratID)
	g.CreatedAt, g.UpdatedAt = fromMs(createdAt), fromMs(updatedAt)
	return &DeviceGroupRow{DeviceGroup: &g, DeviceCount: n, StrategyName: stratName}, nil
}

// GroupSearch filters, sorts and pages group listings.
type GroupSearch struct {
	Query string
	// Sort: name, created_at, or count (member/device count).
	Sort   string
	Desc   bool
	Limit  int
	Offset int
}

func (p GroupSearch) clause(countCol string) (string, []any, string, error) {
	cond, args := "", []any(nil)
	if q := strings.TrimSpace(p.Query); q != "" {
		cond, args = ` WHERE (g.name LIKE ? ESCAPE '\' OR g.note LIKE ? ESCAPE '\')`, []any{"%" + escapeLike(q) + "%", "%" + escapeLike(q) + "%"}
	}
	var expr string
	switch p.Sort {
	case "", "name":
		expr = "g.name COLLATE NOCASE"
	case "created_at":
		expr = "g.created_at"
	case "count":
		expr = countCol
	default:
		return "", nil, "", fmt.Errorf("%w: sort %q", ErrInvalid, p.Sort)
	}
	dir := " ASC"
	if p.Desc {
		dir = " DESC"
	}
	return cond, args, " ORDER BY " + expr + dir + ", g.id" + dir, nil
}

// Search returns a page of device groups with device counts.
func (r *DeviceGroups) Search(ctx context.Context, p GroupSearch) ([]*DeviceGroupRow, int, error) {
	cond, args, order, err := p.clause("device_count")
	if err != nil {
		return nil, 0, err
	}
	var total int
	if err := r.q.QueryRowContext(ctx, `SELECT count(*) FROM device_groups g`+cond, args...).Scan(&total); err != nil {
		return nil, 0, mapErr(err)
	}
	rows, err := r.q.QueryContext(ctx, deviceGroupRowSelect+cond+order+` LIMIT ? OFFSET ?`,
		append(args, clampLimit(p.Limit, 50, 1000), max(p.Offset, 0))...)
	if err != nil {
		return nil, 0, mapErr(err)
	}
	defer rows.Close()
	out := []*DeviceGroupRow{}
	for rows.Next() {
		g, err := scanDeviceGroupRow(rows)
		if err != nil {
			return nil, 0, err
		}
		out = append(out, g)
	}
	return out, total, mapErr(rows.Err())
}

// GetRow returns one device group with its device count.
func (r *DeviceGroups) GetRow(ctx context.Context, id int64) (*DeviceGroupRow, error) {
	return scanDeviceGroupRow(r.q.QueryRowContext(ctx, deviceGroupRowSelect+` WHERE g.id = ?`, id))
}

// Update writes name, note and strategy_id.
func (r *DeviceGroups) Update(ctx context.Context, g *DeviceGroup) error {
	g.UpdatedAt = r.now()
	return expectOne(r.q.ExecContext(ctx, `UPDATE device_groups SET name = ?, note = ?, strategy_id = ?, updated_at = ? WHERE id = ?`,
		strings.TrimSpace(g.Name), g.Note, nullID(g.StrategyID), toMs(g.UpdatedAt), g.ID))
}

// Delete removes a device group; its devices' device_group_id becomes NULL.
func (r *DeviceGroups) Delete(ctx context.Context, id int64) error {
	return expectOne(r.q.ExecContext(ctx, `DELETE FROM device_groups WHERE id = ?`, id))
}
