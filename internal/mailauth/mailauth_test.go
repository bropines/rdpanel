package mailauth_test

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/bropines/rdpanel/internal/config"
	"github.com/bropines/rdpanel/internal/crypto"
	"github.com/bropines/rdpanel/internal/db/dbtest"
	"github.com/bropines/rdpanel/internal/mail"
	"github.com/bropines/rdpanel/internal/mailauth"
	"github.com/bropines/rdpanel/internal/store"
)

type outbox struct {
	mu   sync.Mutex
	msgs []mail.Message
}

func (o *outbox) Send(_ context.Context, m mail.Message) error {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.msgs = append(o.msgs, m)
	return nil
}

func (o *outbox) last(t *testing.T) mail.Message {
	t.Helper()
	o.mu.Lock()
	defer o.mu.Unlock()
	if len(o.msgs) == 0 {
		t.Fatal("no message")
	}
	return o.msgs[len(o.msgs)-1]
}

func (o *outbox) count() int { o.mu.Lock(); defer o.mu.Unlock(); return len(o.msgs) }

type fixture struct {
	svc *mailauth.Service
	st  *store.Store
	out *outbox
	now time.Time
	mu  sync.Mutex
	u   *store.User
}

func (f *fixture) clock() time.Time { f.mu.Lock(); defer f.mu.Unlock(); return f.now }
func (f *fixture) advance(d time.Duration) {
	f.mu.Lock()
	f.now = f.now.Add(d)
	f.mu.Unlock()
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	ctx := context.Background()
	d := dbtest.Open(t)
	f := &fixture{out: &outbox{}, now: time.Date(2026, 10, 15, 12, 0, 0, 0, time.UTC)}
	f.st = store.New(d).WithClock(f.clock)
	key, _ := crypto.RandomBytes(32)
	aead, _ := crypto.NewAEAD(key)
	cfg := config.Defaults()
	cfg.PublicURL = "https://panel.example"
	cfg.SMTP = config.SMTPConfig{Host: "smtp.example", Port: 25, From: "p@example.test", Encryption: "none"}
	f.svc = mailauth.New(mailauth.Options{Store: f.st, AEAD: aead, Config: cfg,
		NewSender: func(config.SMTPConfig) mail.Sender { return f.out }})
	f.u = &store.User{Username: "alice", Email: "alice@example.test", Locale: "ru"}
	if err := f.st.Users.Create(ctx, f.u); err != nil {
		t.Fatal(err)
	}
	return f
}

func codeOf(t *testing.T, m mail.Message) string {
	t.Helper()
	for i := 0; i+6 <= len(m.Subject); i++ {
		s := m.Subject[i : i+6]
		ok := true
		for _, c := range s {
			ok = ok && c >= '0' && c <= '9'
		}
		if ok {
			return s
		}
	}
	t.Fatalf("no code in %q", m.Subject)
	return ""
}

func TestLoginCodeAttemptsAndExpiry(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	if err := f.svc.SendLoginCode(ctx, f.u, "10.0.0.1"); err != nil {
		t.Fatal(err)
	}
	code := codeOf(t, f.out.last(t))
	// Russian template for a user with locale ru.
	if want := "код входа"; !strings.Contains(f.out.last(t).Subject, want) {
		t.Errorf("subject %q", f.out.last(t).Subject)
	}
	// Re-login within the cooldown reuses the code (no new e-mail).
	if err := f.svc.SendLoginCode(ctx, f.u, "10.0.0.1"); err != nil || f.out.count() != 1 {
		t.Fatalf("reuse: %v, %d mails", err, f.out.count())
	}
	// An explicit resend within the cooldown is refused.
	var rl *mailauth.RateLimitError
	if err := f.svc.ResendLoginCode(ctx, f.u, "10.0.0.1"); !errors.As(err, &rl) {
		t.Fatalf("resend: %v", err)
	}
	wrong := "000000"
	if code == wrong {
		wrong = "111111"
	}
	for i := 0; i < mailauth.MaxAttempts; i++ {
		if ok, err := f.svc.VerifyLoginCode(ctx, f.u.ID, wrong); ok || err != nil {
			t.Fatalf("wrong code accepted: %v %v", ok, err)
		}
	}
	if ok, _ := f.svc.VerifyLoginCode(ctx, f.u.ID, code); ok {
		t.Fatal("code valid after max attempts")
	}
	// A fresh code works once, with spaces, and expires.
	f.advance(mailauth.ResendCooldown)
	if err := f.svc.SendLoginCode(ctx, f.u, ""); err != nil {
		t.Fatal(err)
	}
	code = codeOf(t, f.out.last(t))
	if ok, _ := f.svc.VerifyLoginCode(ctx, f.u.ID, code[:3]+" "+code[3:]); !ok {
		t.Fatal("valid code refused")
	}
	if ok, _ := f.svc.VerifyLoginCode(ctx, f.u.ID, code); ok {
		t.Fatal("code reused")
	}
	f.advance(mailauth.ResendCooldown)
	_ = f.svc.SendLoginCode(ctx, f.u, "")
	code = codeOf(t, f.out.last(t))
	f.advance(mailauth.CodeTTL + time.Second)
	if ok, _ := f.svc.VerifyLoginCode(ctx, f.u.ID, code); ok {
		t.Fatal("expired code accepted")
	}
}

