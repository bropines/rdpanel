// Package control is a typed client for the hbbs/hbbr loopback admin
// channels (docs/research/hbbs-integration.md §1).
//
// Protocol: connect to the hbbs admin port (rendezvous port - 1, default
// 21115) or the hbbr relay port (21117) FROM LOOPBACK, send one plain-text
// command line in a single write (hbbs/hbbr do exactly one read() of up to
// 1024 bytes with a 1 s timeout), then read the reply until the server closes
// the connection. Setters and unknown commands reply with nothing.
//
// Only allow-listed commands with validated arguments are sent (see
// Commands). Every call is logged and handed to an AuditSink.
package control

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/netip"
	"strings"
	"sync"
	"sync/atomic"
	"time"
	"unicode/utf8"
)

// Defaults.
const (
	DefaultTimeout     = 3 * time.Second
	DefaultMaxResponse = 256 << 10
)

var (
	// ErrResponseTooLarge is returned when a reply exceeds MaxResponse.
	ErrResponseTooLarge = errors.New("control: response too large")
	// ErrBadResponse is returned when a reply cannot be parsed.
	ErrBadResponse = errors.New("control: unexpected response")
	// ErrNotLoopback is returned by New for non-loopback addresses.
	ErrNotLoopback = errors.New("control: admin address must be loopback (hbbs/hbbr only accept admin commands from 127.0.0.0/8 or ::1)")
)

// Actor identifies who issued a command (for the audit trail).
type Actor struct {
	UserID *int64 `json:"user_id,omitempty"`
	Name   string `json:"name"`
	IP     string `json:"ip,omitempty"`
}

// SystemActor is used when no actor is attached to the context.
var SystemActor = Actor{Name: "system"}

type actorKey struct{}

// WithActor attaches the acting user to ctx.
func WithActor(ctx context.Context, a Actor) context.Context {
	return context.WithValue(ctx, actorKey{}, a)
}

// ActorFrom returns the actor attached to ctx, or SystemActor.
func ActorFrom(ctx context.Context) Actor {
	if a, ok := ctx.Value(actorKey{}).(Actor); ok {
		return a
	}
	return SystemActor
}

// CommandRecord describes one admin-channel call.
type CommandRecord struct {
	Time    time.Time `json:"time"`
	Target  Target    `json:"target"`
	Command string    `json:"command"` // canonical full name
	Args    []string  `json:"args,omitempty"`
	// Mutating is true for calls that change hbbs/hbbr state.
	Mutating bool `json:"mutating"`
	// Raw is true for calls made through the admin console (ExecLine).
	Raw           bool          `json:"raw"`
	Actor         Actor         `json:"actor"`
	Duration      time.Duration `json:"duration_ns"`
	ResponseBytes int           `json:"response_bytes"`
	// ResponseSHA256 is the hex digest of the reply (the reply itself may be large).
	ResponseSHA256 string `json:"response_sha256,omitempty"`
	Err            string `json:"error,omitempty"`
}

// AuditSink records admin-channel calls. adminapi implements it on top of
// admin_audit (typically persisting Mutating or Raw records only).
type AuditSink interface {
	RecordCommand(ctx context.Context, rec CommandRecord)
}

// NopAudit discards records.
type NopAudit struct{}

// RecordCommand implements AuditSink.
func (NopAudit) RecordCommand(context.Context, CommandRecord) {}

// Config configures a Client.
type Config struct {
	// HBBSAddr is the hbbs admin address (default 127.0.0.1:21115).
	// "unix:/path" dials a unix socket forwarder instead.
	HBBSAddr string
	// HBBRAddr is the hbbr admin address (default 127.0.0.1:21117).
	HBBRAddr    string
	Timeout     time.Duration
	MaxResponse int
	Audit       AuditSink
	Log         *slog.Logger
	// Dial is injectable for tests.
	Dial func(ctx context.Context, network, addr string) (net.Conn, error)
}

// Client talks to the hbbs/hbbr admin channels. Calls to the same target are
// serialized. It is safe for concurrent use.
type Client struct {
	cfg   Config
	mu    map[Target]*sync.Mutex
	now   func() time.Time
	addr  map[Target]string
	audit atomic.Pointer[auditBox]
}

type auditBox struct{ AuditSink }

// SetAudit replaces the audit sink (nil restores NopAudit). The admin API
// installs its admin_audit-backed sink this way after construction.
func (c *Client) SetAudit(a AuditSink) {
	if a == nil {
		a = NopAudit{}
	}
	c.audit.Store(&auditBox{a})
}

// New validates cfg (loopback-only addresses) and returns a Client.
func New(cfg Config) (*Client, error) {
	if cfg.HBBSAddr == "" {
		cfg.HBBSAddr = "127.0.0.1:21115"
	}
	if cfg.HBBRAddr == "" {
		cfg.HBBRAddr = "127.0.0.1:21117"
	}
	for _, a := range []string{cfg.HBBSAddr, cfg.HBBRAddr} {
		if err := checkLoopback(a); err != nil {
			return nil, fmt.Errorf("%w: %q", err, a)
		}
	}
	if cfg.Timeout <= 0 {
		cfg.Timeout = DefaultTimeout
	}
	if cfg.MaxResponse <= 0 {
		cfg.MaxResponse = DefaultMaxResponse
	}
	if cfg.Log == nil {
		cfg.Log = slog.New(slog.NewTextHandler(io.Discard, nil))
	}
	if cfg.Dial == nil {
		d := &net.Dialer{}
		cfg.Dial = d.DialContext
	}
	c := &Client{
		cfg:  cfg,
		mu:   map[Target]*sync.Mutex{HBBS: {}, HBBR: {}},
		now:  time.Now,
		addr: map[Target]string{HBBS: cfg.HBBSAddr, HBBR: cfg.HBBRAddr},
	}
	c.SetAudit(cfg.Audit)
	return c, nil
}

