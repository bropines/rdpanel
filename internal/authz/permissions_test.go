package authz

import (
	"testing"

	"github.com/bropines/rdpanel/internal/store"
)

func allowAll() *store.ControlRole {
	return &store.ControlRole{Name: "all", AllowKeyboardMouse: true, AllowClipboard: true, AllowFileTransfer: true,
		AllowAudio: true, AllowTCPTunneling: true, AllowTerminal: true, AllowRestart: true}
}

// Each capability a role takes away disables exactly its permission
// (state 1 at bits 2*enum), and nothing is ever enabled.
func TestRolePermissionsBitMapping(t *testing.T) {
	if got := RolePermissions(nil); got != 0 {
		t.Fatalf("nil role = %d", got)
	}
	if got := RolePermissions(allowAll()); got != 0 || got.String() != "0" {
		t.Fatalf("allow-all role = %d", got)
	}
	cases := []struct {
		name  string
		deny  func(*store.ControlRole)
		perm  Permission
		value uint64
	}{
		{"keyboard", func(r *store.ControlRole) { r.AllowKeyboardMouse = false }, PermKeyboard, 1 << 0},
		{"clipboard", func(r *store.ControlRole) { r.AllowClipboard = false }, PermClipboard, 1 << 4},
		{"file", func(r *store.ControlRole) { r.AllowFileTransfer = false }, PermFile, 1 << 6},
		{"audio", func(r *store.ControlRole) { r.AllowAudio = false }, PermAudio, 1 << 8},
		{"terminal", func(r *store.ControlRole) { r.AllowTerminal = false }, PermTerminal, 1 << 12},
		{"tunnel", func(r *store.ControlRole) { r.AllowTCPTunneling = false }, PermTunnel, 1 << 14},
		{"restart", func(r *store.ControlRole) { r.AllowRestart = false }, PermRestart, 1 << 16},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			r := allowAll()
			c.deny(r)
			got := RolePermissions(r)
			if uint64(got) != c.value {
				t.Fatalf("permissions = %d, want %d", got, c.value)
			}
			if got.State(c.perm) != StateDisable {
				t.Fatalf("state = %d", got.State(c.perm))
			}
			for p := Permission(0); p < 32; p++ {
				if p != c.perm && got.State(p) != StateNotSet {
					t.Fatalf("permission %d also set (%d)", p, got.State(p))
				}
			}
		})
	}

	// The seeded "View Only" role: keyboard, clipboard, file, terminal,
	// tunnel and restart off; audio left to the device.
	viewOnly := &store.ControlRole{AllowAudio: true}
	got := RolePermissions(viewOnly)
	if got.String() != "86097" { // 1 + 16 + 64 + 4096 + 16384 + 65536
		t.Fatalf("view only = %s", got)
	}
	for p := Permission(0); p < 32; p++ {
		if got.State(p) == StateEnable {
			t.Fatalf("permission %d enabled", p)
		}
	}
	if got.State(PermAudio) != StateNotSet || got.State(PermCamera) != StateNotSet || got.State(PermRecording) != StateNotSet {
		t.Fatal("unmapped permissions must stay unset")
	}
}

func TestPermissionsWith(t *testing.T) {
	var p Permissions
	p = p.With(PermUSB, StateEnable)
	if p.State(PermUSB) != StateEnable || uint64(p) != 2<<26 {
		t.Fatalf("usb enable = %d", p)
	}
	p = p.With(PermUSB, StateDisable)
	if p.State(PermUSB) != StateDisable || uint64(p) != 1<<26 {
		t.Fatalf("usb disable = %d", p)
	}
	p = p.With(PermUSB, StateNotSet)
	if p != 0 {
		t.Fatalf("cleared = %d", p)
	}
	// The top permission slot (31) uses the highest two bits of the u64.
	if s := Permissions(0).With(31, StateEnable).String(); s != "9223372036854775808" {
		t.Fatalf("slot 31 = %s", s)
	}
}

func TestAuditConnType(t *testing.T) {
	for in, want := range map[int]ConnType{0: ConnDefault, 1: ConnFileTransfer, 2: ConnPortForward, 3: ConnViewCamera,
		4: ConnTerminal, 9: ConnDefault, -1: ConnDefault} {
		if got := AuditConnType(in); got != want {
			t.Errorf("AuditConnType(%d) = %d, want %d", in, got, want)
		}
	}
}
