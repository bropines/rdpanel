package aclpolicy

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/bropines/rdpanel/internal/store"
)

// Service coordinates access policy compilation, testing, exporting, and atomic application.
type Service struct {
	store *store.Store
}

// NewService creates a new aclpolicy Service.
func NewService(s *store.Store) *Service {
	return &Service{store: s}
}

// CompiledPolicy holds the normalized and resolved database representations ready to be persisted.
type CompiledPolicy struct {
	Document *Document
	Roles    []*store.ControlRole
	ACLs     []*store.ConnectionACL
}

// Export serializes the database's current roles and ACLs into a clean, human-readable HuJSON string with comments.
func (s *Service) Export(ctx context.Context) (string, error) {
	roles, err := s.store.ControlRoles.List(ctx)
	if err != nil {
		return "", fmt.Errorf("list roles: %w", err)
	}

	acls, err := s.store.ConnectionACLs.List(ctx)
	if err != nil {
		return "", fmt.Errorf("list acls: %w", err)
	}

	var sb strings.Builder
	sb.WriteString("// RD Panel Connection Access Control (ACL) Policy File\n")
	sb.WriteString("// Declarative authorization matrix and session capabilities.\n")
	sb.WriteString("{\n")
	sb.WriteString("  \"version\": 1,\n\n")

	// 1. Roles section
	sb.WriteString("  // Control Roles define granular session capabilities.\n")
	sb.WriteString("  \"roles\": {\n")
	for i, r := range roles {
		isLastRole := i == len(roles)-1
		sb.WriteString(fmt.Sprintf("    %q: {\n", r.Name))
		if r.Note != "" {
			sb.WriteString(fmt.Sprintf("      \"note\": %q,\n", r.Note))
		}
		sb.WriteString(fmt.Sprintf("      \"allow_keyboard_mouse\": %t,\n", r.AllowKeyboardMouse))
		sb.WriteString(fmt.Sprintf("      \"allow_clipboard\": %t,\n", r.AllowClipboard))
		sb.WriteString(fmt.Sprintf("      \"allow_file_transfer\": %t,\n", r.AllowFileTransfer))
		sb.WriteString(fmt.Sprintf("      \"allow_audio\": %t,\n", r.AllowAudio))
		sb.WriteString(fmt.Sprintf("      \"allow_tcp_tunneling\": %t,\n", r.AllowTCPTunneling))
		sb.WriteString(fmt.Sprintf("      \"allow_terminal\": %t,\n", r.AllowTerminal))
		sb.WriteString(fmt.Sprintf("      \"allow_restart\": %t,\n", r.AllowRestart))
		sb.WriteString(fmt.Sprintf("      \"is_default\": %t\n", r.IsDefault))
		if isLastRole {
			sb.WriteString("    }\n")
		} else {
			sb.WriteString("    },\n")
		}
	}
	sb.WriteString("  },\n\n")

	// 2. ACL Rules section
	sb.WriteString("  // Connection Access Rules evaluated by priority order.\n")
	sb.WriteString("  \"acls\": [\n")
	for i, a := range acls {
		isLastRule := i == len(acls)-1
		sb.WriteString("    {\n")
		sb.WriteString(fmt.Sprintf("      \"name\": %q,\n", a.Name))
		sb.WriteString(fmt.Sprintf("      \"action\": %q,\n", a.Action))
		if a.Note != "" {
			sb.WriteString(fmt.Sprintf("      \"note\": %q,\n", a.Note))
		}

		// src
		var srcStr string
		if a.UserID != nil && a.UserName != "" {
			srcStr = fmt.Sprintf("[\"user:%s\"]", a.UserName)
		} else if a.UserGroupID != nil && a.UserGroupName != "" {
			srcStr = fmt.Sprintf("[\"group:%s\"]", a.UserGroupName)
		} else {
			srcStr = "[\"*\"]"
		}
		sb.WriteString(fmt.Sprintf("      \"src\": %s,\n", srcStr))

		// dst
		var dstStr string
		if a.DeviceID != nil && a.DeviceName != "" {
			dstStr = fmt.Sprintf("[\"device:%s\"]", a.DeviceName)
		} else if a.DeviceGroupID != nil && a.DeviceGroupName != "" {
			dstStr = fmt.Sprintf("[\"device_group:%s\"]", a.DeviceGroupName)
		} else {
			dstStr = "[\"*\"]"
		}
		sb.WriteString(fmt.Sprintf("      \"dst\": %s,\n", dstStr))

		if a.ControlRoleName != "" {
			sb.WriteString(fmt.Sprintf("      \"role\": %q,\n", a.ControlRoleName))
		}
		sb.WriteString(fmt.Sprintf("      \"priority\": %d,\n", a.Priority))
		sb.WriteString(fmt.Sprintf("      \"enabled\": %t\n", a.Enabled))

		if isLastRule {
			sb.WriteString("    }\n")
		} else {
			sb.WriteString("    },\n")
		}
	}
	sb.WriteString("  ],\n\n")

	// 3. Tests section template
	sb.WriteString("  // In-line tests evaluated automatically before saving changes.\n")
	sb.WriteString("  \"tests\": []\n")
	sb.WriteString("}\n")

	return sb.String(), nil
}

