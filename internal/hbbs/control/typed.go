package control

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"
)

func lines(s string) []string {
	var out []string
	for _, l := range strings.Split(s, "\n") {
		l = strings.TrimRight(l, "\r")
		if l != "" {
			out = append(out, l)
		}
	}
	return out
}

func badf(format string, a ...any) error {
	return fmt.Errorf("%w: %s", ErrBadResponse, fmt.Sprintf(format, a...))
}

func yn(b bool) string {
	if b {
		return "Y"
	}
	return "N"
}

// ---- hbbs ----

// RelayServers returns the relay servers hbbs currently hands out (after its
// DNS/connectivity filtering).
func (c *Client) RelayServers(ctx context.Context) ([]string, error) {
	out, err := c.Exec(ctx, HBBS, "relay-servers")
	if err != nil {
		return nil, err
	}
	return lines(out), nil
}

// SetRelayServers replaces the relay list. hbbs applies it asynchronously and
// silently drops entries that fail DNS resolution. Repeated entries are kept:
// hbbs round-robins over the list, so duplicates are weights.
func (c *Client) SetRelayServers(ctx context.Context, servers []string) error {
	norm, err := NormalizeWeightedRelayServers(servers)
	if err != nil {
		return err
	}
	if len(norm) == 0 {
		return fmt.Errorf("%w: hbbs cannot be given an empty relay list", ErrInvalidArgs)
	}
	_, err = c.Exec(ctx, HBBS, "relay-servers", strings.Join(norm, ","))
	return err
}

func parseFlag(out, prefix string) (bool, error) {
	l := lines(out)
	if len(l) != 1 {
		return false, badf("want %q line, got %q", prefix, out)
	}
	v, ok := strings.CutPrefix(l[0], prefix+": ")
	if !ok {
		return false, badf("want %q line, got %q", prefix, out)
	}
	switch v {
	case "true":
		return true, nil
	case "false":
		return false, nil
	}
	return false, badf("bad boolean %q", v)
}

// AlwaysUseRelay reads ALWAYS_USE_RELAY.
func (c *Client) AlwaysUseRelay(ctx context.Context) (bool, error) {
	out, err := c.Exec(ctx, HBBS, "always-use-relay")
	if err != nil {
		return false, err
	}
	return parseFlag(out, "ALWAYS_USE_RELAY")
}

// SetAlwaysUseRelay sets ALWAYS_USE_RELAY. Upstream hbbs (and this fork before
// the fix) also re-parses the relay list from the literal "Y"/"N", wiping it;
// callers must re-send the relay list afterwards (the reconciler does).
func (c *Client) SetAlwaysUseRelay(ctx context.Context, on bool) error {
	_, err := c.Exec(ctx, HBBS, "always-use-relay", yn(on))
	return err
}

// MustLogin reads MUST_LOGIN.
func (c *Client) MustLogin(ctx context.Context) (bool, error) {
	out, err := c.Exec(ctx, HBBS, "must-login")
	if err != nil {
		return false, err
	}
	return parseFlag(out, "MUST_LOGIN")
}

// SetMustLogin sets MUST_LOGIN.
func (c *Client) SetMustLogin(ctx context.Context, on bool) error {
	_, err := c.Exec(ctx, HBBS, "must-login", yn(on))
	return err
}

// TCPKeepalive reads TCP_KEEPALIVE (bropines/rustdesk-server fork): whether
// hbbs sends periodic empty frames on registered TCP/WebSocket rendezvous
// connections so clients in TCP/WebSocket mode stay online.
func (c *Client) TCPKeepalive(ctx context.Context) (bool, error) {
	out, err := c.Exec(ctx, HBBS, "tcp-keepalive")
	if err != nil {
		return false, err
	}
	return parseFlag(out, "TCP_KEEPALIVE")
}

// SetTCPKeepalive sets TCP_KEEPALIVE; it takes effect on the next keepalive
// tick of every open connection.
func (c *Client) SetTCPKeepalive(ctx context.Context, on bool) error {
	_, err := c.Exec(ctx, HBBS, "tcp-keepalive", yn(on))
	return err
}

// WebRTC reads WEBRTC (bropines/rustdesk-server fork): whether hbbs forwards
// WebRTC signaling (SDP offer/answer, trickle ICE) between RustDesk 1.5+ clients.
func (c *Client) WebRTC(ctx context.Context) (bool, error) {
	out, err := c.Exec(ctx, HBBS, "webrtc")
	if err != nil {
		return false, err
	}
	return parseFlag(out, "WEBRTC")
}

