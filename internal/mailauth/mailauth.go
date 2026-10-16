// Package mailauth implements the e-mail based account features on top of
// internal/mail: the effective SMTP configuration (environment/config file,
// or the panel settings with an AEAD-sealed password), address verification
// codes, the e-mail second factor (panel login and the RustDesk client
// /api/login "email_check" step) and password reset links.
//
// Codes are 6 random digits, stored as sha256 bound to user and purpose,
// valid 10 minutes, invalidated after 5 wrong guesses and by a newer code.
// Sending is limited by a resend cooldown and hourly caps per user and per
// client IP (counted in email_codes, so limits survive restarts).
package mailauth

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"math/big"
	"net/mail"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/bropines/rustdesk-panel/internal/config"
	"github.com/bropines/rustdesk-panel/internal/crypto"
	rdmail "github.com/bropines/rustdesk-panel/internal/mail"
	"github.com/bropines/rustdesk-panel/internal/store"
)

// Tuning.
const (
	CodeTTL        = 10 * time.Minute
	ResetTTL       = 30 * time.Minute
	MaxAttempts    = 5
	ResendCooldown = time.Minute
	// MaxPerUserHour caps e-mails sent to one user (all purposes) per hour.
	MaxPerUserHour = 10
	// MaxPerIPHour caps e-mails triggered from one client IP per hour.
	MaxPerIPHour = 30
	// asyncSendTimeout bounds a background (password reset) delivery.
	asyncSendTimeout = time.Minute
)

// Settings keys (JSON values in the settings table).
const (
	// SettingSMTP holds the panel-managed SMTP server (storedSMTP); ignored
	// while the configuration (RDPANEL_SMTP_HOST / smtp.host) defines one.
	SettingSMTP = "mail.smtp"
	// SettingDefaultLocale is the e-mail language for users without one.
	SettingDefaultLocale = "mail.default_locale"
	// SettingRequireEmail2FA (bool): users without TOTP but with a verified
	// address must enter an e-mailed code at sign-in.
	SettingRequireEmail2FA = "auth.require_email_2fa"
)

// defaultTitle mirrors adminapi.DefaultBrandingTitle (no import cycle).
const defaultTitle = "RustDesk Panel"

// Source tells where the effective SMTP configuration comes from.
type Source string

// Sources.
const (
	SourceNone     Source = "none"
	SourceConfig   Source = "config"
	SourceSettings Source = "settings"
)

// Errors.
var (
	// ErrDisabled: no SMTP server is configured.
	ErrDisabled = errors.New("mailauth: e-mail is not configured")
	// ErrNoAddress: the user has no e-mail address.
	ErrNoAddress = errors.New("mailauth: user has no e-mail address")
	// ErrSend wraps delivery failures.
	ErrSend = errors.New("mailauth: could not send e-mail")
	// ErrInvalidToken: unknown, used or expired reset token.
	ErrInvalidToken = errors.New("mailauth: invalid or expired token")
)

// RateLimitError is returned when a send is refused by the cooldown or the
// hourly caps.
type RateLimitError struct{ Wait time.Duration }

// RetryAfter returns how long to wait before the next send.
func (e *RateLimitError) RetryAfter() time.Duration { return e.Wait }

func (e *RateLimitError) Error() string {
	return fmt.Sprintf("mailauth: too many e-mails, retry in %s", e.Wait.Round(time.Second))
}

// Options configure a Service.
type Options struct {
	Store *store.Store
	AEAD  *crypto.AEAD
	// Config supplies smtp.* and admin_url (reset links).
	Config *config.Config
	Log    *slog.Logger
	// NewSender builds the Sender for a configuration (default mail.NewSMTP).
	NewSender func(config.SMTPConfig) rdmail.Sender
}

// Service implements the e-mail features. Safe for concurrent use.
type Service struct {
	st        *store.Store
	aead      *crypto.AEAD
	cfg       config.SMTPConfig
	adminURL  string
	log       *slog.Logger
	newSender func(config.SMTPConfig) rdmail.Sender
	wg        sync.WaitGroup
}

