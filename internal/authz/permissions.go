package authz

import (
	"strconv"

	"github.com/bropines/rdpanel/internal/store"
)

// Permission is a RustDesk ControlPermissions.Permission value
// (hbb_common rendezvous.proto); its state lives at bit 2*Permission of the
// bitmap.
type Permission uint8

// Permissions known to RustDesk clients.
const (
	PermKeyboard      Permission = 0
	PermRemotePrinter Permission = 1
	PermClipboard     Permission = 2
	PermFile          Permission = 3
	PermAudio         Permission = 4
	PermCamera        Permission = 5
	PermTerminal      Permission = 6
	PermTunnel        Permission = 7
	PermRestart       Permission = 8
	PermRecording     Permission = 9
	PermBlockInput    Permission = 10
	PermRemoteModify  Permission = 11
	PermPrivacyMode   Permission = 12
	PermUSB           Permission = 13
)

// State is the 2-bit state of one permission.
type State uint8

// Permission states. NotSet leaves the decision to the device's own
// setting; Disable turns the capability off for the connection; Enable
// turns it on even when the device owner disabled it (never sent by RD
// Panel: roles only restrict).
const (
	StateNotSet  State = 0
	StateDisable State = 1
	StateEnable  State = 2
)

// Permissions is the ControlPermissions.permissions bitmap (u64, 2 bits per
// permission). The zero value sets nothing: the device's settings apply.
type Permissions uint64

// With returns p with perm set to s.
func (p Permissions) With(perm Permission, s State) Permissions {
	shift := 2 * uint(perm)
	return p&^(3<<shift) | Permissions(s&3)<<shift
}

// State returns the state of perm in p.
func (p Permissions) State(perm Permission) State {
	return State(p>>(2*uint(perm))) & 3
}

// String is the decimal form hbbs expects in the "permissions" field.
func (p Permissions) String() string { return strconv.FormatUint(uint64(p), 10) }

type capability struct {
	allowed bool
	perm    Permission
}

// roleCapabilities maps each control role capability to the permission it
// governs.
func roleCapabilities(r *store.ControlRole) []capability {
	return []capability{
		{r.AllowKeyboardMouse, PermKeyboard},
		{r.AllowClipboard, PermClipboard},
		{r.AllowFileTransfer, PermFile},
		{r.AllowAudio, PermAudio},
		{r.AllowTerminal, PermTerminal},
		{r.AllowTCPTunneling, PermTunnel},
		{r.AllowRestart, PermRestart},
	}
}

// RolePermissions is the bitmap for a connection under control role r: every
// capability the role does not allow is disabled, everything else is left
// unset so the device owner's local settings still apply. A nil role or a
// role allowing everything yields 0.
func RolePermissions(r *store.ControlRole) Permissions {
	if r == nil {
		return 0
	}
	var p Permissions
	for _, c := range roleCapabilities(r) {
		if !c.allowed {
			p = p.With(c.perm, StateDisable)
		}
	}
	return p
}

// ConnType is the rendezvous ConnType of a request (hbb_common
// rendezvous.proto), as hbbs reports it in "conn_type".
type ConnType int

// Connection types.
const (
	ConnDefault      ConnType = 0 // remote desktop
	ConnFileTransfer ConnType = 1
	ConnPortForward  ConnType = 2
	ConnRDP          ConnType = 3
	ConnViewCamera   ConnType = 4
	ConnTerminal     ConnType = 5
)

// AuditConnType maps the "type" of a client's /api/audit/conn record (0
// remote desktop, 1 file transfer, 2 port forward or RDP, 3 view camera,
// 4 terminal) to a ConnType.
func AuditConnType(t int) ConnType {
	switch t {
	case 1:
		return ConnFileTransfer
	case 2:
		return ConnPortForward
	case 3:
		return ConnViewCamera
	case 4:
		return ConnTerminal
	}
	return ConnDefault
}
