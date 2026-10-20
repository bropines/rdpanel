package store

import (
	"context"
	"database/sql"
	"strings"
	"time"

	"github.com/bropines/rdpanel/internal/db"
)

// Device is a row of devices (one per RustDesk ID), with the owner's username
// and the device group's name joined in for reads.
type Device struct {
	ID            int64
	RustdeskID    string
	UUID          string // "" = NULL (not yet reported)
	Hostname      string
	OS            string
	CPU           string
	Memory        string
	OSUsername    string
	ClientVersion string
	Alias         string
	Note          string
	OwnerUserID   *int64
	DeviceGroupID *int64
	StrategyID    *int64
	// PublicKey is the device's Ed25519 public key (32 bytes) when known.
	PublicKey          []byte
	LastSeenAt         *time.Time
	LastSeenIP         string
	LastHeartbeatAt    *time.Time
	SysinfoAt          *time.Time
	SysinfoRequestedAt *time.Time
	CreatedAt          time.Time
	UpdatedAt          time.Time

	// Read-only joins.
	OwnerUsername   string
	DeviceGroupName string
	StrategyName    string
}

// SysinfoPending reports whether the server asked for a sysinfo re-upload
// that has not arrived yet (or the device never uploaded sysinfo).
func (d *Device) SysinfoPending() bool {
	if d.SysinfoAt == nil {
		return true
	}
	return d.SysinfoRequestedAt != nil && d.SysinfoRequestedAt.After(*d.SysinfoAt)
}

// Devices is the devices repository.
type Devices struct {
	q   db.Querier
	now func() time.Time
}

const deviceColumns = `d.id, d.rustdesk_id, d.uuid, d.hostname, d.os, d.cpu, d.memory, d.os_username,
	d.client_version, d.alias, d.note, d.owner_user_id, d.device_group_id, d.strategy_id, d.pk, d.last_seen_at, d.last_seen_ip,
	d.last_heartbeat_at, d.sysinfo_at, d.sysinfo_requested_at, d.created_at, d.updated_at,
	coalesce(u.username, ''), coalesce(g.name, ''), coalesce(st.name, '')`

const deviceFrom = ` FROM devices d
	LEFT JOIN users u ON u.id = d.owner_user_id
	LEFT JOIN device_groups g ON g.id = d.device_group_id
	LEFT JOIN client_strategies st ON st.id = d.strategy_id`

func scanDevice(row interface{ Scan(...any) error }) (*Device, error) {
	var (
		d                       Device
		uuid, ip                sql.NullString
		owner, group, strat     sql.NullInt64
		seen, hb, sysAt, sysReq sql.NullInt64
		createdAt, updatedAt    int64
	)
	err := row.Scan(&d.ID, &d.RustdeskID, &uuid, &d.Hostname, &d.OS, &d.CPU, &d.Memory, &d.OSUsername,
		&d.ClientVersion, &d.Alias, &d.Note, &owner, &group, &strat, &d.PublicKey, &seen, &ip,
		&hb, &sysAt, &sysReq, &createdAt, &updatedAt, &d.OwnerUsername, &d.DeviceGroupName, &d.StrategyName)
	if err != nil {
		return nil, mapErr(err)
	}
	d.UUID, d.LastSeenIP = uuid.String, ip.String
	d.OwnerUserID, d.DeviceGroupID, d.StrategyID = ptrID(owner), ptrID(group), ptrID(strat)
	d.LastSeenAt, d.LastHeartbeatAt = ptrMs(seen), ptrMs(hb)
	d.SysinfoAt, d.SysinfoRequestedAt = ptrMs(sysAt), ptrMs(sysReq)
	d.CreatedAt, d.UpdatedAt = fromMs(createdAt), fromMs(updatedAt)
	return &d, nil
}

// Create inserts d; ID, CreatedAt and UpdatedAt are set.
func (r *Devices) Create(ctx context.Context, d *Device) error {
	now := r.now()
	if d.CreatedAt.IsZero() {
		d.CreatedAt = now
	}
	d.UpdatedAt = now
	return mapErr(r.q.QueryRowContext(ctx, `INSERT INTO devices (rustdesk_id, uuid, hostname, os, cpu, memory,
		os_username, client_version, alias, note, owner_user_id, device_group_id, strategy_id, pk, last_seen_at, last_seen_ip,
		last_heartbeat_at, sysinfo_at, sysinfo_requested_at, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?) RETURNING id`,
		d.RustdeskID, nullStr(d.UUID), d.Hostname, d.OS, d.CPU, d.Memory, d.OSUsername, d.ClientVersion,
		d.Alias, d.Note, nullID(d.OwnerUserID), nullID(d.DeviceGroupID), nullID(d.StrategyID), nullBytes(d.PublicKey),
		nullMs(d.LastSeenAt), nullStr(d.LastSeenIP), nullMs(d.LastHeartbeatAt), nullMs(d.SysinfoAt),
		nullMs(d.SysinfoRequestedAt), toMs(d.CreatedAt), toMs(d.UpdatedAt)).Scan(&d.ID))
}

