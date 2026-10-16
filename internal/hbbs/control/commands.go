package control

import (
	"errors"
	"fmt"
	"math"
	"net"
	"net/netip"
	"regexp"
	"strconv"
	"strings"
)

// Target selects the process an admin command is sent to.
type Target string

// Targets.
const (
	HBBS Target = "hbbs"
	HBBR Target = "hbbr"
)

// Valid reports whether t is a known target.
func (t Target) Valid() bool { return t == HBBS || t == HBBR }

var (
	// ErrNotAllowed is returned for commands outside the allow-list.
	ErrNotAllowed = errors.New("control: command not allowed")
	// ErrInvalidArgs is returned when arguments fail validation.
	ErrInvalidArgs = errors.New("control: invalid arguments")
)

// maxLine is the longest command line we send. hbbs/hbbr read the command with
// a single read() into a 1024-byte buffer.
const maxLine = 1000

// CommandSpec describes one allow-listed admin command.
type CommandSpec struct {
	Target Target `json:"target"`
	Name   string `json:"name"`
	Alias  string `json:"alias"`
	// Usage is a human hint for the console ("[Y|N]").
	Usage string `json:"usage"`
	// validate checks and normalises args and reports whether the call mutates state.
	validate func(args []string) (norm []string, mutating bool, err error)
}

// Commands returns the allow-list (for the admin console UI).
func Commands() []CommandSpec {
	out := make([]CommandSpec, len(specs))
	copy(out, specs)
	return out
}

// lookupSpec resolves a full command name or alias (aliases are case-sensitive:
// hbbr has both "b" (blacklist) and "B" (blocklist)).
func lookupSpec(t Target, cmd string) (*CommandSpec, bool) {
	for i := range specs {
		s := &specs[i]
		if s.Target == t && (s.Name == cmd || s.Alias == cmd) {
			return s, true
		}
	}
	return nil, false
}

var specs = []CommandSpec{
	{HBBS, "h", "", "", noArgs},
	{HBBS, "relay-servers", "rs", "[host[:port],...]", vRelayServers},
	{HBBS, "ip-blocker", "ib", "[<ip>|<start>] [-]", vIPBlocker},
	{HBBS, "ip-changes", "ic", "[<id>|<start>] [-]", vIPChanges},
	{HBBS, "punch-requests", "pr", "[<start> [<page_size>]] | -", vPunchRequests},
	{HBBS, "always-use-relay", "aur", "[Y|N]", vYN},
	{HBBS, "test-geo", "tg", "<ip1> [<ip2>]", vTestGeo},
	{HBBS, "must-login", "ml", "[Y|N]", vYN},
	{HBBS, "tcp-keepalive", "tk", "[Y|N]", vYN},
	{HBBS, "webrtc", "wr", "[Y|N]", vYN},

	{HBBR, "h", "", "", noArgs},
	{HBBR, "blacklist-add", "ba", "<ip>[|<ip>...]", vIPListSet},
	{HBBR, "blacklist-remove", "br", "<ip>[|<ip>...] | all", vIPListRemove},
	{HBBR, "blacklist", "b", "[<ip>]", vOptionalIP},
	{HBBR, "blocklist-add", "Ba", "<ip>[|<ip>...]", vIPListSet},
	{HBBR, "blocklist-remove", "Br", "<ip>[|<ip>...] | all", vIPListRemove},
	{HBBR, "blocklist", "B", "[<ip>]", vOptionalIP},
	{HBBR, "downgrade-threshold", "dt", "[<ratio > 0>]", vOptionalFloat},
	{HBBR, "downgrade-start-check", "t", "[<seconds > 0>]", vOptionalUint},
	{HBBR, "limit-speed", "ls", "[<Mb/s>]", vOptionalFloat},
	{HBBR, "total-bandwidth", "tb", "[<Mb/s>]", vOptionalFloat},
	{HBBR, "single-bandwidth", "sb", "[<Mb/s>]", vOptionalFloat},
	{HBBR, "usage", "u", "", noArgs},
}

func errArgs(format string, a ...any) error {
	return fmt.Errorf("%w: %s", ErrInvalidArgs, fmt.Sprintf(format, a...))
}

func noArgs(args []string) ([]string, bool, error) {
	if len(args) != 0 {
		return nil, false, errArgs("takes no arguments")
	}
	return nil, false, nil
}

func vYN(args []string) ([]string, bool, error) {
	switch len(args) {
	case 0:
		return nil, false, nil
	case 1:
		switch strings.ToUpper(args[0]) {
		case "Y":
			return []string{"Y"}, true, nil
		case "N":
			return []string{"N"}, true, nil
		}
	}
	return nil, false, errArgs("expected [Y|N]")
}