// SetWebRTC sets WEBRTC; it applies to the next punch request.
func (c *Client) SetWebRTC(ctx context.Context, on bool) error {
	_, err := c.Exec(ctx, HBBS, "webrtc", yn(on))
	return err
}

// IPBlockerEntry is one row of hbbs' registration rate limiter.
type IPBlockerEntry struct {
	IP string `json:"ip"`
	// Attempts is the number of RegisterPk calls in the current window and
	// AttemptsAge the age of that window.
	Attempts    int           `json:"attempts"`
	AttemptsAge time.Duration `json:"attempts_age_ns"`
	// DistinctIDs is the number of ids registered from this IP in the daily
	// window, IDsAge the age of that window.
	DistinctIDs int           `json:"distinct_ids"`
	IDsAge      time.Duration `json:"ids_age_ns"`
}

// IPBlockerPage is a page of the ip-blocker table.
type IPBlockerPage struct {
	Total   int              `json:"total"`
	Entries []IPBlockerEntry `json:"entries"`
}

// "a/t1s n/t2s"
func parseBlockerStats(s string, e *IPBlockerEntry) error {
	f := strings.Fields(s)
	if len(f) != 2 {
		return badf("bad ip-blocker stats %q", s)
	}
	var err1, err2 error
	e.Attempts, e.AttemptsAge, err1 = parseCountAge(f[0])
	e.DistinctIDs, e.IDsAge, err2 = parseCountAge(f[1])
	if err1 != nil || err2 != nil {
		return badf("bad ip-blocker stats %q", s)
	}
	return nil
}

func parseCountAge(s string) (int, time.Duration, error) {
	a, b, ok := strings.Cut(s, "/")
	if !ok {
		return 0, 0, badf("bad count/age %q", s)
	}
	n, err := strconv.Atoi(a)
	if err != nil {
		return 0, 0, err
	}
	d, err := parseSecs(b)
	return n, d, err
}

func parseSecs(s string) (time.Duration, error) {
	v, ok := strings.CutSuffix(s, "s")
	if !ok {
		return 0, badf("bad seconds %q", s)
	}
	n, err := strconv.ParseUint(v, 10, 63)
	if err != nil {
		return 0, badf("bad seconds %q", s)
	}
	return time.Duration(n) * time.Second, nil
}

func parseTotal(l []string) (int, error) {
	if len(l) == 0 {
		return 0, badf("empty reply")
	}
	n, err := strconv.Atoi(l[0])
	if err != nil {
		return 0, badf("bad count %q", l[0])
	}
	return n, nil
}

// IPBlocker lists up to 10 entries starting at offset start.
func (c *Client) IPBlocker(ctx context.Context, start int) (IPBlockerPage, error) {
	out, err := c.Exec(ctx, HBBS, "ip-blocker", strconv.Itoa(start))
	if err != nil {
		return IPBlockerPage{}, err
	}
	l := lines(out)
	total, err := parseTotal(l)
	if err != nil {
		return IPBlockerPage{}, err
	}
	page := IPBlockerPage{Total: total, Entries: []IPBlockerEntry{}}
	for _, row := range l[1:] {
		i := strings.LastIndex(row, ": ")
		if i < 0 {
			return IPBlockerPage{}, badf("bad ip-blocker row %q", row)
		}
		e := IPBlockerEntry{IP: row[:i]}
		if err := parseBlockerStats(row[i+2:], &e); err != nil {
			return IPBlockerPage{}, err
		}
		page.Entries = append(page.Entries, e)
	}
	return page, nil
}

// IPBlockerLookup returns the entry for ip (nil if absent). With remove the
// entry is also deleted (unblocking the IP).
func (c *Client) IPBlockerLookup(ctx context.Context, ip string, remove bool) (*IPBlockerEntry, error) {
	args := []string{ip}
	if remove {
		args = append(args, "-")
	}
	out, err := c.Exec(ctx, HBBS, "ip-blocker", args...)
	if err != nil {
		return nil, err
	}
	l := lines(out)
	if _, err := parseTotal(l); err != nil {
		return nil, err
	}
	if len(l) < 2 {
		return nil, nil
	}
	e := IPBlockerEntry{IP: ip}
	if err := parseBlockerStats(l[1], &e); err != nil {
		return nil, err
	}
	return &e, nil
}

