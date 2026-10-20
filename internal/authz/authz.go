// Package authz decides whether a RustDesk connection may start and which
// control permissions the controlled device applies to it, from the
// connection rules and control roles (docs/acl-policy.md).
//
// hbbs (bropines/rustdesk-server with AUTHZ_URL set) asks the panel at
// POST /internal/hbbs/authorize for every punch-hole and relay request and
// attaches the returned permissions to the request it forwards to the
// device (ControlPermissions; honoured by RustDesk 1.4.5 and later on
// desktop platforms). The same decision backs the after-the-fact kick of the
// client API's audit handler.
package authz

import (
	"context"
	"errors"
	"fmt"

	"github.com/bropines/rdpanel/internal/auth"
	"github.com/bropines/rdpanel/internal/crypto"
	"github.com/bropines/rdpanel/internal/store"
)

// Causes (Decision.Cause) are short, fixed strings for logs.
const (
	CauseUnknownDevice = "unknown_device"
	CauseAdmin         = "admin"
	CauseAnonymous     = "anonymous"
	CauseNoRules       = "no_rules"
	CauseOwner         = "owner"
	CauseRule          = "rule"
	CauseDefaultDeny   = "default_deny"
	CauseConnType      = "conn_type"
)

// Reasons shown to the controller when a connection is denied.
const (
	ReasonSignIn      = "Sign in to connect: access rules apply"
	ReasonDefaultDeny = "No access rule allows this connection"
)

// Decision is the outcome for one connection.
type Decision struct {
	Allow bool
	// Permissions restrict an allowed connection (0 = device settings).
	Permissions Permissions
	// Reason is shown to the controller when the connection is denied.
	Reason string
	// Cause, UserID (0 = anonymous), Rule and Role explain the decision in
	// logs.
	Cause  string
	UserID int64
	Rule   string
	Role   string
}

// Decide is the policy for a connection of user (nil: anonymous or unknown
// controller) to the registered device d:
//
//   - active admins are never restricted;
//   - while any connection rule is enabled, anonymous controllers are denied
//     and users get the rules' decision (store.ConnectionACLs.Evaluate);
//     without enabled rules everyone is allowed;
//   - an allowed connection runs under its effective control role (the
//     matching rule's role, else the default role): what the role does not
//     allow is disabled, and file transfer, port forwarding / RDP and
//     terminal connections are denied outright when the role forbids them.
//
// Disabled users count as anonymous.
func Decide(ctx context.Context, acl *store.ConnectionACLs, user *store.User, d *store.Device, ct ConnType) (Decision, error) {
	if user != nil && !user.IsActive() {
		user = nil
	}
	if user != nil && user.IsAdmin() {
		return Decision{Allow: true, Cause: CauseAdmin, UserID: user.ID}, nil
	}
	var (
		out  Decision
		role *store.ControlRole
	)
	if user == nil {
		has, err := acl.HasEnabledRules(ctx)
		if err != nil {
			return Decision{}, err
		}
		if has {
			return Decision{Reason: ReasonSignIn, Cause: CauseAnonymous}, nil
		}
		if role, err = acl.EffectiveRole(ctx, nil); err != nil {
			return Decision{}, err
		}
		out.Cause = CauseNoRules
	} else {
		dec, rule, r, err := acl.EvaluateDevice(ctx, user.ID, d)
		if err != nil {
			return Decision{}, err
		}
		out.UserID = user.ID
		if rule != nil {
			out.Rule = rule.Name
		}
		switch dec {
		case store.ACLDecisionDeny:
			out.Reason, out.Cause = ReasonDefaultDeny, CauseDefaultDeny
			if rule != nil {
				out.Reason, out.Cause = "Denied by access rule "+rule.Name, CauseRule
			}
			return out, nil
		case store.ACLDecisionOwner:
			out.Cause = CauseOwner
		case store.ACLDecisionNoRules:
			out.Cause = CauseNoRules
		default:
			out.Cause = CauseRule
		}
		role = r
	}
	if role != nil {
		out.Role = role.Name
	}
	if reason := connTypeDenied(role, ct); reason != "" {
		out.Reason, out.Cause = reason, CauseConnType
		return out, nil
	}
	out.Allow = true
	out.Permissions = RolePermissions(role)
	return out, nil
}

// connTypeDenied is the reason a connection of type ct is refused under
// role, or "".
func connTypeDenied(role *store.ControlRole, ct ConnType) string {
	if role == nil {
		return ""
	}
	var what string
	switch {
	case ct == ConnFileTransfer && !role.AllowFileTransfer:
		what = "File transfer"
	case (ct == ConnPortForward || ct == ConnRDP) && !role.AllowTCPTunneling:
		what = "TCP tunneling"
	case ct == ConnTerminal && !role.AllowTerminal:
		what = "Terminal"
	default:
		return ""
	}
	return fmt.Sprintf("%s is not allowed by control role %s", what, role.Name)
}

// Request is the body hbbs posts for a punch-hole or relay request.
type Request struct {
	// TargetID is the RustDesk id of the device being connected to.
	TargetID string `json:"target_id"`
	// Token is the controller's client token ("" when not signed in).
	Token string `json:"token"`
	// ConnType is the rendezvous ConnType.
	ConnType ConnType `json:"conn_type"`
	// ControllerIP and Kind ("punch_hole" or "request_relay") are only
	// logged.
	ControllerIP string `json:"controller_ip"`
	Kind         string `json:"kind"`
}

// Service decides hbbs requests.
type Service struct {
	Store *store.Store
	// JWT verifies controller tokens (the panel's client token key, shared
	// with hbbs).
	JWT *auth.ClientJWT
}

// Authorize decides req. Devices the panel does not know are not restricted.
func (s *Service) Authorize(ctx context.Context, req Request) (Decision, error) {
	d, err := s.Store.Devices.GetByRustdeskID(ctx, req.TargetID)
	if errors.Is(err, store.ErrNotFound) {
		return Decision{Allow: true, Cause: CauseUnknownDevice}, nil
	}
	if err != nil {
		return Decision{}, fmt.Errorf("target device: %w", err)
	}
	user, err := s.Controller(ctx, req.Token)
	if err != nil {
		return Decision{}, err
	}
	return Decide(ctx, s.Store.ConnectionACLs, user, d, req.ConnType)
}

// Controller resolves a controller's client token to its user. It is nil
// (anonymous) for an empty, malformed, mis-signed or expired token, a token
// the panel recorded and that was since revoked or expired (sign-out, admin
// revocation, sliding expiry), and an unknown or disabled user. Tokens the
// panel signed without recording them (the web client's short-lived ones)
// are accepted on their signature, as hbbs does for MUST_LOGIN.
func (s *Service) Controller(ctx context.Context, token string) (*store.User, error) {
	if token == "" || s.JWT == nil {
		return nil, nil
	}
	claims, err := s.JWT.Verify(token)
	if err != nil {
		return nil, nil
	}
	inactive, err := s.Store.AuthTokens.Inactive(ctx, store.TokenClient, crypto.HashToken(token))
	if err != nil {
		return nil, fmt.Errorf("controller token: %w", err)
	}
	if inactive {
		return nil, nil
	}
	u, err := s.Store.Users.GetByID(ctx, int64(claims.UserID))
	if errors.Is(err, store.ErrNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("controller user: %w", err)
	}
	if !u.IsActive() {
		return nil, nil
	}
	return u, nil
}