func vRelayServers(args []string) ([]string, bool, error) {
	switch len(args) {
	case 0:
		return nil, false, nil
	case 1:
		list := strings.Split(args[0], ",")
		norm, err := NormalizeWeightedRelayServers(list)
		if err != nil {
			return nil, false, err
		}
		if len(norm) == 0 {
			return nil, false, errArgs("relay server list is empty")
		}
		return []string{strings.Join(norm, ",")}, true, nil
	}
	return nil, false, errArgs("expected a comma-separated list")
}

func vIPBlocker(args []string) ([]string, bool, error) {
	switch len(args) {
	case 0:
		return nil, false, nil
	case 1:
		if n, err := strconv.ParseUint(args[0], 10, 31); err == nil {
			return []string{strconv.FormatUint(n, 10)}, false, nil
		}
		ip, err := canonicalIP(args[0])
		if err != nil {
			return nil, false, err
		}
		return []string{ip}, false, nil
	case 2:
		ip, err := canonicalIP(args[0])
		if err != nil || args[1] != "-" {
			return nil, false, errArgs("expected <ip> -")
		}
		return []string{ip, "-"}, true, nil
	}
	return nil, false, errArgs("too many arguments")
}

// ipChangesMaxStart mirrors hbbs: an argument that parses as an int in
// 0..=10_000_000 is a page offset, never an id.
const ipChangesMaxStart = 10_000_000

func vIPChanges(args []string) ([]string, bool, error) {
	switch len(args) {
	case 0:
		return nil, false, nil
	case 1, 2:
		if n, err := strconv.ParseInt(args[0], 10, 32); err == nil && n >= 0 && n <= ipChangesMaxStart {
			if len(args) == 2 {
				return nil, false, errArgs("ids <= %d cannot be addressed by ip-changes", ipChangesMaxStart)
			}
			return []string{strconv.FormatInt(n, 10)}, false, nil
		}
		if err := ValidateID(args[0]); err != nil {
			return nil, false, err
		}
		if len(args) == 2 {
			if args[1] != "-" {
				return nil, false, errArgs("expected <id> -")
			}
			return []string{args[0], "-"}, true, nil
		}
		return []string{args[0]}, false, nil
	}
	return nil, false, errArgs("too many arguments")
}

const maxPageSize = 10_000

func vPunchRequests(args []string) ([]string, bool, error) {
	if len(args) == 1 && args[0] == "-" {
		return []string{"-"}, true, nil
	}
	if len(args) > 2 {
		return nil, false, errArgs("too many arguments")
	}
	out := make([]string, len(args))
	for i, a := range args {
		n, err := strconv.ParseUint(a, 10, 32)
		if err != nil || (i == 1 && (n == 0 || n > maxPageSize)) {
			return nil, false, errArgs("expected [<start> [<page_size 1..%d>]] or -", maxPageSize)
		}
		out[i] = strconv.FormatUint(n, 10)
	}
	return out, false, nil
}

func vTestGeo(args []string) ([]string, bool, error) {
	if len(args) < 1 || len(args) > 2 {
		return nil, false, errArgs("expected <ip1> [<ip2>]")
	}
	out := make([]string, len(args))
	for i, a := range args {
		ip, err := NormalizeIP(a)
		if err != nil {
			return nil, false, err
		}
		out[i] = ip
	}
	// tg advances hbbs' relay rotation counter, but changes no configuration.
	return out, false, nil
}

func vIPListSet(args []string) ([]string, bool, error) {
	if len(args) != 1 {
		return nil, false, errArgs("expected <ip>[|<ip>...]")
	}
	ips, err := NormalizeIPs(strings.Split(args[0], "|"))
	if err != nil {
		return nil, false, err
	}
	if len(ips) == 0 {
		return nil, false, errArgs("empty ip list")
	}
	return []string{strings.Join(ips, "|")}, true, nil
}

func vIPListRemove(args []string) ([]string, bool, error) {
	if len(args) == 1 && args[0] == "all" {
		return []string{"all"}, true, nil
	}
	return vIPListSet(args)
}

func vOptionalIP(args []string) ([]string, bool, error) {
	switch len(args) {
	case 0:
		return nil, false, nil
	case 1:
		ip, err := NormalizeIP(args[0])
		if err != nil {
			return nil, false, err
		}
		return []string{ip}, false, nil
	}
	return nil, false, errArgs("expected [<ip>]")
}

func vOptionalFloat(args []string) ([]string, bool, error) {
	switch len(args) {
	case 0:
		return nil, false, nil
	case 1:
		v, err := strconv.ParseFloat(args[0], 64)
		if err != nil || !(v > 0) || math.IsInf(v, 0) || v > 1e9 {
			return nil, false, errArgs("expected a number > 0")
		}
		return []string{formatFloat(v)}, true, nil
	}
	return nil, false, errArgs("expected [<number>]")
}