// ValidateAndTest parses HuJSON, resolves names, checks syntax, and executes the in-document tests.
func (s *Service) ValidateAndTest(ctx context.Context, raw []byte) (*CompiledPolicy, *ValidationResult, error) {
	doc, err := Parse(raw)
	if err != nil {
		return nil, &ValidationResult{
			Valid:  false,
			Errors: []string{err.Error()},
		}, nil
	}

	res := &ValidationResult{
		Valid:      true,
		RolesCount: len(doc.Roles),
		RulesCount: len(doc.ACLs),
		TestsCount: len(doc.Tests),
	}

	compiled, errs := s.compile(ctx, doc)
	if len(errs) > 0 {
		res.Valid = false
		res.Errors = errs
		return nil, res, nil
	}

	// Run test assertions
	testResults, testErrors := s.runTests(ctx, compiled, doc.Tests)
	res.TestResults = testResults
	if len(testErrors) > 0 {
		res.Valid = false
		res.Errors = append(res.Errors, testErrors...)
	}

	return compiled, res, nil
}

// Apply compiles, validates, executes tests, and atomically replaces all roles and ACLs in a transaction.
func (s *Service) Apply(ctx context.Context, raw []byte) (*ValidationResult, error) {
	compiled, valResult, err := s.ValidateAndTest(ctx, raw)
	if err != nil {
		return nil, err
	}
	if !valResult.Valid {
		return valResult, fmt.Errorf("policy validation failed: %s", strings.Join(valResult.Errors, "; "))
	}

	// Atomic persistence within a Store transaction
	err = s.store.Tx(ctx, func(tx *store.Store) error {
		// 1. Delete all current connection_acls
		currentACLs, err := tx.ConnectionACLs.List(ctx)
		if err != nil {
			return fmt.Errorf("list current acls: %w", err)
		}
		for _, a := range currentACLs {
			if err := tx.ConnectionACLs.Delete(ctx, a.ID); err != nil {
				return fmt.Errorf("delete acl %d: %w", a.ID, err)
			}
		}

		// 2. Map existing roles by name
		existingRoles, err := tx.ControlRoles.List(ctx)
		if err != nil {
			return fmt.Errorf("list existing roles: %w", err)
		}
		existingByName := make(map[string]*store.ControlRole, len(existingRoles))
		for _, er := range existingRoles {
			existingByName[strings.ToLower(er.Name)] = er
		}

		// 3. Upsert / synchronize roles from document
		roleNameToID := make(map[string]int64, len(compiled.Roles))
		docRoleNames := make(map[string]bool, len(compiled.Roles))

		for _, cr := range compiled.Roles {
			nameKey := strings.ToLower(cr.Name)
			docRoleNames[nameKey] = true

			if existing, ok := existingByName[nameKey]; ok {
				existing.Note = cr.Note
				existing.AllowKeyboardMouse = cr.AllowKeyboardMouse
				existing.AllowClipboard = cr.AllowClipboard
				existing.AllowFileTransfer = cr.AllowFileTransfer
				existing.AllowAudio = cr.AllowAudio
				existing.AllowTCPTunneling = cr.AllowTCPTunneling
				existing.AllowTerminal = cr.AllowTerminal
				existing.AllowRestart = cr.AllowRestart
				existing.IsDefault = cr.IsDefault
				if err := tx.ControlRoles.Update(ctx, existing); err != nil {
					return fmt.Errorf("update role %q: %w", cr.Name, err)
				}
				roleNameToID[nameKey] = existing.ID
			} else {
				if err := tx.ControlRoles.Create(ctx, cr); err != nil {
					return fmt.Errorf("create role %q: %w", cr.Name, err)
				}
				roleNameToID[nameKey] = cr.ID
			}
		}

		// Delete unreferenced roles that are no longer in the document (and not default)
		for _, er := range existingRoles {
			if !docRoleNames[strings.ToLower(er.Name)] && !er.IsDefault {
				_ = tx.ControlRoles.Delete(ctx, er.ID)
			}
		}

		// 4. Insert new ACL rules
		for _, a := range compiled.ACLs {
			// Resolve role ID from newly created/updated roles if it was named
			if a.ControlRoleName != "" {
				if id, ok := roleNameToID[strings.ToLower(a.ControlRoleName)]; ok {
					a.ControlRoleID = &id
				}
			}
			if err := tx.ConnectionACLs.Create(ctx, a); err != nil {
				return fmt.Errorf("create acl rule %q: %w", a.Name, err)
			}
		}

		return nil
	})

	if err != nil {
		return valResult, fmt.Errorf("apply policy: %w", err)
	}

	return valResult, nil
}