// Get returns the device with id.
func (r *Devices) Get(ctx context.Context, id int64) (*Device, error) {
	return scanDevice(r.q.QueryRowContext(ctx, `SELECT `+deviceColumns+deviceFrom+` WHERE d.id = ?`, id))
}

// GetByRustdeskID returns the device with the given RustDesk ID.
func (r *Devices) GetByRustdeskID(ctx context.Context, rustdeskID string) (*Device, error) {
	return scanDevice(r.q.QueryRowContext(ctx, `SELECT `+deviceColumns+deviceFrom+` WHERE d.rustdesk_id = ?`,
		rustdeskID))
}

// Sysinfo holds the fields reported by POST /api/sysinfo.
type Sysinfo struct {
	// UUID replaces the stored uuid (callers enforce the change rules).
	UUID          string
	Hostname      string
	OS            string
	CPU           string
	Memory        string
	ClientVersion string
	// OSUsername is nil when the client omitted it (kept unchanged).
	OSUsername *string
}

// UpdateSysinfo stores a sysinfo upload and marks sysinfo_at.
func (r *Devices) UpdateSysinfo(ctx context.Context, id int64, s Sysinfo) error {
	now := toMs(r.now())
	return expectOne(r.q.ExecContext(ctx, `UPDATE devices SET uuid = ?, hostname = ?, os = ?, cpu = ?, memory = ?,
		client_version = ?, os_username = coalesce(?, os_username), sysinfo_at = ?, updated_at = ? WHERE id = ?`,
		nullStr(s.UUID), s.Hostname, s.OS, s.CPU, s.Memory, s.ClientVersion, s.OSUsername, now, now, id))
}

// TouchHeartbeat records a heartbeat from ip. To bound write volume (clients
// beat every 3 s while controlled) the row is only written when the previous
// heartbeat is older than minInterval or the IP changed. It reports whether a
// write happened.
func (r *Devices) TouchHeartbeat(ctx context.Context, id int64, ip string, minInterval time.Duration) (bool, error) {
	now := r.now()
	res, err := r.q.ExecContext(ctx, `UPDATE devices SET last_heartbeat_at = ?, last_seen_at = ?, last_seen_ip = ?
		WHERE id = ? AND (last_heartbeat_at IS NULL OR last_heartbeat_at <= ? OR last_seen_ip IS DISTINCT FROM ?)`,
		toMs(now), toMs(now), nullStr(ip), id, toMs(now.Add(-minInterval)), nullStr(ip))
	if err != nil {
		return false, mapErr(err)
	}
	n, err := res.RowsAffected()
	return n > 0, err
}

// TouchSeen records a presence observation (hbbs online poll) without
// touching the heartbeat timestamp.
func (r *Devices) TouchSeen(ctx context.Context, id int64, at time.Time) error {
	ms := toMs(at)
	return expectOne(r.q.ExecContext(ctx, `UPDATE devices SET last_seen_at = CASE
		WHEN coalesce(last_seen_at, 0) > ? THEN coalesce(last_seen_at, 0) ELSE ? END
		WHERE id = ?`, ms, ms, id))
}

// RequestSysinfo asks the device to re-upload sysinfo on its next heartbeat.
func (r *Devices) RequestSysinfo(ctx context.Context, id int64) error {
	return expectOne(r.q.ExecContext(ctx, `UPDATE devices SET sysinfo_requested_at = ? WHERE id = ?`,
		toMs(r.now()), id))
}

// Update writes the administrative fields of d: alias, note, owner, device
// group, hostname and OS username.
func (r *Devices) Update(ctx context.Context, d *Device) error {
	d.UpdatedAt = r.now()
	return expectOne(r.q.ExecContext(ctx, `UPDATE devices SET alias = ?, note = ?, owner_user_id = ?,
		device_group_id = ?, strategy_id = ?, hostname = ?, os_username = ?, updated_at = ? WHERE id = ?`,
		d.Alias, d.Note, nullID(d.OwnerUserID), nullID(d.DeviceGroupID), nullID(d.StrategyID), d.Hostname, d.OSUsername,
		toMs(d.UpdatedAt), d.ID))
}