// IPChange is one row of hbbs' "id seen from several IPs" table.
type IPChange struct {
	ID string `json:"id"`
	// Age is the age of the observation window.
	Age time.Duration `json:"age_ns"`
	// IPs maps each source IP to its registration count.
	IPs map[string]int `json:"ips"`
}

// IPChangesPage is a page of the ip-changes table.
type IPChangesPage struct {
	Total   int        `json:"total"`
	Entries []IPChange `json:"entries"`
}

// "{secs}s {"ip": n, ...}"
func parseIPChange(s string, e *IPChange) error {
	secs, m, ok := strings.Cut(s, " ")
	if !ok {
		return badf("bad ip-changes row %q", s)
	}
	d, err := parseSecs(secs)
	if err != nil {
		return err
	}
	e.Age = d
	e.IPs, err = parseRustStringIntMap(m)
	return err
}

// parseRustStringIntMap parses Rust's Debug format of HashMap<String, i32>:
// {"1.2.3.4": 3, "::1": 1}
func parseRustStringIntMap(s string) (map[string]int, error) {
	s = strings.TrimSpace(s)
	if len(s) < 2 || s[0] != '{' || s[len(s)-1] != '}' {
		return nil, badf("bad map %q", s)
	}
	out := map[string]int{}
	body := strings.TrimSpace(s[1 : len(s)-1])
	if body == "" {
		return out, nil
	}
	for _, kv := range strings.Split(body, ", ") {
		i := strings.LastIndex(kv, ": ")
		if i < 0 {
			return nil, badf("bad map entry %q", kv)
		}
		k, err := strconv.Unquote(kv[:i])
		if err != nil {
			return nil, badf("bad map key %q", kv[:i])
		}
		n, err := strconv.Atoi(kv[i+2:])
		if err != nil {
			return nil, badf("bad map value %q", kv[i+2:])
		}
		out[k] = n
	}
	return out, nil
}

// IPChanges lists up to 10 entries starting at offset start.
func (c *Client) IPChanges(ctx context.Context, start int) (IPChangesPage, error) {
	out, err := c.Exec(ctx, HBBS, "ip-changes", strconv.Itoa(start))
	if err != nil {
		return IPChangesPage{}, err
	}
	l := lines(out)
	total, err := parseTotal(l)
	if err != nil {
		return IPChangesPage{}, err
	}
	page := IPChangesPage{Total: total, Entries: []IPChange{}}
	for _, row := range l[1:] {
		id, rest, ok := strings.Cut(row, ": ")
		if !ok {
			return IPChangesPage{}, badf("bad ip-changes row %q", row)
		}
		e := IPChange{ID: id}
		if err := parseIPChange(rest, &e); err != nil {
			return IPChangesPage{}, err
		}
		page.Entries = append(page.Entries, e)
	}
	return page, nil
}

// IPChangesLookup returns the entry for id (nil if absent); remove deletes it.
// Ids that are integers <= 10,000,000 cannot be addressed (hbbs treats them as
// a page offset) and yield ErrInvalidArgs.
func (c *Client) IPChangesLookup(ctx context.Context, id string, remove bool) (*IPChange, error) {
	if n, err := strconv.ParseInt(id, 10, 32); err == nil && n >= 0 && n <= ipChangesMaxStart {
		return nil, fmt.Errorf("%w: id %q is read by hbbs as a page offset", ErrInvalidArgs, id)
	}
	args := []string{id}
	if remove {
		args = append(args, "-")
	}
	out, err := c.Exec(ctx, HBBS, "ip-changes", args...)
	if err != nil {
		return nil, err
	}
	l := lines(out)
	if _, err := parseTotal(l); err != nil {
		return nil, err
	}
	if len(l) < 2 {
		return nil, nil
	}
	e := IPChange{ID: id}
	if err := parseIPChange(l[1], &e); err != nil {
		return nil, err
	}
	return &e, nil
}

// PunchRequest is one connection attempt recorded by hbbs.
type PunchRequest struct {
	Time   time.Time `json:"time"`
	FromIP string    `json:"from_ip"`
	ToID   string    `json:"to_id"`
	ToIP   string    `json:"to_ip"`
}