func vOptionalUint(args []string) ([]string, bool, error) {
	switch len(args) {
	case 0:
		return nil, false, nil
	case 1:
		v, err := strconv.ParseUint(args[0], 10, 32)
		if err != nil || v == 0 {
			return nil, false, errArgs("expected an integer > 0")
		}
		return []string{strconv.FormatUint(v, 10)}, true, nil
	}
	return nil, false, errArgs("expected [<integer>]")
}

func formatFloat(v float64) string { return strconv.FormatFloat(v, 'f', -1, 64) }

// NormalizeIP validates a single IP address (no CIDR, no zone) and returns
// its canonical form with IPv4-mapped IPv6 unmapped, which is how hbbs/hbbr
// key their tables.
func NormalizeIP(s string) (string, error) {
	a, err := netip.ParseAddr(s)
	if err != nil || a.Zone() != "" {
		return "", errArgs("invalid ip %q", s)
	}
	return a.Unmap().String(), nil
}

// canonicalIP validates an IP but keeps IPv4-mapped IPv6 as is: hbbs keys its
// ip-blocker table by the raw peer address, which on a dual-stack listener is
// "::ffff:a.b.c.d" for IPv4 clients.
func canonicalIP(s string) (string, error) {
	a, err := netip.ParseAddr(s)
	if err != nil || a.Zone() != "" {
		return "", errArgs("invalid ip %q", s)
	}
	return a.String(), nil
}

// NormalizeIPs validates, canonicalises and de-duplicates a list of IPs
// (order preserved, empty entries skipped).
func NormalizeIPs(in []string) ([]string, error) {
	seen := map[string]bool{}
	out := make([]string, 0, len(in))
	for _, s := range in {
		s = strings.TrimSpace(s)
		if s == "" {
			continue
		}
		ip, err := NormalizeIP(s)
		if err != nil {
			return nil, err
		}
		if !seen[ip] {
			seen[ip] = true
			out = append(out, ip)
		}
	}
	return out, nil
}

var idRe = regexp.MustCompile(`^[A-Za-z0-9_-]{6,100}$`)

// ValidateID checks a RustDesk id.
func ValidateID(id string) error {
	if !idRe.MatchString(id) {
		return errArgs("invalid id %q", id)
	}
	return nil
}

var hostnameRe = regexp.MustCompile(`^(?i:[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?)(\.(?i:[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?))*\.?$`)

// MaxRelayServers bounds the relay server list.
const MaxRelayServers = 32

// MaxWeightedRelayServers bounds a weighted relay list (MaxRelayServers
// distinct relays, each repeated at most 5 times); the 1000-byte command
// line limit usually bites first.
const MaxWeightedRelayServers = MaxRelayServers * 5

// NormalizeRelayServers validates relay entries ("host", "host:port",
// "1.2.3.4", "[v6]:port"), trims blanks and de-duplicates. hbbs resolves each
// entry with DNS and silently drops failures, so only syntax is checked here.
func NormalizeRelayServers(in []string) ([]string, error) {
	return normalizeRelays(in, true)
}

// NormalizeWeightedRelayServers is NormalizeRelayServers without the
// de-duplication: hbbs picks relays round-robin over the list as given, so
// repeating an entry gives it more weight (computed relay rotation).
func NormalizeWeightedRelayServers(in []string) ([]string, error) {
	return normalizeRelays(in, false)
}

func normalizeRelays(in []string, dedupe bool) ([]string, error) {
	seen := map[string]bool{}
	out := make([]string, 0, len(in))
	for _, s := range in {
		s = strings.TrimSpace(s)
		if s == "" {
			continue
		}
		if err := validateRelay(s); err != nil {
			return nil, err
		}
		if !seen[s] || !dedupe {
			seen[s] = true
			out = append(out, s)
		}
	}
	if len(seen) > MaxRelayServers || len(out) > MaxWeightedRelayServers {
		return nil, errArgs("at most %d relay servers", MaxRelayServers)
	}
	return out, nil
}

func validateRelay(s string) error {
	if len(s) > 260 {
		return errArgs("relay server too long")
	}
	host := s
	if strings.Contains(s, ":") {
		h, p, err := net.SplitHostPort(s)
		if err != nil {
			// A bare IPv6 address would be mis-parsed by hbbs (it splits on ':').
			return errArgs("invalid relay server %q (use host:port or [ipv6]:port)", s)
		}
		port, err := strconv.Atoi(p)
		if err != nil || port < 1 || port > 65535 {
			return errArgs("invalid port in %q", s)
		}
		if a, err := netip.ParseAddr(h); err == nil {
			if a.Zone() != "" {
				return errArgs("invalid relay server %q", s)
			}
			return nil
		}
		host = h
	} else if _, err := netip.ParseAddr(s); err == nil {
		return nil
	}
	if len(host) > 253 || !hostnameRe.MatchString(host) {
		return errArgs("invalid relay server %q", s)
	}
	return nil
}
