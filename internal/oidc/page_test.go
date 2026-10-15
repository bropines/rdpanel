package oidc

import (
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestResultPage(t *testing.T) {
	s := &Service{log: slog.New(slog.NewTextHandler(io.Discard, nil))}
	cases := []struct {
		name, lang, msg string
		status          int
		want, notWant   []string
	}{
		{"success en", "en-US,en;q=0.9", "", 200,
			[]string{`lang="en"`, "Signed in", `href="rustdesk://"`, "Return to RustDesk"}, []string{"<script"}},
		{"success ru", "ru-RU,ru;q=0.9,en;q=0.8", "", 200,
			[]string{`lang="ru"`, "Вход выполнен", "Вернуться в RustDesk"}, nil},
		{"failure escapes message", "en", `<img src=x onerror=alert(1)>`, 400,
			[]string{"Sign-in failed", "&lt;img src=x onerror=alert(1)&gt;"}, []string{"<img"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			r := httptest.NewRequest(http.MethodGet, "/api/oidc/callback", nil)
			r.Header.Set("Accept-Language", c.lang)
			w := httptest.NewRecorder()
			s.page(w, r, c.status, c.msg)
			body := w.Body.String()
			if w.Code != c.status || w.Header().Get("Cache-Control") != "no-store" {
				t.Fatalf("status %d cache %q", w.Code, w.Header().Get("Cache-Control"))
			}
			for _, x := range c.want {
				if !strings.Contains(body, x) {
					t.Errorf("missing %q", x)
				}
			}
			for _, x := range c.notWant {
				if strings.Contains(body, x) {
					t.Errorf("unexpected %q", x)
				}
			}
		})
	}
}