func (s *Service) compile(ctx context.Context, doc *Document) (*CompiledPolicy, []string) {
	var errs []string

	// Compile roles
	var compiledRoles []*store.ControlRole
	roleNames := make(map[string]bool)
	hasDefaultRole := false

	for name, spec := range doc.Roles {
		trimmedName := strings.TrimSpace(name)
		nameKey := strings.ToLower(trimmedName)
		if roleNames[nameKey] {
			errs = append(errs, fmt.Sprintf("duplicate role name %q", trimmedName))
			continue
		}
		roleNames[nameKey] = true
		if spec.IsDefault {
			hasDefaultRole = true
		}

		compiledRoles = append(compiledRoles, &store.ControlRole{
			Name:               trimmedName,
			Note:               strings.TrimSpace(spec.Note),
			AllowKeyboardMouse: spec.AllowKeyboardMouse,
			AllowClipboard:     spec.AllowClipboard,
			AllowFileTransfer:  spec.AllowFileTransfer,
			AllowAudio:         spec.AllowAudio,
			AllowTCPTunneling:  spec.AllowTCPTunneling,
			AllowTerminal:      spec.AllowTerminal,
			AllowRestart:       spec.AllowRestart,
			IsDefault:          spec.IsDefault,
		})
	}

	// Sort roles by name for determinism
	sort.Slice(compiledRoles, func(i, j int) bool {
		return compiledRoles[i].Name < compiledRoles[j].Name
	})

	// Pre-fetch DB entities for selector resolution
	userMap := make(map[string]*store.User)
	userGroupMap := make(map[string]*store.UserGroup)
	deviceMap := make(map[string]*store.Device)
	deviceGroupMap := make(map[string]*store.DeviceGroup)

	// Fetch existing DB roles if needed
	dbRoles, err := s.store.ControlRoles.List(ctx)
	if err == nil {
		for _, dr := range dbRoles {
			roleNames[strings.ToLower(dr.Name)] = true
			if dr.IsDefault {
				hasDefaultRole = true
			}
		}
	}

	// Compile ACLs
	var compiledACLs []*store.ConnectionACL
	priorityCounter := 100

	for idx, rule := range doc.ACLs {
		priority := rule.Priority
		if priority <= 0 {
			priority = priorityCounter
			priorityCounter += 10
		}

		enabled := true
		if rule.Enabled != nil {
			enabled = *rule.Enabled
		}

		action := strings.ToLower(strings.TrimSpace(rule.Action))
		roleName := strings.TrimSpace(rule.Role)
		if action == "allow" && roleName != "" {
			if !roleNames[strings.ToLower(roleName)] {
				errs = append(errs, fmt.Sprintf("rule %q: unknown role %q", rule.Name, roleName))
			}
		}

		// Expand src x dst selectors
		for _, src := range rule.Src {
			srcType, srcVal := parseSelector(src)
			var userID, userGroupID *int64
			var userName, userGroupName string

			switch srcType {
			case "all":
				// wildcard
			case "user":
				u, ok := userMap[strings.ToLower(srcVal)]
				if !ok {
					found, err := s.store.Users.GetByUsername(ctx, srcVal)
					if err != nil {
						errs = append(errs, fmt.Sprintf("rule %q: user %q not found", rule.Name, srcVal))
						continue
					}
					u = found
					userMap[strings.ToLower(srcVal)] = u
				}
				userID = &u.ID
				userName = u.Username
			case "group":
				g, ok := userGroupMap[strings.ToLower(srcVal)]
				if !ok {
					found, err := s.store.UserGroups.GetByName(ctx, srcVal)
					if err != nil {
						errs = append(errs, fmt.Sprintf("rule %q: user group %q not found", rule.Name, srcVal))
						continue
					}
					g = found
					userGroupMap[strings.ToLower(srcVal)] = g
				}
				userGroupID = &g.ID
				userGroupName = g.Name
			default:
				errs = append(errs, fmt.Sprintf("rule %q: invalid src selector %q", rule.Name, src))
				continue
			}

			for _, dst := range rule.Dst {
				dstType, dstVal := parseSelector(dst)
				var deviceID, deviceGroupID *int64
				var deviceName, deviceGroupName string

				switch dstType {
				case "all":
					// wildcard
				case "device":
					d, ok := deviceMap[strings.ToLower(dstVal)]
					if !ok {
						found, err := s.store.Devices.GetByRustdeskID(ctx, dstVal)
						if err != nil {
							errs = append(errs, fmt.Sprintf("rule %q: device %q not found", rule.Name, dstVal))
							continue
						}
						d = found
						deviceMap[strings.ToLower(dstVal)] = d
					}
					deviceID = &d.ID
					deviceName = d.RustdeskID
				case "device_group", "group":
					dg, ok := deviceGroupMap[strings.ToLower(dstVal)]
					if !ok {
						found, err := s.store.DeviceGroups.GetByName(ctx, dstVal)
						if err != nil {
							errs = append(errs, fmt.Sprintf("rule %q: device group %q not found", rule.Name, dstVal))
							continue
						}
						dg = found
						deviceGroupMap[strings.ToLower(dstVal)] = dg
					}
					deviceGroupID = &dg.ID
					deviceGroupName = dg.Name
				default:
					errs = append(errs, fmt.Sprintf("rule %q: invalid dst selector %q", rule.Name, dst))
					continue
				}

				name := rule.Name
				if len(rule.Src) > 1 || len(rule.Dst) > 1 {
					name = fmt.Sprintf("%s (%s -> %s)", rule.Name, src, dst)
				}

				compiledACLs = append(compiledACLs, &store.ConnectionACL{
					Name:            name,
					Note:            strings.TrimSpace(rule.Note),
					UserID:          userID,
					UserName:        userName,
					UserGroupID:     userGroupID,
					UserGroupName:   userGroupName,
					DeviceID:        deviceID,
					DeviceName:      deviceName,
					DeviceGroupID:   deviceGroupID,
					DeviceGroupName: deviceGroupName,
					ControlRoleName: roleName,
					Action:          store.ACLAction(action),
					Priority:        priority,
					Enabled:         enabled,
				})
			}
		}
		_ = idx
	}

	// Sort ACL rules by priority ASC
	sort.Slice(compiledACLs, func(i, j int) bool {
		return compiledACLs[i].Priority < compiledACLs[j].Priority
	})

	_ = hasDefaultRole
	if len(errs) > 0 {
		return nil, errs
	}

	return &CompiledPolicy{
		Document: doc,
		Roles:    compiledRoles,
		ACLs:     compiledACLs,
	}, nil
}