// New returns a Service.
func New(o Options) *Service {
	s := &Service{st: o.Store, aead: o.AEAD, log: o.Log, newSender: o.NewSender}
	if o.Config != nil {
		s.cfg, s.adminURL = o.Config.SMTP, strings.TrimRight(o.Config.AdminURL, "/")
		if s.adminURL == "" {
			s.adminURL = strings.TrimRight(o.Config.PublicURL, "/")
		}
	}
	if s.log == nil {
		s.log = slog.New(slog.NewTextHandler(io.Discard, nil))
	}
	if s.newSender == nil {
		s.newSender = func(c config.SMTPConfig) rdmail.Sender { return rdmail.NewSMTP(c) }
	}
	return s
}

// Wait blocks until background deliveries (password reset) finished (tests).
func (s *Service) Wait() { s.wg.Wait() }

func (s *Service) now() time.Time { return s.st.Now() }

// ---- SMTP configuration ----

// storedSMTP is the JSON value of SettingSMTP.
type storedSMTP struct {
	Host               string `json:"host"`
	Port               int    `json:"port"`
	Username           string `json:"username"`
	PasswordEnc        []byte `json:"password_enc,omitempty"`
	From               string `json:"from"`
	Encryption         string `json:"encryption"`
	InsecureSkipVerify bool   `json:"insecure_skip_verify"`
}

