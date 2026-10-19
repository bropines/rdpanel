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

	isDef := 0
	if cr.IsDefault {
		isDef = 1
		if _, err := r.q.ExecContext(ctx, `UPDATE control_roles SET is_default = 0`); err != nil {
			return mapErr(err)
		}
	}

	res, err := r.q.ExecContext(ctx, `
		INSERT INTO control_roles (
			name, note,
			allow_keyboard_mouse, allow_clipboard, allow_file_transfer,
			allow_audio, allow_tcp_tunneling, allow_terminal, allow_restart,
			is_default, created_at, updated_at
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		cr.Name, cr.Note,
		boolInt(cr.AllowKeyboardMouse), boolInt(cr.AllowClipboard), boolInt(cr.AllowFileTransfer),
		boolInt(cr.AllowAudio), boolInt(cr.AllowTCPTunneling), boolInt(cr.AllowTerminal), boolInt(cr.AllowRestart),
		isDef, now, now,
	)
	if err != nil {
		return mapErr(err)
	}
	id, err := res.LastInsertId()
	if err != nil {
		return mapErr(err)
	}
	cr.ID = id
	return nil
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

	isDef := 0
	if cr.IsDefault {
		isDef = 1
		if _, err := r.q.ExecContext(ctx, `UPDATE control_roles SET is_default = 0 WHERE id <> ?`, cr.ID); err != nil {
			return mapErr(err)
		}
	}

	return expectOne(r.q.ExecContext(ctx, `
		UPDATE control_roles SET
			name = ?, note = ?,
			allow_keyboard_mouse = ?, allow_clipboard = ?, allow_file_transfer = ?,
			allow_audio = ?, allow_tcp_tunneling = ?, allow_terminal = ?, allow_restart = ?,
			is_default = ?, updated_at = ?
		WHERE id = ?`,
		cr.Name, cr.Note,
		boolInt(cr.AllowKeyboardMouse), boolInt(cr.AllowClipboard), boolInt(cr.AllowFileTransfer),
		boolInt(cr.AllowAudio), boolInt(cr.AllowTCPTunneling), boolInt(cr.AllowTerminal), boolInt(cr.AllowRestart),
		isDef, now, cr.ID,
	))
}

// SetDefault marks role id as default.
func (r *ControlRoles) SetDefault(ctx context.Context, id int64) error {
	now := toMs(r.now())
	if _, err := r.q.ExecContext(ctx, `UPDATE control_roles SET is_default = 0`); err != nil {
		return mapErr(err)
	}
	return expectOne(r.q.ExecContext(ctx, `UPDATE control_roles SET is_default = 1, updated_at = ? WHERE id = ?`, now, id))
}

// Delete removes role id. The default role cannot be deleted.
func (r *ControlRoles) Delete(ctx context.Context, id int64) error {
	cr, err := r.Get(ctx, id)
	if err != nil {
		return err
	}
	if cr.IsDefault {
		return fmt.Errorf("%w: cannot delete default control role", ErrInvalid)
	}
	return expectOne(r.q.ExecContext(ctx, `DELETE FROM control_roles WHERE id = ?`, id))
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

	res, err := r.q.ExecContext(ctx, `
		INSERT INTO connection_acls (
			name, note,
			user_id, user_group_id, device_id, device_group_id, control_role_id,
			action, priority, enabled, created_at, updated_at
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		a.Name, a.Note,
		a.UserID, a.UserGroupID, a.DeviceID, a.DeviceGroupID, a.ControlRoleID,
		string(a.Action), a.Priority, boolInt(a.Enabled), now, now,
	)
	if err != nil {
		return mapErr(err)
	}
	id, err := res.LastInsertId()
	if err != nil {
		return mapErr(err)
	}
	a.ID = id
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

// Evaluate checks connection permission between userID and deviceID.
func (r *ConnectionACLs) Evaluate(
	ctx context.Context,
	userID int64,
	deviceID int64,
) (ACLDecision, *ConnectionACL, *ControlRole, error) {
	// 1. Fetch user group
	var userGroupID sql.NullInt64
	err := r.q.QueryRowContext(ctx, `SELECT user_group_id FROM users WHERE id = ?`, userID).Scan(&userGroupID)
	if err != nil {
		return "", nil, nil, fmt.Errorf("user lookup: %w", mapErr(err))
	}

	// 2. Fetch device details
	var (
		ownerUserID   sql.NullInt64
		deviceGroupID sql.NullInt64
	)
	err = r.q.QueryRowContext(ctx, `SELECT owner_user_id, device_group_id FROM devices WHERE id = ?`, deviceID).
		Scan(&ownerUserID, &deviceGroupID)
	if err != nil {
		return "", nil, nil, fmt.Errorf("device lookup: %w", mapErr(err))
	}

	isOwner := ownerUserID.Valid && ownerUserID.Int64 == userID

	// 3. Query all enabled rules ordered by priority ASC, id ASC
	rules, err := r.List(ctx)
	if err != nil {
		return "", nil, nil, err
	}

	var activeRules []*ConnectionACL
	for _, rule := range rules {
		if rule.Enabled {
			activeRules = append(activeRules, rule)
		}
	}

	// Helper to fetch role or fallback to default
	getRole := func(roleID *int64) (*ControlRole, error) {
		crRepo := &ControlRoles{q: r.q, now: r.now}
		if roleID != nil {
			cr, err := crRepo.Get(ctx, *roleID)
			if err == nil {
				return cr, nil
			}
		}
		return crRepo.GetDefault(ctx)
	}

	// 4. If no enabled rules exist on the system:
	if len(activeRules) == 0 {
		role, _ := getRole(nil)
		if isOwner {
			return ACLDecisionOwner, nil, role, nil
		}
		return ACLDecisionNoRules, nil, role, nil
	}

	// 5. Evaluate rules in priority order
	for _, rule := range activeRules {
		// User matching
		userMatches := false
		if rule.UserID == nil && rule.UserGroupID == nil {
			userMatches = true // Wildcard all users
		} else if rule.UserID != nil && *rule.UserID == userID {
			userMatches = true // Specific user
		} else if rule.UserGroupID != nil && userGroupID.Valid && *rule.UserGroupID == userGroupID.Int64 {
			userMatches = true // User group
		}
		if !userMatches {
			continue
		}

		// Device matching
		deviceMatches := false
		if rule.DeviceID == nil && rule.DeviceGroupID == nil {
			deviceMatches = true // Wildcard all devices
		} else if rule.DeviceID != nil && *rule.DeviceID == deviceID {
			deviceMatches = true // Specific device
		} else if rule.DeviceGroupID != nil && deviceGroupID.Valid && *rule.DeviceGroupID == deviceGroupID.Int64 {
			deviceMatches = true // Device group
		}
		if !deviceMatches {
			continue
		}

		// Matched rule found!
		if rule.Action == ACLActionDeny {
			return ACLDecisionDeny, rule, nil, nil
		}

		role, _ := getRole(rule.ControlRoleID)
		return ACLDecisionAllow, rule, role, nil
	}

	// 6. If no rule matched:
	// Owners retain access if not explicitly denied
	role, _ := getRole(nil)
	if isOwner {
		return ACLDecisionOwner, nil, role, nil
	}

	// In an environment with configured ACL rules, unmatched non-owned access is denied
	return ACLDecisionDeny, nil, nil, nil
}

// FilterAccessibleDevices filters devices to only those permitted for userID.
func (r *ConnectionACLs) FilterAccessibleDevices(ctx context.Context, userID int64, devices []*Device) ([]*Device, error) {
	out := make([]*Device, 0, len(devices))
	for _, d := range devices {
		dec, _, _, err := r.Evaluate(ctx, userID, d.ID)
		if err != nil {
			return nil, err
		}
		if dec != ACLDecisionDeny {
			out = append(out, d)
		}
	}
	return out, nil
}