func (s *Service) runTests(ctx context.Context, pol *CompiledPolicy, tests []TestCase) ([]TestResult, []string) {
	var results []TestResult
	var testErrors []string

	// Build in-memory lookup for simulation
	roleByName := make(map[string]*store.ControlRole)
	var defaultRole *store.ControlRole
	for _, r := range pol.Roles {
		roleByName[strings.ToLower(r.Name)] = r
		if r.IsDefault {
			defaultRole = r
		}
	}
	if defaultRole == nil {
		if dr, err := s.store.ControlRoles.GetDefault(ctx); err == nil {
			defaultRole = dr
		}
	}

	for idx, tc := range tests {
		srcType, srcVal := parseSelector(tc.Src)
		if srcType != "user" && srcType != "plain" {
			errMsg := fmt.Sprintf("test #%d: src must refer to a user (got %q)", idx+1, tc.Src)
			results = append(results, TestResult{Test: tc, Passed: false, Error: errMsg})
			testErrors = append(testErrors, errMsg)
			continue
		}

		user, err := s.store.Users.GetByUsername(ctx, srcVal)
		if err != nil {
			errMsg := fmt.Sprintf("test #%d: user %q not found", idx+1, srcVal)
			results = append(results, TestResult{Test: tc, Passed: false, Error: errMsg})
			testErrors = append(testErrors, errMsg)
			continue
		}

		dstType, dstVal := parseSelector(tc.Dst)
		if dstType != "device" && dstType != "plain" {
			errMsg := fmt.Sprintf("test #%d: dst must refer to a device (got %q)", idx+1, tc.Dst)
			results = append(results, TestResult{Test: tc, Passed: false, Error: errMsg})
			testErrors = append(testErrors, errMsg)
			continue
		}

		dev, err := s.store.Devices.GetByRustdeskID(ctx, dstVal)
		if err != nil {
			errMsg := fmt.Sprintf("test #%d: device %q not found", idx+1, dstVal)
			results = append(results, TestResult{Test: tc, Passed: false, Error: errMsg})
			testErrors = append(testErrors, errMsg)
			continue
		}

		// Evaluate in-memory
		actualDecision, actualRole := evaluateInMemory(user, dev, pol.ACLs, roleByName, defaultRole)

		expectedAction := strings.ToLower(strings.TrimSpace(tc.Expect))
		passed := true
		var failMsg string

		// Normalization: allow matches "allow", "owner", "no_rules"
		isAllow := actualDecision == "allow" || actualDecision == "owner" || actualDecision == "no_rules"
		if expectedAction == "allow" && !isAllow {
			passed = false
			failMsg = fmt.Sprintf("expected allow, got %s", actualDecision)
		} else if expectedAction == "deny" && isAllow {
			passed = false
			failMsg = fmt.Sprintf("expected deny, got %s", actualDecision)
		} else if expectedAction == "allow" && tc.Role != "" {
			if !strings.EqualFold(tc.Role, actualRole) {
				passed = false
				failMsg = fmt.Sprintf("expected role %q, got %q", tc.Role, actualRole)
			}
		}

		res := TestResult{
			Test:           tc,
			Passed:         passed,
			ActualDecision: actualDecision,
			ActualRole:     actualRole,
		}
		if !passed {
			res.Error = failMsg
			testErrors = append(testErrors, fmt.Sprintf("test #%d (%s -> %s): %s", idx+1, tc.Src, tc.Dst, failMsg))
		}
		results = append(results, res)
	}

	return results, testErrors
}