func (s *Service) loadStored(ctx context.Context) (*storedSMTP, error) {
	var v storedSMTP
	err := s.st.Settings.Get(ctx, SettingSMTP, &v)
	if errors.Is(err, store.ErrNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &v, nil
}

// SMTP returns the effective SMTP configuration and its source. The
// configuration file / environment wins over the panel settings.
func (s *Service) SMTP(ctx context.Context) (config.SMTPConfig, Source, error) {
	if s.cfg.Enabled() {
		return s.cfg, SourceConfig, nil
	}
	v, err := s.loadStored(ctx)
	if err != nil || v == nil || v.Host == "" {
		return config.SMTPConfig{}, SourceNone, err
	}
	c := config.SMTPConfig{Host: v.Host, Port: v.Port, Username: v.Username, From: v.From,
		Encryption: v.Encryption, InsecureSkipVerify: v.InsecureSkipVerify}
	if c.Encryption == "" {
		c.Encryption = config.SMTPEncryptionSTARTTLS
	}
	if c.Port == 0 {
		c.Port = config.DefaultSMTPPort(c.Encryption)
	}
	if len(v.PasswordEnc) > 0 {
		if s.aead == nil {
			return config.SMTPConfig{}, SourceNone, errors.New("mailauth: no AEAD key for the stored SMTP password")
		}
		pw, err := s.aead.OpenString(v.PasswordEnc, crypto.SMTPPasswordAD())
		if err != nil {
			return config.SMTPConfig{}, SourceNone, fmt.Errorf("mailauth: open SMTP password: %w", err)
		}
		c.Password = config.Secret(pw)
	}
	return c, SourceSettings, nil
}

// Enabled reports whether e-mail can be sent (an SMTP host is configured).
func (s *Service) Enabled(ctx context.Context) bool {
	c, _, err := s.SMTP(ctx)
	if err != nil {
		s.log.WarnContext(ctx, "mail: load SMTP settings", slog.Any("err", err))
	}
	return err == nil && c.Enabled()
}

// SMTPView is the SMTP configuration without the password (admin UI).
type SMTPView struct {
	Source             Source
	Host               string
	Port               int
	Username           string
	PasswordSet        bool
	From               string
	Encryption         string
	InsecureSkipVerify bool
}

// View returns the effective configuration for display.
func (s *Service) View(ctx context.Context) (SMTPView, error) {
	c, src, err := s.SMTP(ctx)
	if err != nil {
		return SMTPView{}, err
	}
	if src == SourceNone {
		// Show an incomplete stored draft (host cleared) as empty.
		return SMTPView{Source: SourceNone, Port: config.DefaultSMTPPort(config.SMTPEncryptionSTARTTLS),
			Encryption: config.SMTPEncryptionSTARTTLS}, nil
	}
	return SMTPView{Source: src, Host: c.Host, Port: c.Port, Username: c.Username, PasswordSet: c.Password.IsSet(),
		From: c.From, Encryption: c.Encryption, InsecureSkipVerify: c.InsecureSkipVerify}, nil
}

// SMTPInput is an update of the panel-managed SMTP configuration.
type SMTPInput struct {
	Host     string
	Port     int
	Username string
	// Password: nil keeps the stored one, "" clears it.
	Password           *string
	From               string
	Encryption         string
	InsecureSkipVerify bool
}

// ValidateInput normalises in and returns per-field errors.
func ValidateInput(in *SMTPInput) map[string]string {
	f := map[string]string{}
	in.Host, in.Username, in.From = strings.TrimSpace(in.Host), strings.TrimSpace(in.Username), strings.TrimSpace(in.From)
	in.Encryption = strings.ToLower(strings.TrimSpace(in.Encryption))
	if in.Encryption == "" {
		in.Encryption = config.SMTPEncryptionSTARTTLS
	}
	switch in.Encryption {
	case config.SMTPEncryptionNone, config.SMTPEncryptionSTARTTLS, config.SMTPEncryptionTLS:
	default:
		f["encryption"] = "must be none, starttls or tls"
	}
	if in.Host == "" {
		return f // disables e-mail; other fields are ignored
	}
	if len(in.Host) > 253 || strings.ContainsAny(in.Host, " /\\:@\r\n") {
		f["host"] = "must be a host name or IP address"
	}
	if in.Port == 0 {
		in.Port = config.DefaultSMTPPort(in.Encryption)
	}
	if in.Port < 1 || in.Port > 65535 {
		f["port"] = "must be between 1 and 65535"
	}
	if len(in.Username) > 256 {
		f["username"] = "must be at most 256 characters"
	}
	if in.Password != nil && len(*in.Password) > 1024 {
		f["password"] = "must be at most 1024 bytes"
	}
	if a, err := mail.ParseAddress(in.From); in.From == "" || err != nil || len(in.From) > 320 || a.Address == "" {
		f["from"] = `must be an address such as "Panel <panel@example.com>"`
	}
	return f
}

// SaveStored stores the panel-managed SMTP configuration (an empty Host
// removes it). It refuses while the configuration file / environment
// defines the server.
func (s *Service) SaveStored(ctx context.Context, in SMTPInput, actor *int64) error {
	if s.cfg.Enabled() {
		return errors.New("mailauth: SMTP is set by the server configuration")
	}
	if in.Host == "" {
		return s.st.Settings.Delete(ctx, SettingSMTP)
	}
	old, err := s.loadStored(ctx)
	if err != nil {
		return err
	}
	v := storedSMTP{Host: in.Host, Port: in.Port, Username: in.Username, From: in.From, Encryption: in.Encryption,
		InsecureSkipVerify: in.InsecureSkipVerify}
	switch {
	case in.Password == nil:
		if old != nil {
			v.PasswordEnc = old.PasswordEnc
		}
	case *in.Password != "":
		if s.aead == nil {
			return errors.New("mailauth: no AEAD key")
		}
		if v.PasswordEnc, err = s.aead.SealString(*in.Password, crypto.SMTPPasswordAD()); err != nil {
			return err
		}
	}
	return s.st.Settings.Set(ctx, SettingSMTP, v, actor)
}

// ---- rendering and sending ----

// DefaultLocale returns the panel's e-mail language.
func (s *Service) DefaultLocale(ctx context.Context) string {
	l, _ := store.GetOr(ctx, s.st.Settings, SettingDefaultLocale, rdmail.DefaultLocale)
	if l = rdmail.NormalizeLocale(l); l == "" {
		l = rdmail.DefaultLocale
	}
	return l
}

func (s *Service) localeOf(ctx context.Context, u *store.User) string {
	if u != nil {
		if l := rdmail.NormalizeLocale(u.Locale); l != "" {
			return l
		}
	}
	return s.DefaultLocale(ctx)
}

func (s *Service) title(ctx context.Context) string {
	t, _ := store.GetOr(ctx, s.st.Settings, store.SettingBrandingTitle, defaultTitle)
	if strings.TrimSpace(t) == "" {
		t = defaultTitle
	}
	return t
}

// send renders kind and delivers it to addr.
func (s *Service) send(ctx context.Context, kind rdmail.Kind, locale, addr string, d rdmail.Data) error {
	cfg, _, err := s.SMTP(ctx)
	if err != nil {
		return err
	}
	if !cfg.Enabled() {
		return ErrDisabled
	}
	d.Title = s.title(ctx)
	msg, err := rdmail.Render(kind, locale, d)
	if err != nil {
		return err
	}
	msg.To = addr
	if err := s.newSender(cfg).Send(ctx, msg); err != nil {
		s.log.WarnContext(ctx, "mail: send failed", slog.String("kind", string(kind)), slog.Any("err", err))
		return fmt.Errorf("%w: %w", ErrSend, err)
	}
	return nil
}

// SendTest delivers the test message to addr (admin "Send test e-mail").
func (s *Service) SendTest(ctx context.Context, addr, locale string) error {
	if l := rdmail.NormalizeLocale(locale); l != "" {
		locale = l
	} else {
		locale = s.DefaultLocale(ctx)
	}
	return s.send(ctx, rdmail.KindTest, locale, addr, rdmail.Data{})
}

// ---- one-time codes ----

func codeDigest(userID int64, purpose, code string) []byte {
	sum := sha256.Sum256([]byte("email_code:" + purpose + ":" + strconv.FormatInt(userID, 10) + ":" + code))
	return sum[:]
}

func newCode() (string, error) {
	n, err := rand.Int(rand.Reader, big.NewInt(1_000_000))
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("%06d", n.Int64()), nil
}

// normalizeCode strips spaces and dashes; "" when not 6 digits.
func normalizeCode(code string) string {
	code = strings.Map(func(r rune) rune {
		if r == ' ' || r == '-' || r == '\t' {
			return -1
		}
		return r
	}, code)
	if len(code) != 6 {
		return ""
	}
	for _, c := range code {
		if c < '0' || c > '9' {
			return ""
		}
	}
	return code
}

// checkLimits enforces the resend cooldown and the hourly caps. With
// reuse, a still-valid code sent within the cooldown satisfies the request
// (returns reused=true, nothing is sent).
func (s *Service) checkLimits(ctx context.Context, u *store.User, purpose, ip string, reuse bool) (bool, error) {
	now := s.now()
	last, err := s.st.EmailCodes().Latest(ctx, u.ID, purpose)
	if err != nil && !errors.Is(err, store.ErrNotFound) {
		return false, err
	}
	if last != nil {
		if wait := ResendCooldown - now.Sub(last.CreatedAt); wait > 0 {
			if reuse && last.ConsumedAt == nil && last.ExpiresAt.After(now) && strings.EqualFold(last.Email, u.Email) {
				return true, nil
			}
			return false, &RateLimitError{Wait: wait}
		}
	}
	hour := now.Add(-time.Hour)
	n, err := s.st.EmailCodes().CountForUserSince(ctx, u.ID, hour)
	if err != nil {
		return false, err
	}
	if n >= MaxPerUserHour {
		return false, &RateLimitError{Wait: 15 * time.Minute}
	}
	if ip != "" {
		if n, err = s.st.EmailCodes().CountForIPSince(ctx, ip, hour); err != nil {
			return false, err
		}
		if n >= MaxPerIPHour {
			return false, &RateLimitError{Wait: 15 * time.Minute}
		}
	}
	return false, nil
}

// issueCode creates a code for purpose and e-mails it to the user's address.
func (s *Service) issueCode(ctx context.Context, u *store.User, purpose, ip string, kind rdmail.Kind, reuse bool) error {
	if !s.Enabled(ctx) {
		return ErrDisabled
	}
	if u.Email == "" {
		return ErrNoAddress
	}
	reused, err := s.checkLimits(ctx, u, purpose, ip, reuse)
	if err != nil || reused {
		return err
	}
	code, err := newCode()
	if err != nil {
		return err
	}
	row := &store.EmailCode{UserID: u.ID, Purpose: purpose, Email: u.Email, CodeSHA256: codeDigest(u.ID, purpose, code),
		IP: ip, ExpiresAt: s.now().Add(CodeTTL)}
	if err := s.st.Tx(ctx, func(tx *store.Store) error { return tx.EmailCodes().Create(ctx, row) }); err != nil {
		return err
	}
	err = s.send(ctx, kind, s.localeOf(ctx, u), u.Email, rdmail.Data{Username: displayName(u), Code: code,
		Minutes: int(CodeTTL / time.Minute), IP: ip})
	if err != nil {
		// Unusable: the user never got it. It still counts for the limits.
		_ = s.st.EmailCodes().Consume(ctx, row.ID)
	}
	return err
}

func displayName(u *store.User) string {
	if u.DisplayName != "" {
		return u.DisplayName
	}
	return u.Username
}

// verifyCode checks code against the user's active code for purpose and
// consumes it on success.
func (s *Service) verifyCode(ctx context.Context, userID int64, purpose, code string) (*store.EmailCode, bool, error) {
	code = normalizeCode(code)
	if code == "" {
		return nil, false, nil
	}
	ec := s.st.EmailCodes()
	c, err := ec.Active(ctx, userID, purpose)
	if errors.Is(err, store.ErrNotFound) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	if !crypto.Equal(c.CodeSHA256, codeDigest(userID, purpose, code)) {
		if _, err := ec.Fail(ctx, c.ID, MaxAttempts); err != nil {
			return nil, false, err
		}
		return nil, false, nil
	}
	if err := ec.Consume(ctx, c.ID); errors.Is(err, store.ErrNotFound) {
		return nil, false, nil // a concurrent request used it
	} else if err != nil {
		return nil, false, err
	}
	return c, true, nil
}

// ---- address verification ----

// SendVerification e-mails a verification code to the user's address.
func (s *Service) SendVerification(ctx context.Context, u *store.User, ip string) error {
	return s.issueCode(ctx, u, store.EmailCodeVerify, ip, rdmail.KindVerifyEmail, false)
}

// ConfirmVerification marks the address verified when code matches and the
// address did not change since the code was sent.
func (s *Service) ConfirmVerification(ctx context.Context, u *store.User, code string) (bool, error) {
	c, ok, err := s.verifyCode(ctx, u.ID, store.EmailCodeVerify, code)
	if err != nil || !ok {
		return false, err
	}
	if !strings.EqualFold(c.Email, u.Email) {
		return false, nil
	}
	now := s.now()
	err = s.st.Users.SetEmailVerified(ctx, u.ID, c.Email, &now)
	if errors.Is(err, store.ErrNotFound) {
		return false, nil
	}
	return err == nil, err
}

// ---- e-mail second factor ----

// Email2FARequiredByPolicy reports the auth.require_email_2fa setting.
func (s *Service) Email2FARequiredByPolicy(ctx context.Context) (bool, error) {
	return store.GetOr(ctx, s.st.Settings, SettingRequireEmail2FA, false)
}

// LoginFactorRequired reports whether u must enter an e-mailed code at
// sign-in. Callers check TOTP first (TOTP wins). The factor needs a
// verified address and a configured SMTP server: when e-mail is not
// configured it is not enforced (an administrator removed the server).
func (s *Service) LoginFactorRequired(ctx context.Context, u *store.User) (bool, error) {
	if !u.EmailVerified() {
		return false, nil
	}
	want := u.Email2FAEnabled
	if !want {
		req, err := s.Email2FARequiredByPolicy(ctx)
		if err != nil {
			return false, err
		}
		want = req
	}
	if !want {
		return false, nil
	}
	if !s.Enabled(ctx) {
		s.log.WarnContext(ctx, "e-mail 2FA skipped: SMTP is not configured", slog.Int64("user_id", u.ID))
		return false, nil
	}
	return true, nil
}

// SendLoginCode e-mails a sign-in code. A code sent less than a minute ago
// that is still valid is reused (no new e-mail).
func (s *Service) SendLoginCode(ctx context.Context, u *store.User, ip string) error {
	return s.issueCode(ctx, u, store.EmailCodeLogin, ip, rdmail.KindLoginCode, true)
}

// ResendLoginCode e-mails a fresh sign-in code (subject to the cooldown).
func (s *Service) ResendLoginCode(ctx context.Context, u *store.User, ip string) error {
	return s.issueCode(ctx, u, store.EmailCodeLogin, ip, rdmail.KindLoginCode, false)
}

// VerifyLoginCode checks and consumes a sign-in code.
func (s *Service) VerifyLoginCode(ctx context.Context, userID int64, code string) (bool, error) {
	_, ok, err := s.verifyCode(ctx, userID, store.EmailCodeLogin, code)
	return ok, err
}

// ---- password reset ----

// PasswordResetAvailable reports whether "Forgot password?" works: SMTP is
// configured and the panel URL (for the link) is known.
func (s *Service) PasswordResetAvailable(ctx context.Context) bool {
	return s.adminURL != "" && s.Enabled(ctx)
}

// ResetPath is the SPA route of the reset link.
const ResetPath = "/reset-password"

// RequestPasswordReset e-mails a reset link to the account named by login
// (username, or e-mail address of up to 5 accounts) when it is active, has a
// password, may use password sign-in and has a verified address. The lookup
// and delivery run in the background so the caller's response does not
// depend on whether the account exists.
func (s *Service) RequestPasswordReset(ctx context.Context, login, ip string) {
	login = strings.TrimSpace(login)
	if login == "" || len(login) > 320 {
		return
	}
	bg, cancel := context.WithTimeout(context.WithoutCancel(ctx), asyncSendTimeout)
	s.wg.Add(1)
	go func() {
		defer s.wg.Done()
		defer cancel()
		if err := s.requestReset(bg, login, ip); err != nil {
			s.log.WarnContext(bg, "password reset request", slog.Any("err", err))
		}
	}()
}

func (s *Service) requestReset(ctx context.Context, login, ip string) error {
	if !s.PasswordResetAvailable(ctx) {
		return nil
	}
	if ip != "" {
		n, err := s.st.EmailCodes().CountForIPSince(ctx, ip, s.now().Add(-time.Hour))
		if err != nil || n >= MaxPerIPHour {
			return err
		}
	}
	var users []*store.User
	u, err := s.st.Users.GetByUsername(ctx, login)
	switch {
	case err == nil:
		users = []*store.User{u}
	case !errors.Is(err, store.ErrNotFound):
		return err
	case strings.Contains(login, "@"):
		if users, err = s.st.Users.ListByEmail(ctx, login); err != nil {
			return err
		}
	}
	sent := 0
	for _, u := range users {
		if sent >= 5 {
			break
		}
		if !u.IsActive() || !u.HasPassword() || !u.EmailVerified() {
			continue
		}
		if ok, err := s.st.Settings.PasswordLoginAllowed(ctx, u); err != nil || !ok {
			continue
		}
		if _, err := s.checkLimits(ctx, u, store.EmailCodePasswordReset, ip, false); err != nil {
			var rl *RateLimitError
			if errors.As(err, &rl) {
				continue
			}
			return err
		}
		token, err := crypto.RandomToken(32)
		if err != nil {
			return err
		}
		row := &store.EmailCode{UserID: u.ID, Purpose: store.EmailCodePasswordReset, Email: u.Email,
			CodeSHA256: crypto.HashToken(token), IP: ip, ExpiresAt: s.now().Add(ResetTTL)}
		if err := s.st.Tx(ctx, func(tx *store.Store) error { return tx.EmailCodes().Create(ctx, row) }); err != nil {
			return err
		}
		link := s.adminURL + ResetPath + "?token=" + token
		if err := s.send(ctx, rdmail.KindPasswordReset, s.localeOf(ctx, u), u.Email, rdmail.Data{
			Username: displayName(u), Link: link, Minutes: int(ResetTTL / time.Minute), IP: ip}); err != nil {
			_ = s.st.EmailCodes().Consume(ctx, row.ID)
			continue
		}
		sent++
	}
	return nil
}

// CheckResetToken returns the user a reset token belongs to (ErrInvalidToken
// when it is unknown, used or expired).
func (s *Service) CheckResetToken(ctx context.Context, token string) (*store.User, error) {
	_, u, err := s.resetTarget(ctx, s.st, token)
	return u, err
}

func (s *Service) resetTarget(ctx context.Context, st *store.Store, token string) (*store.EmailCode, *store.User, error) {
	if token == "" || len(token) > 256 {
		return nil, nil, ErrInvalidToken
	}
	c, err := st.EmailCodes().ActiveByHash(ctx, store.EmailCodePasswordReset, crypto.HashToken(token))
	if errors.Is(err, store.ErrNotFound) {
		return nil, nil, ErrInvalidToken
	}
	if err != nil {
		return nil, nil, err
	}
	u, err := st.Users.GetByID(ctx, c.UserID)
	if errors.Is(err, store.ErrNotFound) {
		return nil, nil, ErrInvalidToken
	}
	if err != nil {
		return nil, nil, err
	}
	if !u.IsActive() || !strings.EqualFold(u.Email, c.Email) {
		return nil, nil, ErrInvalidToken
	}
	return c, u, nil
}

// ResetPassword consumes token, stores the new password hash and revokes
// every session and client token of the user. It returns the user and the
// number of revoked tokens.
func (s *Service) ResetPassword(ctx context.Context, token, hash, algo string) (*store.User, int64, error) {
	var (
		user    *store.User
		revoked int64
	)
	err := s.st.Tx(ctx, func(tx *store.Store) error {
		c, u, err := s.resetTarget(ctx, tx, token)
		if err != nil {
			return err
		}
		if err := tx.EmailCodes().Consume(ctx, c.ID); errors.Is(err, store.ErrNotFound) {
			return ErrInvalidToken
		} else if err != nil {
			return err
		}
		if err := tx.Users.SetPassword(ctx, u.ID, hash, algo); err != nil {
			return err
		}
		for _, p := range []string{store.EmailCodePasswordReset, store.EmailCodeLogin} {
			if err := tx.EmailCodes().ConsumeAll(ctx, u.ID, p); err != nil {
				return err
			}
		}
		if revoked, err = tx.AuthTokens.RevokeForUser(ctx, u.ID, "", "password reset by e-mail"); err != nil {
			return err
		}
		user = u
		return nil
	})
	if err != nil {
		return nil, 0, err
	}
	return user, revoked, nil
}

// MaskEmail hides most of the local part for display on a code prompt:
// "alice@example.com" -> "a***e@example.com".
func MaskEmail(addr string) string {
	at := strings.LastIndexByte(addr, '@')
	if at <= 0 {
		return ""
	}
	local, domain := []rune(addr[:at]), addr[at:]
	if len(local) <= 2 {
		return string(local[:1]) + "***" + domain
	}
	return string(local[:1]) + "***" + string(local[len(local)-1:]) + domain
}
