package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/bropines/rdpanel/internal/db"
)

// ControlRole defines session capability permissions.
type ControlRole struct {
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

// ControlRoles repository.
type ControlRoles struct {
	q   db.Querier
	d   *db.DB
	now func() time.Time
}

func scanControlRole(row interface{ Scan(...any) error }) (*ControlRole, error) {
	var (
		cr                                                   ControlRole
		kbm, clip, file, audio, tunnel, term, restart, isDef int
		createdMs, updatedMs                                 int64
		ruleCount                                            sql.NullInt64
	)
	err := row.Scan(
		&cr.ID, &cr.Name, &cr.Note,
		&kbm, &clip, &file, &audio, &tunnel, &term, &restart,
		&isDef, &createdMs, &updatedMs, &ruleCount,
	)
	if err != nil {
		return nil, mapErr(err)
	}
	cr.AllowKeyboardMouse = kbm == 1
	cr.AllowClipboard = clip == 1
	cr.AllowFileTransfer = file == 1
	cr.AllowAudio = audio == 1
	cr.AllowTCPTunneling = tunnel == 1
	cr.AllowTerminal = term == 1
	cr.AllowRestart = restart == 1
	cr.IsDefault = isDef == 1
	cr.CreatedAt = fromMs(createdMs)
	cr.UpdatedAt = fromMs(updatedMs)
	if ruleCount.Valid {
		cr.RuleCount = int(ruleCount.Int64)
	}
	return &cr, nil
}

const controlRoleColsWithCount = `
	cr.id, cr.name, cr.note,
	cr.allow_keyboard_mouse, cr.allow_clipboard, cr.allow_file_transfer,
	cr.allow_audio, cr.allow_tcp_tunneling, cr.allow_terminal, cr.allow_restart,
	cr.is_default, cr.created_at, cr.updated_at,
	(SELECT count(*) FROM connection_acls WHERE control_role_id = cr.id) AS rule_count
`

// Create inserts a new control role.
func (r *ControlRoles) Create(ctx context.Context, cr *ControlRole) error {
	cr.Name = strings.TrimSpace(cr.Name)
	if cr.Name == "" || len(cr.Name) > 64 {
		return fmt.Errorf("%w: role name must be 1-64 characters", ErrInvalid)
	}
	now := toMs(r.now())
	cr.CreatedAt = fromMs(now)
	cr.UpdatedAt = fromMs(now)

	// One transaction, insert first: a failed insert (e.g. a duplicate name)
	// leaves the current default in place.
	return atomic(ctx, r.d, r.q, func(q db.Querier) error {
		err := q.QueryRowContext(ctx, `
			INSERT INTO control_roles (
				name, note,
				allow_keyboard_mouse, allow_clipboard, allow_file_transfer,
				allow_audio, allow_tcp_tunneling, allow_terminal, allow_restart,
				is_default, created_at, updated_at
			) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?) RETURNING id`,
			cr.Name, cr.Note,
			boolInt(cr.AllowKeyboardMouse), boolInt(cr.AllowClipboard), boolInt(cr.AllowFileTransfer),
			boolInt(cr.AllowAudio), boolInt(cr.AllowTCPTunneling), boolInt(cr.AllowTerminal), boolInt(cr.AllowRestart),
			boolInt(cr.IsDefault), now, now,
		).Scan(&cr.ID)
		if err != nil {
			return mapErr(err)
		}
		if cr.IsDefault {
			return clearOtherDefaults(ctx, q, "control_roles", cr.ID)
		}
		return nil
	})
}

// Get finds a role by ID.
func (r *ControlRoles) Get(ctx context.Context, id int64) (*ControlRole, error) {
	row := r.q.QueryRowContext(ctx, `SELECT `+controlRoleColsWithCount+` FROM control_roles cr WHERE cr.id = ?`, id)
	return scanControlRole(row)
}

// GetByName finds a role by Name.
func (r *ControlRoles) GetByName(ctx context.Context, name string) (*ControlRole, error) {
	row := r.q.QueryRowContext(ctx, `SELECT `+controlRoleColsWithCount+` FROM control_roles cr WHERE cr.name = ?`, name)
	return scanControlRole(row)
}

// GetDefault finds the default control role.
func (r *ControlRoles) GetDefault(ctx context.Context) (*ControlRole, error) {
	row := r.q.QueryRowContext(ctx, `SELECT `+controlRoleColsWithCount+` FROM control_roles cr WHERE cr.is_default = 1 LIMIT 1`)
	cr, err := scanControlRole(row)
	if err != nil && errors.Is(err, ErrNotFound) {
		// Fallback to first available role if no default is marked
		row = r.q.QueryRowContext(ctx, `SELECT `+controlRoleColsWithCount+` FROM control_roles cr ORDER BY cr.id ASC LIMIT 1`)
		return scanControlRole(row)
	}
	return cr, err
}

// List returns all control roles.
func (r *ControlRoles) List(ctx context.Context) ([]*ControlRole, error) {
	rows, err := r.q.QueryContext(ctx, `SELECT `+controlRoleColsWithCount+` FROM control_roles cr ORDER BY cr.id ASC`)
	if err != nil {
		return nil, mapErr(err)
	}
	defer rows.Close()

	var out []*ControlRole
	for rows.Next() {
		cr, err := scanControlRole(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, cr)
	}
	return out, rows.Err()
}

// Update modifies an existing role.
func (r *ControlRoles) Update(ctx context.Context, cr *ControlRole) error {
	cr.Name = strings.TrimSpace(cr.Name)
	if cr.Name == "" || len(cr.Name) > 64 {
		return fmt.Errorf("%w: role name must be 1-64 characters", ErrInvalid)
	}
	now := toMs(r.now())
	cr.UpdatedAt = fromMs(now)

	// One transaction, the row first: updating a missing id (or a duplicate
	// name) leaves the current default in place.
	return atomic(ctx, r.d, r.q, func(q db.Querier) error {
		if err := expectOne(q.ExecContext(ctx, `
			UPDATE control_roles SET
				name = ?, note = ?,
				allow_keyboard_mouse = ?, allow_clipboard = ?, allow_file_transfer = ?,
				allow_audio = ?, allow_tcp_tunneling = ?, allow_terminal = ?, allow_restart = ?,
				is_default = ?, updated_at = ?
			WHERE id = ?`,
			cr.Name, cr.Note,
			boolInt(cr.AllowKeyboardMouse), boolInt(cr.AllowClipboard), boolInt(cr.AllowFileTransfer),
			boolInt(cr.AllowAudio), boolInt(cr.AllowTCPTunneling), boolInt(cr.AllowTerminal), boolInt(cr.AllowRestart),
			boolInt(cr.IsDefault), now, cr.ID,
		)); err != nil {
			return err
		}
		if cr.IsDefault {
			return clearOtherDefaults(ctx, q, "control_roles", cr.ID)
		}
		return nil
	})
}

// SetDefault marks role id as default. An unknown id is ErrNotFound and
// changes nothing.
func (r *ControlRoles) SetDefault(ctx context.Context, id int64) error {
	now := toMs(r.now())
	return atomic(ctx, r.d, r.q, func(q db.Querier) error {
		if err := expectOne(q.ExecContext(ctx, `UPDATE control_roles SET is_default = 1, updated_at = ? WHERE id = ?`, now, id)); err != nil {
			return err
		}
		return clearOtherDefaults(ctx, q, "control_roles", id)
	})
}

// ErrRoleInUse is returned (wrapping ErrReferenced) when deleting a control
// role that rules still reference: deleting it would silently switch those
// rules to the default role.
var ErrRoleInUse = fmt.Errorf("%w: control role is referenced by connection rules", ErrReferenced)

// Delete removes role id. The default role (ErrInvalid) and a role that any
// rule references, enabled or not (ErrRoleInUse), cannot be deleted.
func (r *ControlRoles) Delete(ctx context.Context, id int64) error {
	return atomic(ctx, r.d, r.q, func(q db.Querier) error {
		cr, err := scanControlRole(q.QueryRowContext(ctx, `SELECT `+controlRoleColsWithCount+` FROM control_roles cr WHERE cr.id = ?`, id))
		if err != nil {
			return err
		}
		if cr.IsDefault {
			return fmt.Errorf("%w: cannot delete default control role", ErrInvalid)
		}
		if cr.RuleCount > 0 {
			return ErrRoleInUse
		}
		return expectOne(q.ExecContext(ctx, `DELETE FROM control_roles WHERE id = ?`, id))
	})
}

// ACLAction represents permission decision.
type ACLAction string

const (
	ACLActionAllow ACLAction = "allow"
	ACLActionDeny  ACLAction = "deny"
)

// ACLDecision represents the evaluated outcome.
type ACLDecision string

const (
	ACLDecisionAllow   ACLDecision = "allow"
	ACLDecisionDeny    ACLDecision = "deny"
	ACLDecisionOwner   ACLDecision = "owner"
	ACLDecisionNoRules ACLDecision = "no_rules"
)

// ConnectionACL is a rule in the access matrix.
type ConnectionACL struct {
	ID              int64     `json:"id"`
	Name            string    `json:"name"`
	Note            string    `json:"note"`
	UserID          *int64    `json:"user_id"`
	UserName        string    `json:"user_name,omitempty"`
	UserGroupID     *int64    `json:"user_group_id"`
	UserGroupName   string    `json:"user_group_name,omitempty"`
	DeviceID        *int64    `json:"device_id"`
	DeviceName      string    `json:"device_name,omitempty"`
	DeviceGroupID   *int64    `json:"device_group_id"`
	DeviceGroupName string    `json:"device_group_name,omitempty"`
	ControlRoleID   *int64    `json:"control_role_id"`
	ControlRoleName string    `json:"control_role_name,omitempty"`
	Action          ACLAction `json:"action"`
	Priority        int       `json:"priority"`
	Enabled         bool      `json:"enabled"`
	CreatedAt       time.Time `json:"created_at"`
	UpdatedAt       time.Time `json:"updated_at"`
}

// ConnectionACLs repository.
type ConnectionACLs struct {
	q   db.Querier
	now func() time.Time
}

func scanACL(row interface{ Scan(...any) error }) (*ConnectionACL, error) {
	var (
		a                                            ConnectionACL
		action                                       string
		en                                           int
		createdMs, updatedMs                         int64
		uID, ugID, dID, dgID, crID                   sql.NullInt64
		uName, ugName, dAlias, dHost, dgName, crName sql.NullString
	)
	err := row.Scan(
		&a.ID, &a.Name, &a.Note,
		&uID, &ugID, &dID, &dgID, &crID,
		&action, &a.Priority, &en,
		&createdMs, &updatedMs,
		&uName, &ugName, &dAlias, &dHost, &dgName, &crName,
	)
	if err != nil {
		return nil, mapErr(err)
	}
	if uID.Valid {
		a.UserID = &uID.Int64
	}
	if ugID.Valid {
		a.UserGroupID = &ugID.Int64
	}
	if dID.Valid {
		a.DeviceID = &dID.Int64
	}
	if dgID.Valid {
		a.DeviceGroupID = &dgID.Int64
	}
	if crID.Valid {
		a.ControlRoleID = &crID.Int64
	}
	if uName.Valid {
		a.UserName = uName.String
	}
	if ugName.Valid {
		a.UserGroupName = ugName.String
	}
	if dAlias.Valid && dAlias.String != "" {
		a.DeviceName = dAlias.String
	} else if dHost.Valid {
		a.DeviceName = dHost.String
	}
	if dgName.Valid {
		a.DeviceGroupName = dgName.String
	}
	if crName.Valid {
		a.ControlRoleName = crName.String
	}

	a.Action = ACLAction(action)
	a.Enabled = en == 1
	a.CreatedAt = fromMs(createdMs)
	a.UpdatedAt = fromMs(updatedMs)
	return &a, nil
}

const aclJoinedCols = `
	a.id, a.name, a.note,
	a.user_id, a.user_group_id, a.device_id, a.device_group_id, a.control_role_id,
	a.action, a.priority, a.enabled,
	a.created_at, a.updated_at,
	u.username, ug.name, d.alias, d.hostname, dg.name, cr.name
`

const aclJoins = `
	FROM connection_acls a
	LEFT JOIN users u ON u.id = a.user_id
	LEFT JOIN user_groups ug ON ug.id = a.user_group_id
	LEFT JOIN devices d ON d.id = a.device_id
	LEFT JOIN device_groups dg ON dg.id = a.device_group_id
	LEFT JOIN control_roles cr ON cr.id = a.control_role_id
`

// Create inserts a new ACL rule.
func (r *ConnectionACLs) Create(ctx context.Context, a *ConnectionACL) error {
	a.Name = strings.TrimSpace(a.Name)
	if a.Name == "" || len(a.Name) > 128 {
		return fmt.Errorf("%w: rule name must be 1-128 characters", ErrInvalid)
	}
	if a.Action != ACLActionAllow && a.Action != ACLActionDeny {
		return fmt.Errorf("%w: action must be allow or deny", ErrInvalid)
	}
	now := toMs(r.now())
	a.CreatedAt = fromMs(now)
	a.UpdatedAt = fromMs(now)

	err := r.q.QueryRowContext(ctx, `
		INSERT INTO connection_acls (
			name, note,
			user_id, user_group_id, device_id, device_group_id, control_role_id,
			action, priority, enabled, created_at, updated_at
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?) RETURNING id`,
		a.Name, a.Note,
		a.UserID, a.UserGroupID, a.DeviceID, a.DeviceGroupID, a.ControlRoleID,
		string(a.Action), a.Priority, boolInt(a.Enabled), now, now,
	).Scan(&a.ID)
	if err != nil {
		return mapErr(err)
	}
	return nil
}

// Get finds a rule by ID.
func (r *ConnectionACLs) Get(ctx context.Context, id int64) (*ConnectionACL, error) {
	row := r.q.QueryRowContext(ctx, `SELECT `+aclJoinedCols+aclJoins+` WHERE a.id = ?`, id)
	return scanACL(row)
}

// List returns all rules ordered by priority ASC, id ASC.
func (r *ConnectionACLs) List(ctx context.Context) ([]*ConnectionACL, error) {
	rows, err := r.q.QueryContext(ctx, `SELECT `+aclJoinedCols+aclJoins+` ORDER BY a.priority ASC, a.id ASC`)
	if err != nil {
		return nil, mapErr(err)
	}
	defer rows.Close()

	var out []*ConnectionACL
	for rows.Next() {
		a, err := scanACL(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

// Update modifies an existing ACL rule.
func (r *ConnectionACLs) Update(ctx context.Context, a *ConnectionACL) error {
	a.Name = strings.TrimSpace(a.Name)
	if a.Name == "" || len(a.Name) > 128 {
		return fmt.Errorf("%w: rule name must be 1-128 characters", ErrInvalid)
	}
	if a.Action != ACLActionAllow && a.Action != ACLActionDeny {
		return fmt.Errorf("%w: action must be allow or deny", ErrInvalid)
	}
	now := toMs(r.now())
	a.UpdatedAt = fromMs(now)

	return expectOne(r.q.ExecContext(ctx, `
		UPDATE connection_acls SET
			name = ?, note = ?,
			user_id = ?, user_group_id = ?, device_id = ?, device_group_id = ?, control_role_id = ?,
			action = ?, priority = ?, enabled = ?, updated_at = ?
		WHERE id = ?`,
		a.Name, a.Note,
		a.UserID, a.UserGroupID, a.DeviceID, a.DeviceGroupID, a.ControlRoleID,
		string(a.Action), a.Priority, boolInt(a.Enabled), now, a.ID,
	))
}

// Delete removes rule id.
func (r *ConnectionACLs) Delete(ctx context.Context, id int64) error {
	return expectOne(r.q.ExecContext(ctx, `DELETE FROM connection_acls WHERE id = ?`, id))
}

// ACLSubject is one (user, device) pair for DecideACL.
type ACLSubject struct {
	UserID        int64
	UserGroupID   *int64
	DeviceID      int64
	DeviceGroupID *int64
	// OwnerAccess: the user owns the device, or the device's owner is a
	// member of the user's "shared" user group. Such access is allowed unless
	// a rule denies it.
	OwnerAccess bool
}

// DecideACL is the connection decision for s under rules (in priority order;
// disabled rules are skipped): the first rule matching both the user and the
// device decides. Without a match, owner access is allowed and everything
// else denied (default deny), or allowed when no rule is enabled at all.
func DecideACL(rules []*ConnectionACL, s ACLSubject) (ACLDecision, *ConnectionACL) {
	active := false
	for _, rule := range rules {
		if !rule.Enabled {
			continue
		}
		active = true
		if !rule.matchesUser(s.UserID, s.UserGroupID) || !rule.matchesDevice(s.DeviceID, s.DeviceGroupID) {
			continue
		}
		if rule.Action == ACLActionDeny {
			return ACLDecisionDeny, rule
		}
		return ACLDecisionAllow, rule
	}
	switch {
	case s.OwnerAccess:
		return ACLDecisionOwner, nil
	case !active:
		return ACLDecisionNoRules, nil
	}
	return ACLDecisionDeny, nil
}

func (a *ConnectionACL) matchesUser(userID int64, groupID *int64) bool {
	switch {
	case a.UserID == nil && a.UserGroupID == nil:
		return true // all users
	case a.UserID != nil:
		return *a.UserID == userID
	}
	return groupID != nil && *a.UserGroupID == *groupID
}

func (a *ConnectionACL) matchesDevice(deviceID int64, groupID *int64) bool {
	switch {
	case a.DeviceID == nil && a.DeviceGroupID == nil:
		return true // all devices
	case a.DeviceID != nil:
		return *a.DeviceID == deviceID
	}
	return groupID != nil && *a.DeviceGroupID == *groupID
}

// HasEnabledRules reports whether any connection rule is enabled (without
// one, every connection is allowed).
func (r *ConnectionACLs) HasEnabledRules(ctx context.Context) (bool, error) {
	var n int
	err := r.q.QueryRowContext(ctx, `SELECT count(*) FROM (SELECT 1 FROM connection_acls WHERE enabled = 1 LIMIT 1) x`).Scan(&n)
	return n > 0, mapErr(err)
}

// UserACL decides the connections of one user; it loads the rules and the
// user's group once (see ForUser).
type UserACL struct {
	userID  int64
	groupID *int64
	// peers are the members of the user's group when it is "shared".
	peers map[int64]bool
	rules []*ConnectionACL
}

// ForUser loads what deciding userID's connections needs: the user's group,
// the members of that group when it is "shared", and all rules.
func (r *ConnectionACLs) ForUser(ctx context.Context, userID int64) (*UserACL, error) {
	var (
		groupID    sql.NullInt64
		visibility sql.NullString
	)
	err := r.q.QueryRowContext(ctx, `SELECT u.user_group_id, g.visibility FROM users u
		LEFT JOIN user_groups g ON g.id = u.user_group_id WHERE u.id = ?`, userID).Scan(&groupID, &visibility)
	if err != nil {
		return nil, fmt.Errorf("user lookup: %w", mapErr(err))
	}
	u := &UserACL{userID: userID, groupID: ptrID(groupID)}
	if groupID.Valid && GroupVisibility(visibility.String) == VisibilityShared {
		rows, err := r.q.QueryContext(ctx, `SELECT id FROM users WHERE user_group_id = ?`, groupID.Int64)
		if err != nil {
			return nil, mapErr(err)
		}
		defer rows.Close()
		u.peers = map[int64]bool{}
		for rows.Next() {
			var id int64
			if err := rows.Scan(&id); err != nil {
				return nil, mapErr(err)
			}
			u.peers[id] = true
		}
		if err := rows.Err(); err != nil {
			return nil, mapErr(err)
		}
	}
	if u.rules, err = r.List(ctx); err != nil {
		return nil, err
	}
	return u, nil
}

// Decide returns the decision for a connection of the user to d.
func (u *UserACL) Decide(d *Device) (ACLDecision, *ConnectionACL) {
	owner := d.OwnerUserID != nil && (*d.OwnerUserID == u.userID || u.peers[*d.OwnerUserID])
	return DecideACL(u.rules, ACLSubject{UserID: u.userID, UserGroupID: u.groupID, DeviceID: d.ID,
		DeviceGroupID: d.DeviceGroupID, OwnerAccess: owner})
}

// Evaluate checks connection permission between userID and deviceID and
// returns the effective control role of allowed connections (the rule's
// role, else the default role).
func (r *ConnectionACLs) Evaluate(
	ctx context.Context,
	userID int64,
	deviceID int64,
) (ACLDecision, *ConnectionACL, *ControlRole, error) {
	u, err := r.ForUser(ctx, userID)
	if err != nil {
		return "", nil, nil, err
	}
	var (
		ownerUserID   sql.NullInt64
		deviceGroupID sql.NullInt64
	)
	err = r.q.QueryRowContext(ctx, `SELECT owner_user_id, device_group_id FROM devices WHERE id = ?`, deviceID).
		Scan(&ownerUserID, &deviceGroupID)
	if err != nil {
		return "", nil, nil, fmt.Errorf("device lookup: %w", mapErr(err))
	}
	dec, rule := u.Decide(&Device{ID: deviceID, OwnerUserID: ptrID(ownerUserID), DeviceGroupID: ptrID(deviceGroupID)})
	if dec == ACLDecisionDeny {
		return dec, rule, nil, nil
	}
	roles := &ControlRoles{q: r.q, now: r.now}
	var role *ControlRole
	if rule != nil && rule.ControlRoleID != nil {
		if role, err = roles.Get(ctx, *rule.ControlRoleID); err != nil && !errors.Is(err, ErrNotFound) {
			return "", nil, nil, err
		}
	}
	if role == nil {
		if role, err = roles.GetDefault(ctx); err != nil && !errors.Is(err, ErrNotFound) {
			return "", nil, nil, err
		}
	}
	return dec, rule, role, nil
}

// FilterAccessibleDevices filters devices to only those permitted for userID
// (the rules are loaded once).
func (r *ConnectionACLs) FilterAccessibleDevices(ctx context.Context, userID int64, devices []*Device) ([]*Device, error) {
	u, err := r.ForUser(ctx, userID)
	if err != nil {
		return nil, err
	}
	return u.Filter(devices), nil
}

// Filter returns the devices the user may connect to.
func (u *UserACL) Filter(devices []*Device) []*Device {
	out := make([]*Device, 0, len(devices))
	for _, d := range devices {
		if dec, _ := u.Decide(d); dec != ACLDecisionDeny {
			out = append(out, d)
		}
	}
	return out
}
