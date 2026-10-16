package oidc

import (
	"html/template"
	"net/http"
	"strings"
)

// resultPage is the page the browser shows at the end of a RustDesk client
// OIDC login (the client itself learns the result by polling auth-query).
// It is static: no scripts (the panel CSP forbids inline scripts), inline
// styles only, light/dark from prefers-color-scheme.
var resultPage = template.Must(template.New("result").Parse(`<!doctype html>
<html lang="{{.Lang}}">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width,initial-scale=1">
<meta name="color-scheme" content="light dark">
<title>{{.Title}}</title>
<style>
:root{--bg:#f6f7f9;--card:#fff;--fg:#0f172a;--muted:#64748b;--border:#e2e8f0;--ok:#16a34a;--err:#dc2626;--btn:#2563eb;--btnfg:#fff}
@media (prefers-color-scheme:dark){:root{--bg:#09090b;--card:#111113;--fg:#f4f4f5;--muted:#a1a1aa;--border:#27272a;--ok:#22c55e;--err:#ef4444;--btn:#3b82f6}}
*{box-sizing:border-box}
html,body{height:100%}
body{margin:0;display:flex;align-items:center;justify-content:center;padding:16px;background:var(--bg);color:var(--fg);
font:15px/1.5 -apple-system,BlinkMacSystemFont,"Segoe UI",Roboto,"Helvetica Neue",Arial,sans-serif}
.card{width:100%;max-width:420px;background:var(--card);border:1px solid var(--border);border-radius:16px;padding:32px 28px;text-align:center;box-shadow:0 10px 30px rgba(0,0,0,.08)}
.icon{width:56px;height:56px;margin:0 auto 16px;border-radius:50%;display:flex;align-items:center;justify-content:center;background:color-mix(in srgb,var(--c) 14%,transparent);color:var(--c)}
.ok{--c:var(--ok)}.err{--c:var(--err)}
h1{font-size:20px;margin:0 0 8px}
p{margin:0 0 20px;color:var(--muted)}
.btn{display:inline-block;padding:10px 18px;border-radius:10px;background:var(--btn);color:var(--btnfg);text-decoration:none;font-weight:600}
.btn:focus-visible{outline:2px solid var(--btn);outline-offset:3px}
.hint{margin:16px 0 0;font-size:13px}
.brand{margin-top:24px;font-size:12px;color:var(--muted)}
</style>
</head>
<body>
<main class="card">
  <div class="icon {{if .OK}}ok{{else}}err{{end}}" aria-hidden="true">
    {{if .OK}}<svg width="28" height="28" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2.5" stroke-linecap="round" stroke-linejoin="round"><path d="M20 6 9 17l-5-5"/></svg>
    {{else}}<svg width="28" height="28" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2.5" stroke-linecap="round" stroke-linejoin="round"><path d="M18 6 6 18M6 6l12 12"/></svg>{{end}}
  </div>
  <h1>{{.Title}}</h1>
  <p>{{.Body}}</p>
  <a class="btn" href="rustdesk://">{{.Button}}</a>
  <p class="hint">{{.Hint}}</p>
  <div class="brand">RD Panel</div>
</main>
</body>
</html>
`))

type resultPageData struct {
	Lang, Title, Body, Button, Hint string
	OK                              bool
}

// prefersRussian reports whether the browser lists Russian before English.
func prefersRussian(r *http.Request) bool {
	for _, part := range strings.Split(r.Header.Get("Accept-Language"), ",") {
		tag := strings.ToLower(strings.TrimSpace(strings.SplitN(part, ";", 2)[0]))
		switch {
		case strings.HasPrefix(tag, "ru"):
			return true
		case strings.HasPrefix(tag, "en"):
			return false
		}
	}
	return false
}

// page renders the client-login result page. errMsg is shown verbatim
// (escaped); an empty errMsg means success.
func (s *Service) page(w http.ResponseWriter, r *http.Request, status int, errMsg string) {
	d := resultPageData{Lang: "en", OK: errMsg == "",
		Title: "Signed in", Body: "You are signed in to RustDesk.",
		Button: "Return to RustDesk", Hint: "You can close this tab."}
	ru := prefersRussian(r)
	if ru {
		d = resultPageData{Lang: "ru", OK: errMsg == "",
			Title: "Вход выполнен", Body: "Вы вошли в RustDesk.",
			Button: "Вернуться в RustDesk", Hint: "Эту вкладку можно закрыть."}
	}
	if errMsg != "" {
		d.Body = errMsg
		if ru {
			d.Title, d.Hint = "Не удалось войти", "Вернитесь в RustDesk и попробуйте ещё раз."
		} else {
			d.Title, d.Hint = "Sign-in failed", "Go back to RustDesk and try again."
		}
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Referrer-Policy", "no-referrer")
	w.Header().Set("Content-Language", d.Lang)
	w.WriteHeader(status)
	if err := resultPage.Execute(w, d); err != nil {
		s.log.WarnContext(r.Context(), "oidc: render result page", "err", err)
	}
}
