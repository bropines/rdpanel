// Package mail sends the panel's transactional e-mails (address
// verification, sign-in codes, password reset, test message) over SMTP with
// net/smtp: implicit TLS, STARTTLS or plain, PLAIN/LOGIN authentication, and
// multipart/alternative (text + minimal HTML) bodies.
package mail

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/tls"
	"encoding/base64"
	"errors"
	"fmt"
	"mime"
	"mime/multipart"
	"mime/quotedprintable"
	"net"
	"net/mail"
	"net/smtp"
	"net/textproto"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/bropines/rdpanel/internal/config"
)

// Timeouts of one delivery.
const (
	dialTimeout = 10 * time.Second
	sendTimeout = 30 * time.Second
)

// Message is one e-mail to a single recipient.
type Message struct {
	To      string
	Subject string
	Text    string
	// HTML is optional; when set the body is multipart/alternative.
	HTML string
}

// Sender delivers messages.
type Sender interface {
	Send(ctx context.Context, m Message) error
}

// SenderFunc adapts a function to Sender.
type SenderFunc func(ctx context.Context, m Message) error

// Send implements Sender.
func (f SenderFunc) Send(ctx context.Context, m Message) error { return f(ctx, m) }

// SMTP is a Sender for one server configuration.
type SMTP struct {
	Config config.SMTPConfig
	// Now is the clock for the Date header (default time.Now).
	Now func() time.Time
}

// NewSMTP returns a Sender delivering through cfg.
func NewSMTP(cfg config.SMTPConfig) *SMTP { return &SMTP{Config: cfg} }

// ErrNotConfigured is returned when no SMTP host is set.
var ErrNotConfigured = errors.New("mail: SMTP is not configured")

// Validate checks the fields Send relies on.
func Validate(cfg config.SMTPConfig) error {
	if cfg.Host == "" {
		return ErrNotConfigured
	}
	if strings.ContainsAny(cfg.Host, " \r\n/") {
		return fmt.Errorf("mail: invalid host %q", cfg.Host)
	}
	if cfg.Port < 1 || cfg.Port > 65535 {
		return fmt.Errorf("mail: invalid port %d", cfg.Port)
	}
	switch cfg.Encryption {
	case config.SMTPEncryptionNone, config.SMTPEncryptionSTARTTLS, config.SMTPEncryptionTLS:
	default:
		return fmt.Errorf("mail: invalid encryption %q", cfg.Encryption)
	}
	if _, err := mail.ParseAddress(cfg.From); err != nil {
		return fmt.Errorf("mail: invalid from address: %w", err)
	}
	return nil
}

// Send implements Sender.
func (s *SMTP) Send(ctx context.Context, m Message) error {
	cfg := s.Config
	if err := Validate(cfg); err != nil {
		return err
	}
	from, err := mail.ParseAddress(cfg.From)
	if err != nil {
		return fmt.Errorf("mail: from: %w", err)
	}
	to, err := mail.ParseAddress(m.To)
	if err != nil {
		return fmt.Errorf("mail: recipient: %w", err)
	}
	now := time.Now
	if s.Now != nil {
		now = s.Now
	}
	body, err := Build(from, to, m, now())
	if err != nil {
		return err
	}
	if _, ok := ctx.Deadline(); !ok {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, sendTimeout)
		defer cancel()
	}
	return deliver(ctx, cfg, from.Address, to.Address, body)
}

func deliver(ctx context.Context, cfg config.SMTPConfig, from, to string, body []byte) error {
	addr := net.JoinHostPort(cfg.Host, strconv.Itoa(cfg.Port))
	tlsCfg := &tls.Config{ServerName: cfg.Host, InsecureSkipVerify: cfg.InsecureSkipVerify, MinVersion: tls.VersionTLS12} //nolint:gosec // opt-in
	dialer := &net.Dialer{Timeout: dialTimeout}
	var (
		conn net.Conn
		err  error
	)
	if cfg.Encryption == config.SMTPEncryptionTLS {
		conn, err = (&tls.Dialer{NetDialer: dialer, Config: tlsCfg}).DialContext(ctx, "tcp", addr)
	} else {
		conn, err = dialer.DialContext(ctx, "tcp", addr)
	}
	if err != nil {
		return fmt.Errorf("mail: connect %s: %w", addr, err)
	}
	if dl, ok := ctx.Deadline(); ok {
		_ = conn.SetDeadline(dl)
	}
	// Abort a stuck exchange when ctx is cancelled.
	stop := context.AfterFunc(ctx, func() { _ = conn.SetDeadline(time.Unix(1, 0)) })
	defer stop()

	c, err := smtp.NewClient(conn, cfg.Host)
	if err != nil {
		_ = conn.Close()
		return fmt.Errorf("mail: greeting: %w", err)
	}
	defer c.Close()
	if err := c.Hello(helloName()); err != nil {
		return fmt.Errorf("mail: EHLO: %w", err)
	}
	if cfg.Encryption == config.SMTPEncryptionSTARTTLS {
		if ok, _ := c.Extension("STARTTLS"); !ok {
			return errors.New("mail: server does not offer STARTTLS (set encryption to tls or none)")
		}
		if err := c.StartTLS(tlsCfg); err != nil {
			return fmt.Errorf("mail: STARTTLS: %w", err)
		}
	}
	if cfg.Username != "" {
		if ok, _ := c.Extension("AUTH"); !ok {
			return errors.New("mail: server does not offer AUTH")
		}
		if err := c.Auth(&credAuth{user: cfg.Username, pass: cfg.Password.Reveal()}); err != nil {
			return fmt.Errorf("mail: authentication failed: %w", err)
		}
	}
	if err := c.Mail(from); err != nil {
		return fmt.Errorf("mail: MAIL FROM: %w", err)
	}
	if err := c.Rcpt(to); err != nil {
		return fmt.Errorf("mail: RCPT TO: %w", err)
	}
	w, err := c.Data()
	if err != nil {
		return fmt.Errorf("mail: DATA: %w", err)
	}
	if _, err := w.Write(body); err != nil {
		return fmt.Errorf("mail: write: %w", err)
	}
	if err := w.Close(); err != nil {
		return fmt.Errorf("mail: message rejected: %w", err)
	}
	_ = c.Quit()
	return nil
}