func evaluateInMemory(
	u *store.User,
	d *store.Device,
	acls []*store.ConnectionACL,
	rolesByName map[string]*store.ControlRole,
	defaultRole *store.ControlRole,
) (string, string) {
	isOwner := d.OwnerUserID != nil && *d.OwnerUserID == u.ID

	// Filter active
	var active []*store.ConnectionACL
	for _, a := range acls {
		if a.Enabled {
			active = append(active, a)
		}
	}

	getRoleName := func(name string) string {
		if name != "" {
			if r, ok := rolesByName[strings.ToLower(name)]; ok {
				return r.Name
			}
		}
		if defaultRole != nil {
			return defaultRole.Name
		}
		return ""
	}

	if len(active) == 0 {
		if isOwner {
			return "owner", getRoleName("")
		}
		return "no_rules", getRoleName("")
	}

	for _, a := range active {
		// User match
		userMatch := false
		if a.UserID == nil && a.UserGroupID == nil {
			userMatch = true
		} else if a.UserID != nil && *a.UserID == u.ID {
			userMatch = true
		} else if a.UserGroupID != nil && u.UserGroupID != nil && *a.UserGroupID == *u.UserGroupID {
			userMatch = true
		}
		if !userMatch {
			continue
		}

		// Device match
		deviceMatch := false
		if a.DeviceID == nil && a.DeviceGroupID == nil {
			deviceMatch = true
		} else if a.DeviceID != nil && *a.DeviceID == d.ID {
			deviceMatch = true
		} else if a.DeviceGroupID != nil && d.DeviceGroupID != nil && *a.DeviceGroupID == *d.DeviceGroupID {
			deviceMatch = true
		}
		if !deviceMatch {
			continue
		}

		// Match found!
		if a.Action == "deny" {
			return "deny", ""
		}
		return "allow", getRoleName(a.ControlRoleName)
	}

	if isOwner {
		return "owner", getRoleName("")
	}
	return "deny", ""
}

func parseSelector(s string) (selectorType string, value string) {
	s = strings.TrimSpace(s)
	if s == "*" {
		return "all", ""
	}
	parts := strings.SplitN(s, ":", 2)
	if len(parts) == 1 {
		return "plain", parts[0]
	}
	prefix := strings.ToLower(parts[0])
	val := strings.TrimSpace(parts[1])
	switch prefix {
	case "user", "u":
		return "user", val
	case "group", "g", "user_group":
		return "group", val
	case "device", "d":
		return "device", val
	case "device_group", "dg":
		return "device_group", val
	default:
		return "plain", s
	}
}

// Errors
var (
	ErrValidationFailed = errors.New("policy validation failed")
	ErrTestFailed       = errors.New("policy test assertion failed")
)