// SetIdentity sets the uuid and (when non-nil) the public key of a device.
func (r *Devices) SetIdentity(ctx context.Context, id int64, uuid string, pk []byte) error {
	now := toMs(r.now())
	return expectOne(r.q.ExecContext(ctx, `UPDATE devices SET uuid = ?, pk = coalesce(?, pk), updated_at = ?
		WHERE id = ?`, nullStr(uuid), nullBytes(pk), now, id))
}

// BindOwner sets the owner of the device with rustdeskID whose uuid matches
// (client login). A device without a stored uuid is not bound. It reports
// whether a device was bound.
func (r *Devices) BindOwner(ctx context.Context, rustdeskID, uuid string, userID int64) (bool, error) {
	if rustdeskID == "" || uuid == "" {
		return false, nil
	}
	res, err := r.q.ExecContext(ctx, `UPDATE devices SET owner_user_id = ?, updated_at = ?
		WHERE rustdesk_id = ? AND uuid = ? AND owner_user_id IS DISTINCT FROM ?`, userID, toMs(r.now()), rustdeskID, uuid, userID)
	if err != nil {
		return false, mapErr(err)
	}
	n, err := res.RowsAffected()
	return n > 0, err
}

// UnbindOwner clears the owner of the device with rustdeskID/uuid when it is
// userID (client logout).
func (r *Devices) UnbindOwner(ctx context.Context, rustdeskID, uuid string, userID int64) error {
	if rustdeskID == "" || uuid == "" {
		return nil
	}
	_, err := r.q.ExecContext(ctx, `UPDATE devices SET owner_user_id = NULL, updated_at = ?
		WHERE rustdesk_id = ? AND uuid = ? AND owner_user_id = ?`, toMs(r.now()), rustdeskID, uuid, userID)
	return mapErr(err)
}

// GuessOwner returns the user of the newest active client token issued to the
// device (rustdeskID, uuid), used to bind a device created by its first
// sysinfo after the user already logged in. ErrNotFound when none.
func (r *Devices) GuessOwner(ctx context.Context, rustdeskID, uuid string) (int64, error) {
	var id int64
	err := r.q.QueryRowContext(ctx, `SELECT user_id FROM auth_tokens
		WHERE kind = 'client' AND device_rustdesk_id = ? AND device_uuid = ? AND revoked_at IS NULL
		  AND expires_at > ? ORDER BY created_at DESC, id DESC LIMIT 1`, rustdeskID, uuid, toMs(r.now())).Scan(&id)
	return id, mapErr(err)
}

// Delete removes a device.
func (r *Devices) Delete(ctx context.Context, id int64) error {
	return expectOne(r.q.ExecContext(ctx, `DELETE FROM devices WHERE id = ?`, id))
}

// DeviceScope restricts device listings. The zero value matches every device.
type DeviceScope struct {
	// OwnerUserID limits to devices owned by this user.
	OwnerUserID *int64
	// OwnerGroupID limits to devices owned by members of this user group.
	OwnerGroupID *int64
}

func (s DeviceScope) where() (string, []any) {
	var (
		conds []string
		args  []any
	)
	if s.OwnerUserID != nil {
		conds, args = append(conds, "d.owner_user_id = ?"), append(args, *s.OwnerUserID)
	}
	if s.OwnerGroupID != nil {
		conds = append(conds, "d.owner_user_id IN (SELECT id FROM users WHERE user_group_id = ?)")
		args = append(args, *s.OwnerGroupID)
	}
	if len(conds) == 0 {
		return "", nil
	}
	return " WHERE " + strings.Join(conds, " AND "), args
}

// ListDevicesParams filters and pages List.
type ListDevicesParams struct {
	Scope DeviceScope
	// DeviceGroupID limits to one device group.
	DeviceGroupID *int64
	// Query matches RustDesk ID, hostname, alias or OS username (substring).
	Query  string
	Limit  int
	Offset int
}

