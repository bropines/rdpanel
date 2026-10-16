package clientapi

import (
	"context"
	"encoding/json"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/bropines/rustdesk-panel/internal/store"
)

type fakeEmail struct {
	mu       sync.Mutex
	required map[int64]bool
	sent     map[int64]int
	code     string
	sendErr  error
}

func (f *fakeEmail) LoginFactorRequired(_ context.Context, u *store.User) (bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.required[u.ID], nil
}

func (f *fakeEmail) SendLoginCode(_ context.Context, u *store.User, _ string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.sendErr != nil {
		return f.sendErr
	}
	f.sent[u.ID]++
	return nil
}

func (f *fakeEmail) VerifyLoginCode(_ context.Context, _ int64, code string) (bool, error) {
	return code == f.code, nil
}

type retryErr struct{}

func (retryErr) Error() string             { return "rate limited" }
func (retryErr) RetryAfter() time.Duration { return 42 * time.Second }

func TestLoginEmailCode(t *testing.T) {
	fe := &fakeEmail{required: map[int64]bool{}, sent: map[int64]int{}, code: "654321"}
	e := newEnv(t, func(d *Deps) { d.Email = fe })
	u := e.user("dave", "pw-dave-12", func(u *store.User) { u.Email = "dave@example.test" })
	fe.required[u.ID] = true

	r := e.mustDo("POST", "/api/login", loginBody("dave", "pw-dave-12"), "", 200)
	var ch struct {
		Secret      string `json:"secret"`
		AccessToken string `json:"access_token"`
		User        struct {
			Name, Email string
		} `json:"user"`
	}
	_ = json.Unmarshal([]byte(r.body), &ch)
	if !strings.Contains(r.body, `"type":"email_check"`) || !strings.Contains(r.body, `"tfa_type":"email_check"`) ||
		ch.Secret == "" || ch.AccessToken != "" || ch.User.Name != "dave" || ch.User.Email != "d***e@example.test" {
		t.Fatalf("challenge %s", r.body)
	}
	if fe.sent[u.ID] != 1 {
		t.Fatalf("sent = %d", fe.sent[u.ID])
	}
	step2 := func(code string) resp {
		// Flutter: no tfaCode for e-mail codes.
		return e.do("POST", "/api/login", string(mustJSON(map[string]any{"username": "dave", "type": "email_code",
			"verificationCode": code, "secret": ch.Secret, "id": "1", "uuid": "u"})), "")
	}
	if r := step2("000000"); r.code != 400 {
		t.Fatalf("bad code: %d %s", r.code, r.body)
	}
	// A TOTP challenge cannot be answered with an e-mail code and vice versa.
	e.totp.codes[u.ID] = "111111"
	if r := step2("111111"); r.code != 400 {
		t.Fatalf("totp code on email challenge: %d", r.code)
	}
	delete(e.totp.codes, u.ID)
	if r := step2("654321"); r.code != 200 || !strings.Contains(r.body, `"access_token"`) {
		t.Fatalf("good code: %d %s", r.code, r.body)
	}

	// TOTP wins over e-mail.
	e.totp.codes[u.ID] = "111111"
	r = e.mustDo("POST", "/api/login", loginBody("dave", "pw-dave-12"), "", 200)
	if !strings.Contains(r.body, `"tfa_type":"tfa_check"`) {
		t.Fatalf("totp first: %s", r.body)
	}
	_ = json.Unmarshal([]byte(r.body), &ch)
	if r := step2("654321"); r.code != 400 {
		t.Fatalf("email code on totp challenge: %d", r.code)
	}
	delete(e.totp.codes, u.ID)

	// Send failures and rate limits.
	fe.sendErr = retryErr{}
	r = e.do("POST", "/api/login", loginBody("dave", "pw-dave-12"), "")
	if r.code != 429 || r.hdr.Get("Retry-After") != "42" || !strings.Contains(r.body, `"error"`) {
		t.Fatalf("rate limited: %d %s", r.code, r.body)
	}
	fe.sendErr = context.DeadlineExceeded
	if r := e.do("POST", "/api/login", loginBody("dave", "pw-dave-12"), ""); r.code != 503 {
		t.Fatalf("send failure: %d %s", r.code, r.body)
	}
}
