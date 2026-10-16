package mail_test

import (
	"context"
	"mime"
	"strings"
	"testing"

	"github.com/bropines/rustdesk-panel/internal/config"
	"github.com/bropines/rustdesk-panel/internal/mail"
	"github.com/bropines/rustdesk-panel/internal/mail/mailtest"
)

func TestSendModes(t *testing.T) {
	cases := []struct {
		name string
		opts []mailtest.Option
		tls  bool
	}{
		{"plain", nil, false},
		{"plain+auth", []mailtest.Option{mailtest.WithAuth("bob", "s3cret")}, false},
		{"starttls+auth", []mailtest.Option{mailtest.WithSTARTTLS(), mailtest.WithAuth("bob", "s3cret")}, true},
		{"implicit tls", []mailtest.Option{mailtest.WithImplicitTLS(), mailtest.WithAuth("bob", "s3cret")}, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			srv := mailtest.Start(t, tc.opts...)
			msg, err := mail.Render(mail.KindLoginCode, "ru", mail.Data{Title: "Панель", Username: "bob", Code: "123456", Minutes: 10, IP: "10.0.0.1"})
			if err != nil {
				t.Fatal(err)
			}
			msg.To = "Bob <bob@example.test>"
			if err := mail.NewSMTP(srv.Config()).Send(context.Background(), msg); err != nil {
				t.Fatal(err)
			}
			got := srv.Last(t)
			if got.TLS != tc.tls {
				t.Errorf("TLS = %v, want %v", got.TLS, tc.tls)
			}
			if got.From != "panel@example.test" || len(got.To) != 1 || got.To[0] != "bob@example.test" {
				t.Errorf("envelope = %q -> %v", got.From, got.To)
			}
			if got.Code() != "123456" {
				t.Errorf("code = %q", got.Code())
			}
			subj, err := new(mime.WordDecoder).DecodeHeader(got.Header().Get("Subject"))
			if err != nil || subj != "123456 — код входа в Панель" {
				t.Errorf("subject = %q (%v)", subj, err)
			}
			if !strings.Contains(got.Data, "multipart/alternative") || !strings.Contains(got.Data, "text/html") {
				t.Error("want multipart text+html body")
			}
		})
	}
}

func TestSendErrors(t *testing.T) {
	srv := mailtest.Start(t, mailtest.WithAuth("bob", "right"))
	cfg := srv.Config()
	cfg.Password = "wrong"
	err := mail.NewSMTP(cfg).Send(context.Background(), mail.Message{To: "a@example.test", Subject: "x", Text: "y"})
	if err == nil || !strings.Contains(err.Error(), "authentication") {
		t.Errorf("bad password: err = %v", err)
	}

	srv2 := mailtest.Start(t)
	srv2.RejectRecipients(true)
	err = mail.NewSMTP(srv2.Config()).Send(context.Background(), mail.Message{To: "a@example.test", Subject: "x", Text: "y"})
	if err == nil || !strings.Contains(err.Error(), "RCPT") {
		t.Errorf("rejected rcpt: err = %v", err)
	}

	// STARTTLS required but not offered.
	cfg = mailtest.Start(t).Config()
	cfg.Encryption = config.SMTPEncryptionSTARTTLS
	if err := mail.NewSMTP(cfg).Send(context.Background(), mail.Message{To: "a@example.test", Text: "y"}); err == nil {
		t.Error("want STARTTLS error")
	}
	if err := mail.NewSMTP(config.SMTPConfig{}).Send(context.Background(), mail.Message{To: "a@example.test"}); err != mail.ErrNotConfigured {
		t.Errorf("unconfigured: %v", err)
	}
	// Header injection in the recipient is refused.
	if err := mail.NewSMTP(srv2.Config()).Send(context.Background(), mail.Message{To: "a@example.test\r\nBcc: x@example.test"}); err == nil {
		t.Error("want invalid recipient error")
	}
}

func TestRenderTemplates(t *testing.T) {
	for _, loc := range []string{"en", "ru", "de", ""} {
		for _, k := range []mail.Kind{mail.KindTest, mail.KindVerifyEmail, mail.KindLoginCode, mail.KindPasswordReset} {
			m, err := mail.Render(k, loc, mail.Data{Title: "T<x>", Username: "u", Code: "000111", Link: "https://p.example/reset-password?token=a&b", Minutes: 30})
			if err != nil {
				t.Fatalf("%s/%s: %v", loc, k, err)
			}
			if m.Subject == "" || m.Text == "" || !strings.Contains(m.HTML, "<p>") {
				t.Errorf("%s/%s: empty parts", loc, k)
			}
			if strings.Contains(m.HTML, "T<x>") {
				t.Errorf("%s/%s: title not escaped in HTML", loc, k)
			}
		}
	}
	if mail.NormalizeLocale("ru-RU") != "ru" || mail.NormalizeLocale("fr") != "" {
		t.Error("NormalizeLocale")
	}
}