// "2026-10-15T12:00:00Z 1.2.3.4 -> 123456789@5.6.7.8"
func parsePunchRequest(s string) (PunchRequest, error) {
	f := strings.Fields(s)
	if len(f) != 4 || f[2] != "->" {
		return PunchRequest{}, badf("bad punch request %q", s)
	}
	ts, err := time.Parse(time.RFC3339, f[0])
	if err != nil {
		return PunchRequest{}, badf("bad punch request time %q", f[0])
	}
	i := strings.LastIndex(f[3], "@")
	if i <= 0 {
		return PunchRequest{}, badf("bad punch request target %q", f[3])
	}
	return PunchRequest{Time: ts, FromIP: f[1], ToID: f[3][:i], ToIP: f[3][i+1:]}, nil
}

// PunchRequests returns up to pageSize recorded connection attempts starting
// at offset start (oldest first).
func (c *Client) PunchRequests(ctx context.Context, start, pageSize int) ([]PunchRequest, error) {
	out, err := c.Exec(ctx, HBBS, "punch-requests", strconv.Itoa(start), strconv.Itoa(pageSize))
	if err != nil {
		return nil, err
	}
	res := []PunchRequest{}
	for _, l := range lines(out) {
		pr, err := parsePunchRequest(l)
		if err != nil {
			return nil, err
		}
		res = append(res, pr)
	}
	return res, nil
}

// ClearPunchRequests empties hbbs' punch-request log.
func (c *Client) ClearPunchRequests(ctx context.Context) error {
	_, err := c.Exec(ctx, HBBS, "punch-requests", "-")
	return err
}

// TestGeo returns the relay server hbbs would pick for a connection between
// ip1 and ip2 (ip2 optional). Note: it advances hbbs' round-robin counter.
func (c *Client) TestGeo(ctx context.Context, ip1, ip2 string) (string, error) {
	args := []string{ip1}
	if ip2 != "" {
		args = append(args, ip2)
	}
	out, err := c.Exec(ctx, HBBS, "test-geo", args...)
	if err != nil {
		return "", err
	}
	out = strings.TrimSpace(out)
	if out == "" {
		return "", badf("empty test-geo reply")
	}
	// Rust Debug of a String: "relay.example.com:21117"
	if s, err := strconv.Unquote(out); err == nil {
		return s, nil
	}
	if len(out) >= 2 && out[0] == '"' && out[len(out)-1] == '"' {
		return out[1 : len(out)-1], nil
	}
	return "", badf("bad test-geo reply %q", out)
}

// ---- hbbr ----

// List selects one of hbbr's IP lists.
type List string

// hbbr lists: blacklisted IPs are throttled to limit-speed; blocklisted IPs
// are refused and cut from running relays.
const (
	Blacklist List = "blacklist"
	Blocklist List = "blocklist"
)

func (l List) valid() bool { return l == Blacklist || l == Blocklist }

// ListIPs returns the entries of an hbbr list.
func (c *Client) ListIPs(ctx context.Context, l List) ([]string, error) {
	if !l.valid() {
		return nil, fmt.Errorf("%w: list %q", ErrInvalidArgs, l)
	}
	out, err := c.Exec(ctx, HBBR, string(l))
	if err != nil {
		return nil, err
	}
	res := []string{}
	for _, ip := range lines(out) {
		if ip = strings.TrimSpace(ip); ip != "" {
			res = append(res, ip)
		}
	}
	return res, nil
}

// ListContains reports whether ip is on an hbbr list.
func (c *Client) ListContains(ctx context.Context, l List, ip string) (bool, error) {
	if !l.valid() {
		return false, fmt.Errorf("%w: list %q", ErrInvalidArgs, l)
	}
	out, err := c.Exec(ctx, HBBR, string(l), ip)
	if err != nil {
		return false, err
	}
	switch strings.TrimSpace(out) {
	case "true":
		return true, nil
	case "false":
		return false, nil
	}
	return false, badf("bad %s reply %q", l, out)
}

// ListAdd adds IPs to an hbbr list (batched to fit the 1 KiB command limit).
func (c *Client) ListAdd(ctx context.Context, l List, ips []string) error {
	return c.listBatch(ctx, l, string(l)+"-add", ips)
}

// ListRemove removes IPs from an hbbr list.
func (c *Client) ListRemove(ctx context.Context, l List, ips []string) error {
	return c.listBatch(ctx, l, string(l)+"-remove", ips)
}

// ListClear empties an hbbr list.
func (c *Client) ListClear(ctx context.Context, l List) error {
	if !l.valid() {
		return fmt.Errorf("%w: list %q", ErrInvalidArgs, l)
	}
	_, err := c.Exec(ctx, HBBR, string(l)+"-remove", "all")
	return err
}