func checkLoopback(addr string) error {
	if strings.HasPrefix(addr, "unix:") {
		if len(addr) == len("unix:") {
			return errors.New("control: empty unix socket path")
		}
		return nil
	}
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		return err
	}
	if host == "localhost" {
		return nil
	}
	a, err := netip.ParseAddr(host)
	if err != nil || !a.Unmap().IsLoopback() {
		return ErrNotLoopback
	}
	return nil
}

// Addr returns the configured address of t.
func (c *Client) Addr(t Target) string { return c.addr[t] }

// Exec runs an allow-listed command with validated arguments and returns the
// raw reply. cmd may be the full name or the alias.
func (c *Client) Exec(ctx context.Context, t Target, cmd string, args ...string) (string, error) {
	return c.exec(ctx, t, false, cmd, args)
}

// ExecLine parses and runs one console line ("ml Y", "blacklist 1.2.3.4") for
// the admin console. Only allow-listed commands are accepted.
func (c *Client) ExecLine(ctx context.Context, t Target, line string) (string, error) {
	f := strings.Fields(line)
	if len(f) == 0 {
		return "", fmt.Errorf("%w: empty command", ErrNotAllowed)
	}
	return c.exec(ctx, t, true, f[0], f[1:])
}

func (c *Client) exec(ctx context.Context, t Target, raw bool, cmd string, args []string) (string, error) {
	if !t.Valid() {
		return "", fmt.Errorf("%w: unknown target %q", ErrNotAllowed, t)
	}
	spec, ok := lookupSpec(t, cmd)
	if !ok {
		return "", fmt.Errorf("%w: %s %q", ErrNotAllowed, t, cmd)
	}
	norm, mutating, err := spec.validate(args)
	if err != nil {
		return "", err
	}
	line := strings.Join(append([]string{spec.Name}, norm...), " ")
	if len(line) > maxLine {
		return "", fmt.Errorf("%w: command longer than %d bytes", ErrInvalidArgs, maxLine)
	}

	start := c.now()
	resp, err := c.roundTrip(ctx, t, line)
	rec := CommandRecord{
		Time: start.UTC(), Target: t, Command: spec.Name, Args: norm, Mutating: mutating, Raw: raw,
		Actor: ActorFrom(ctx), Duration: c.now().Sub(start), ResponseBytes: len(resp),
	}
	if len(resp) > 0 {
		sum := sha256.Sum256([]byte(resp))
		rec.ResponseSHA256 = hex.EncodeToString(sum[:])
	}
	level := slog.LevelDebug
	if mutating || raw {
		level = slog.LevelInfo
	}
	if err != nil {
		rec.Err = err.Error()
		level = slog.LevelWarn
	}
	c.cfg.Log.LogAttrs(ctx, level, "hbb admin command",
		slog.String("target", string(t)), slog.String("cmd", spec.Name), slog.Any("args", norm),
		slog.Bool("mutating", mutating), slog.Bool("raw", raw), slog.String("actor", rec.Actor.Name),
		slog.Duration("duration", rec.Duration), slog.Int("response_bytes", len(resp)), slog.Any("err", err))
	c.audit.Load().RecordCommand(ctx, rec)
	return resp, err
}

// roundTrip sends one line and reads the reply until EOF.
func (c *Client) roundTrip(ctx context.Context, t Target, line string) (string, error) {
	mu := c.mu[t]
	mu.Lock()
	defer mu.Unlock()

	ctx, cancel := context.WithTimeout(ctx, c.cfg.Timeout)
	defer cancel()
	network, addr := "tcp", c.addr[t]
	if p, ok := strings.CutPrefix(addr, "unix:"); ok {
		network, addr = "unix", p
	}
	conn, err := c.cfg.Dial(ctx, network, addr)
	if err != nil {
		return "", fmt.Errorf("control: %s: %w", t, err)
	}
	defer conn.Close()
	if dl, ok := ctx.Deadline(); ok {
		_ = conn.SetDeadline(dl)
	}
	stop := context.AfterFunc(ctx, func() { _ = conn.SetDeadline(time.Unix(1, 0)) })
	defer stop()

	// One Write: the server performs a single read().
	if _, err := conn.Write([]byte(line)); err != nil {
		return "", fmt.Errorf("control: %s: write: %w", t, err)
	}
	b, err := io.ReadAll(io.LimitReader(conn, int64(c.cfg.MaxResponse)+1))
	if err != nil {
		if ctx.Err() != nil && errors.Is(ctx.Err(), context.DeadlineExceeded) {
			return "", fmt.Errorf("control: %s: timeout waiting for reply (is the address the loopback admin port?): %w", t, err)
		}
		return "", fmt.Errorf("control: %s: read: %w", t, err)
	}
	if len(b) > c.cfg.MaxResponse {
		return "", fmt.Errorf("%w (> %d bytes)", ErrResponseTooLarge, c.cfg.MaxResponse)
	}
	if !utf8.Valid(b) {
		return "", fmt.Errorf("%w: reply is not UTF-8", ErrBadResponse)
	}
	return string(b), nil
}

// Ping checks that the admin channel of t answers (the help command always
// returns text) and returns the round-trip time.
func (c *Client) Ping(ctx context.Context, t Target) (time.Duration, error) {
	start := time.Now()
	out, err := c.Exec(ctx, t, "h")
	if err != nil {
		return 0, err
	}
	if strings.TrimSpace(out) == "" {
		return 0, fmt.Errorf("%w: empty help reply from %s", ErrBadResponse, t)
	}
	return time.Since(start), nil
}