func TestHourlyCaps(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	for i := 0; i < mailauth.MaxPerUserHour; i++ {
		if err := f.svc.ResendLoginCode(ctx, f.u, "10.0.0.2"); err != nil {
			t.Fatalf("send %d: %v", i, err)
		}
		f.advance(mailauth.ResendCooldown)
	}
	var rl *mailauth.RateLimitError
	if err := f.svc.ResendLoginCode(ctx, f.u, "10.0.0.2"); !errors.As(err, &rl) {
		t.Fatalf("cap: %v", err)
	}
	f.advance(time.Hour)
	if err := f.svc.ResendLoginCode(ctx, f.u, "10.0.0.2"); err != nil {
		t.Fatalf("after an hour: %v", err)
	}
}

func TestVerificationAndFactorRules(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	if need, _ := f.svc.LoginFactorRequired(ctx, f.u); need {
		t.Fatal("unverified address requires the factor")
	}
	if err := f.svc.SendVerification(ctx, f.u, ""); err != nil {
		t.Fatal(err)
	}
	code := codeOf(t, f.out.last(t))
	// Address changed after sending: the code no longer verifies.
	moved := *f.u
	moved.Email = "other@example.test"
	if err := f.st.Users.Update(ctx, &moved); err != nil {
		t.Fatal(err)
	}
	if ok, _ := f.svc.ConfirmVerification(ctx, &moved, code); ok {
		t.Fatal("verified a different address")
	}
	f.advance(mailauth.ResendCooldown)
	if err := f.svc.SendVerification(ctx, &moved, ""); err != nil {
		t.Fatal(err)
	}
	if ok, err := f.svc.ConfirmVerification(ctx, &moved, codeOf(t, f.out.last(t))); !ok || err != nil {
		t.Fatalf("confirm: %v %v", ok, err)
	}
	u, _ := f.st.Users.GetByID(ctx, f.u.ID)
	if !u.EmailVerified() {
		t.Fatal("not verified")
	}
	if need, _ := f.svc.LoginFactorRequired(ctx, u); need {
		t.Fatal("factor without opt-in or policy")
	}
	if err := f.st.Users.SetEmail2FA(ctx, u.ID, true); err != nil {
		t.Fatal(err)
	}
	u, _ = f.st.Users.GetByID(ctx, u.ID)
	if need, _ := f.svc.LoginFactorRequired(ctx, u); !need {
		t.Fatal("opt-in ignored")
	}
	// Changing the address case only keeps the state; a new address clears it.
	u.Email = "OTHER@example.test"
	_ = f.st.Users.Update(ctx, u)
	u, _ = f.st.Users.GetByID(ctx, u.ID)
	if !u.EmailVerified() || !u.Email2FAEnabled {
		t.Fatal("case change cleared verification")
	}
	u.Email = "third@example.test"
	_ = f.st.Users.Update(ctx, u)
	u, _ = f.st.Users.GetByID(ctx, u.ID)
	if u.EmailVerified() || u.Email2FAEnabled {
		t.Fatal("new address kept verification")
	}
	if err := f.st.Users.SetEmail2FA(ctx, u.ID, true); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("enable on unverified: %v", err)
	}
}

func TestStoredSMTPPasswordSealed(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	// Configuration wins: saving is refused.
	pw := "s3cret"
	if err := f.svc.SaveStored(ctx, mailauth.SMTPInput{Host: "x", Password: &pw}, nil); err == nil {
		t.Fatal("saved over config")
	}
	key, _ := crypto.RandomBytes(32)
	aead, _ := crypto.NewAEAD(key)
	svc := mailauth.New(mailauth.Options{Store: f.st, AEAD: aead})
	in := mailauth.SMTPInput{Host: "smtp.example", From: "p@example.test", Username: "u", Password: &pw}
	if fe := mailauth.ValidateInput(&in); len(fe) != 0 {
		t.Fatal(fe)
	}
	if err := svc.SaveStored(ctx, in, nil); err != nil {
		t.Fatal(err)
	}
	raw, _ := f.st.Settings.GetRaw(ctx, mailauth.SettingSMTP)
	if strings.Contains(string(raw), pw) {
		t.Fatalf("password in clear: %s", raw)
	}
	c, src, err := svc.SMTP(ctx)
	if err != nil || src != mailauth.SourceSettings || c.Password.Reveal() != pw || c.Port != 587 {
		t.Fatalf("effective: %+v %s %v", c, src, err)
	}
	in.Password = nil // keep
	_ = svc.SaveStored(ctx, in, nil)
	if c, _, _ := svc.SMTP(ctx); c.Password.Reveal() != pw {
		t.Fatal("password not kept")
	}
	if fe := mailauth.ValidateInput(&mailauth.SMTPInput{Host: "a b", From: "nope", Port: 70000, Encryption: "ssl"}); len(fe) != 4 {
		t.Fatalf("validation: %v", fe)
	}
	if mailauth.MaskEmail("ab@x.io") != "a***@x.io" || mailauth.MaskEmail("alice@x.io") != "a***e@x.io" || mailauth.MaskEmail("") != "" {
		t.Fatal("MaskEmail")
	}
}