func (c *Client) listBatch(ctx context.Context, l List, cmd string, ips []string) error {
	if !l.valid() {
		return fmt.Errorf("%w: list %q", ErrInvalidArgs, l)
	}
	norm, err := NormalizeIPs(ips)
	if err != nil {
		return err
	}
	for _, batch := range BatchIPs(norm, maxLine-len(cmd)-1) {
		if _, err := c.Exec(ctx, HBBR, cmd, strings.Join(batch, "|")); err != nil {
			return err
		}
	}
	return nil
}

// BatchIPs splits ips into groups whose "|"-joined length is <= limit.
func BatchIPs(ips []string, limit int) [][]string {
	var out [][]string
	var cur []string
	n := 0
	for _, ip := range ips {
		add := len(ip)
		if len(cur) > 0 {
			add++
		}
		if len(cur) > 0 && n+add > limit {
			out = append(out, cur)
			cur, n, add = nil, 0, len(ip)
		}
		cur = append(cur, ip)
		n += add
	}
	if len(cur) > 0 {
		out = append(out, cur)
	}
	return out
}

func parseNumberWithSuffix(out, suffix string) (float64, error) {
	l := lines(out)
	if len(l) != 1 {
		return 0, badf("want one line, got %q", out)
	}
	v, ok := strings.CutSuffix(l[0], suffix)
	if !ok {
		return 0, badf("want suffix %q in %q", suffix, l[0])
	}
	f, err := strconv.ParseFloat(v, 64)
	if err != nil {
		return 0, badf("bad number %q", l[0])
	}
	return f, nil
}

// Rate selects one of hbbr's bandwidth settings (Mb/s, really Mibit/s).
type Rate string

// hbbr rates. limit-speed applies to blacklisted/downgraded relays and
// single-bandwidth to each relay (both for new relays only); total-bandwidth
// is global and applies immediately.
const (
	LimitSpeed      Rate = "limit-speed"
	TotalBandwidth  Rate = "total-bandwidth"
	SingleBandwidth Rate = "single-bandwidth"
)

// GetRate reads a bandwidth setting in Mb/s.
func (c *Client) GetRate(ctx context.Context, r Rate) (float64, error) {
	if r != LimitSpeed && r != TotalBandwidth && r != SingleBandwidth {
		return 0, fmt.Errorf("%w: rate %q", ErrInvalidArgs, r)
	}
	out, err := c.Exec(ctx, HBBR, string(r))
	if err != nil {
		return 0, err
	}
	return parseNumberWithSuffix(out, "Mb/s")
}

// SetRate sets a bandwidth setting in Mb/s (> 0).
func (c *Client) SetRate(ctx context.Context, r Rate, mbps float64) error {
	if r != LimitSpeed && r != TotalBandwidth && r != SingleBandwidth {
		return fmt.Errorf("%w: rate %q", ErrInvalidArgs, r)
	}
	_, err := c.Exec(ctx, HBBR, string(r), formatFloat(mbps))
	return err
}

// DowngradeThreshold reads the downgrade threshold ratio (hbbr keeps two decimals).
func (c *Client) DowngradeThreshold(ctx context.Context) (float64, error) {
	out, err := c.Exec(ctx, HBBR, "downgrade-threshold")
	if err != nil {
		return 0, err
	}
	return parseNumberWithSuffix(out, "")
}

// SetDowngradeThreshold sets the downgrade threshold ratio (> 0).
func (c *Client) SetDowngradeThreshold(ctx context.Context, v float64) error {
	_, err := c.Exec(ctx, HBBR, "downgrade-threshold", formatFloat(v))
	return err
}

// DowngradeStartCheck reads how long a relay runs before downgrade checks start.
func (c *Client) DowngradeStartCheck(ctx context.Context) (time.Duration, error) {
	out, err := c.Exec(ctx, HBBR, "downgrade-start-check")
	if err != nil {
		return 0, err
	}
	l := lines(out)
	if len(l) != 1 {
		return 0, badf("want one line, got %q", out)
	}
	return parseSecs(l[0])
}

// SetDowngradeStartCheck sets the downgrade start delay (whole seconds, > 0).
func (c *Client) SetDowngradeStartCheck(ctx context.Context, d time.Duration) error {
	secs := int64(d / time.Second)
	_, err := c.Exec(ctx, HBBR, "downgrade-start-check", strconv.FormatInt(secs, 10))
	return err
}