// List returns a page of devices ordered by id and the total match count.
func (r *Devices) List(ctx context.Context, p ListDevicesParams) ([]*Device, int, error) {
	cond, args := p.Scope.where()
	add := func(c string, a ...any) {
		if cond == "" {
			cond = " WHERE " + c
		} else {
			cond += " AND " + c
		}
		args = append(args, a...)
	}
	if p.DeviceGroupID != nil {
		add("d.device_group_id = ?", *p.DeviceGroupID)
	}
	if q := strings.TrimSpace(p.Query); q != "" {
		like := "%" + escapeLike(q) + "%"
		add(`(d.rustdesk_id LIKE ? ESCAPE '\' OR d.hostname LIKE ? ESCAPE '\' OR d.alias LIKE ? ESCAPE '\'
			OR d.os_username LIKE ? ESCAPE '\')`, like, like, like, like)
	}
	var total int
	if err := r.q.QueryRowContext(ctx, `SELECT count(*) FROM devices d`+cond, args...).Scan(&total); err != nil {
		return nil, 0, mapErr(err)
	}
	rows, err := r.q.QueryContext(ctx, `SELECT `+deviceColumns+deviceFrom+cond+` ORDER BY d.id LIMIT ? OFFSET ?`,
		append(args, clampLimit(p.Limit, 100, 1000), max(p.Offset, 0))...)
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

func nullBytes(b []byte) any {
	if len(b) == 0 {
		return nil
	}
	return b
}

// DeviceGroup is a row of device_groups.
type DeviceGroup struct {
	ID         int64
	Name       string
	Note       string
	StrategyID *int64
	CreatedAt  time.Time
	UpdatedAt  time.Time
}

// DeviceGroups is the device_groups repository (read side plus Create).
type DeviceGroups struct {
	q   db.Querier
	now func() time.Time
}

func scanDeviceGroup(row interface{ Scan(...any) error }) (*DeviceGroup, error) {
	var (
		g                    DeviceGroup
		stratID              sql.NullInt64
		createdAt, updatedAt int64
	)
	if err := row.Scan(&g.ID, &g.Name, &g.Note, &stratID, &createdAt, &updatedAt); err != nil {
		return nil, mapErr(err)
	}
	g.StrategyID = ptrID(stratID)
	g.CreatedAt, g.UpdatedAt = fromMs(createdAt), fromMs(updatedAt)
	return &g, nil
}

// Create inserts g (honouring a preset ID for legacy import).
func (r *DeviceGroups) Create(ctx context.Context, g *DeviceGroup) error {
	now := r.now()
	if g.CreatedAt.IsZero() {
		g.CreatedAt = now
	}
	g.UpdatedAt = now
	return insertWithID(ctx, r.q, "device_groups", "name, note, strategy_id, created_at, updated_at", &g.ID,
		strings.TrimSpace(g.Name), g.Note, nullID(g.StrategyID), toMs(g.CreatedAt), toMs(g.UpdatedAt))
}

// Get returns the group with id.
func (r *DeviceGroups) Get(ctx context.Context, id int64) (*DeviceGroup, error) {
	return scanDeviceGroup(r.q.QueryRowContext(ctx,
		`SELECT id, name, note, strategy_id, created_at, updated_at FROM device_groups WHERE id = ?`, id))
}

// GetByName returns the group named name (case-insensitive).
func (r *DeviceGroups) GetByName(ctx context.Context, name string) (*DeviceGroup, error) {
	return scanDeviceGroup(r.q.QueryRowContext(ctx, `SELECT id, name, note, strategy_id, created_at, updated_at
		FROM device_groups WHERE name = ? COLLATE NOCASE`, strings.TrimSpace(name)))
}

// List returns a page of device groups visible within scope, ordered by name,
// and the total count. With a non-zero scope only groups containing at least
// one device in scope are returned.
func (r *DeviceGroups) List(ctx context.Context, scope DeviceScope, limit, offset int) ([]*DeviceGroup, int, error) {
	cond, args := scope.where()
	filter := ""
	if cond != "" {
		filter = ` WHERE g.id IN (SELECT d.device_group_id FROM devices d` + cond + ` AND d.device_group_id IS NOT NULL)`
	}
	var total int
	if err := r.q.QueryRowContext(ctx, `SELECT count(*) FROM device_groups g`+filter, args...).Scan(&total); err != nil {
		return nil, 0, mapErr(err)
	}
	rows, err := r.q.QueryContext(ctx, `SELECT g.id, g.name, g.note, g.strategy_id, g.created_at, g.updated_at
		FROM device_groups g`+filter+` ORDER BY g.name COLLATE NOCASE, g.id LIMIT ? OFFSET ?`,
		append(args, clampLimit(limit, 100, 1000), max(offset, 0))...)
	if err != nil {
		return nil, 0, mapErr(err)
	}
	defer rows.Close()
	var out []*DeviceGroup
	for rows.Next() {
		g, err := scanDeviceGroup(rows)
		if err != nil {
			return nil, 0, err
		}
		out = append(out, g)
	}
	return out, total, mapErr(rows.Err())
}