func helloName() string {
	h, err := os.Hostname()
	if err != nil || h == "" || strings.ContainsAny(h, " \r\n") {
		return "localhost"
	}
	return h
}

// credAuth is SMTP AUTH PLAIN (preferred) or LOGIN. Unlike smtp.PlainAuth it
// does not refuse unencrypted connections: encryption "none" is an explicit
// administrator choice (typically a relay on a private network).
type credAuth struct {
	user, pass string
	mech       string
}

func (a *credAuth) Start(si *smtp.ServerInfo) (string, []byte, error) {
	has := func(m string) bool {
		for _, x := range si.Auth {
			if strings.EqualFold(x, m) {
				return true
			}
		}
		return false
	}
	switch {
	case has("PLAIN"):
		a.mech = "PLAIN"
		return "PLAIN", []byte("\x00" + a.user + "\x00" + a.pass), nil
	case has("LOGIN"):
		a.mech = "LOGIN"
		return "LOGIN", nil, nil
	}
	return "", nil, fmt.Errorf("no supported mechanism (server offers %s)", strings.Join(si.Auth, ", "))
}

func (a *credAuth) Next(fromServer []byte, more bool) ([]byte, error) {
	if !more {
		return nil, nil
	}
	if a.mech != "LOGIN" {
		return nil, errors.New("unexpected server challenge")
	}
	prompt := strings.ToLower(string(fromServer))
	switch {
	case strings.Contains(prompt, "username"):
		return []byte(a.user), nil
	case strings.Contains(prompt, "password"):
		return []byte(a.pass), nil
	}
	return nil, fmt.Errorf("unexpected LOGIN prompt %q", fromServer)
}

// headerSafe rejects CR/LF (header injection).
func headerSafe(s string) string {
	return strings.Map(func(r rune) rune {
		if r == '\r' || r == '\n' {
			return ' '
		}
		return r
	}, s)
}

// Build renders the RFC 5322 message (CRLF line endings).
func Build(from, to *mail.Address, m Message, now time.Time) ([]byte, error) {
	var buf bytes.Buffer
	hdr := func(k, v string) { buf.WriteString(k + ": " + v + "\r\n") }
	hdr("From", from.String())
	hdr("To", to.String())
	hdr("Subject", mime.QEncoding.Encode("utf-8", headerSafe(m.Subject)))
	hdr("Date", now.Format(time.RFC1123Z))
	hdr("Message-ID", messageID(from.Address))
	hdr("MIME-Version", "1.0")
	hdr("Auto-Submitted", "auto-generated")
	if m.HTML == "" {
		hdr("Content-Type", "text/plain; charset=utf-8")
		hdr("Content-Transfer-Encoding", "quoted-printable")
		buf.WriteString("\r\n")
		if err := writeQP(&buf, m.Text); err != nil {
			return nil, err
		}
		return buf.Bytes(), nil
	}
	mw := multipart.NewWriter(&buf)
	hdr("Content-Type", `multipart/alternative; boundary="`+mw.Boundary()+`"`)
	buf.WriteString("\r\n")
	for _, part := range []struct{ ctype, body string }{
		{"text/plain; charset=utf-8", m.Text}, {"text/html; charset=utf-8", m.HTML},
	} {
		pw, err := mw.CreatePart(textproto.MIMEHeader{
			"Content-Type": {part.ctype}, "Content-Transfer-Encoding": {"quoted-printable"},
		})
		if err != nil {
			return nil, err
		}
		if err := writeQP(pw, part.body); err != nil {
			return nil, err
		}
	}
	if err := mw.Close(); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

func writeQP(w interface{ Write([]byte) (int, error) }, s string) error {
	qp := quotedprintable.NewWriter(w)
	s = strings.ReplaceAll(strings.ReplaceAll(s, "\r\n", "\n"), "\n", "\r\n")
	if _, err := qp.Write([]byte(s)); err != nil {
		return err
	}
	return qp.Close()
}

func messageID(from string) string {
	domain := "rdpanel.local"
	if i := strings.LastIndexByte(from, '@'); i >= 0 && i < len(from)-1 {
		domain = from[i+1:]
	}
	var b [16]byte
	_, _ = rand.Read(b[:])
	return "<" + base64.RawURLEncoding.EncodeToString(b[:]) + "@" + domain + ">"
}