// RelayUsage is one active relay reported by hbbr "usage".
type RelayUsage struct {
	Addr    string        `json:"addr"`
	Elapsed time.Duration `json:"elapsed_ns"`
	TotalMB float64       `json:"total_mb"`
	// Rates in kb/s as printed by hbbr.
	HighestKbps int64 `json:"highest_kbps"`
	AvgKbps     int64 `json:"avg_kbps"`
	CurrentKbps int64 `json:"current_kbps"`
}

// "{ip}:{port}: {elapsed}s {total:.2}MB {highest}kb/s {avg}kb/s {current}kb/s"
func parseUsage(s string) (RelayUsage, error) {
	addr, rest, ok := strings.Cut(s, ": ")
	f := strings.Fields(rest)
	if !ok || len(f) != 5 {
		return RelayUsage{}, badf("bad usage row %q", s)
	}
	u := RelayUsage{Addr: addr}
	var err error
	if u.Elapsed, err = parseSecs(f[0]); err != nil {
		return RelayUsage{}, err
	}
	mb, ok := strings.CutSuffix(f[1], "MB")
	if u.TotalMB, err = strconv.ParseFloat(mb, 64); !ok || err != nil {
		return RelayUsage{}, badf("bad usage total %q", f[1])
	}
	for i, dst := range []*int64{&u.HighestKbps, &u.AvgKbps, &u.CurrentKbps} {
		v, ok := strings.CutSuffix(f[2+i], "kb/s")
		n, err := strconv.ParseInt(v, 10, 64)
		if !ok || err != nil {
			return RelayUsage{}, badf("bad usage rate %q", f[2+i])
		}
		*dst = n
	}
	return u, nil
}

// Usage returns hbbr's active relays, busiest first.
func (c *Client) Usage(ctx context.Context) ([]RelayUsage, error) {
	out, err := c.Exec(ctx, HBBR, "usage")
	if err != nil {
		return nil, err
	}
	res := []RelayUsage{}
	for _, l := range lines(out) {
		u, err := parseUsage(l)
		if err != nil {
			return nil, err
		}
		res = append(res, u)
	}
	return res, nil
}

// RelayStats represents cumulative process-lifetime statistics reported by hbbr stats.
type RelayStats struct {
	BytesTotal    uint64 `json:"bytes_total"`
	RelaysTotal   uint64 `json:"relays_total"`
	RelaysActive  int    `json:"relays_active"`
	Waiting       int    `json:"waiting"`
	AuthFailed    uint64 `json:"auth_failed"`
	Blocked       uint64 `json:"blocked"`
	Downgraded    uint64 `json:"downgraded"`
	UptimeSeconds uint64 `json:"uptime_s"`
	Version       string `json:"version"`
}

func parseStats(raw string) (*RelayStats, error) {
	st := &RelayStats{}
	for _, line := range lines(raw) {
		k, v, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		switch k {
		case "bytes_total":
			st.BytesTotal, _ = strconv.ParseUint(v, 10, 64)
		case "relays_total":
			st.RelaysTotal, _ = strconv.ParseUint(v, 10, 64)
		case "relays_active":
			st.RelaysActive, _ = strconv.Atoi(v)
		case "waiting":
			st.Waiting, _ = strconv.Atoi(v)
		case "auth_failed":
			st.AuthFailed, _ = strconv.ParseUint(v, 10, 64)
		case "blocked":
			st.Blocked, _ = strconv.ParseUint(v, 10, 64)
		case "downgraded":
			st.Downgraded, _ = strconv.ParseUint(v, 10, 64)
		case "uptime_s":
			st.UptimeSeconds, _ = strconv.ParseUint(v, 10, 64)
		case "version":
			st.Version = v
		}
	}
	return st, nil
}

// Stats returns cumulative process-lifetime statistics from hbbr.
func (c *Client) Stats(ctx context.Context) (*RelayStats, error) {
	out, err := c.Exec(ctx, HBBR, "stats")
	if err != nil {
		return nil, err
	}
	return parseStats(out)
}

// ReloadGeo triggers hbbs to reload its relay_map.txt prefix routing table.
func (c *Client) ReloadGeo(ctx context.Context) (string, error) {
	return c.Exec(ctx, HBBS, "reload-geo")
}

