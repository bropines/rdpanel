package adminapi

import (
	"net/http"

	"github.com/bropines/rdpanel/internal/crypto"
	"github.com/bropines/rdpanel/internal/httpx"
)

// csrfTokenLen is the length of a CSRF token (32 random bytes, base64url).
const csrfTokenLen = 43

func validCSRFFormat(s string) bool {
	if len(s) != csrfTokenLen {
		return false
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '-' || c == '_') {
			return false
		}
	}
	return true
}

// setCSRFCookie issues a fresh CSRF token cookie (readable by the SPA).
func setCSRFCookie(w http.ResponseWriter, secure bool) (string, error) {
	tok, err := crypto.RandomToken(32)
	if err != nil {
		return "", err
	}
	http.SetCookie(w, &http.Cookie{Name: CSRFCookie, Value: tok, Path: "/", HttpOnly: false,
		Secure: secure, SameSite: http.SameSiteLaxMode})
	return tok, nil
}

// csrfFromRequest returns the CSRF cookie value when well-formed.
func csrfFromRequest(r *http.Request) string {
	if c, err := r.Cookie(CSRFCookie); err == nil && validCSRFFormat(c.Value) {
		return c.Value
	}
	return ""
}

// csrfProtect enforces the double-submit check on every unsafe method: the
// X-CSRF-Token header must equal the rdpanel_csrf cookie. A cross-site page
// can make the browser send the cookie but can neither read it nor set the
// header, so forged requests fail.
//
// Requests carrying "Authorization: Bearer ..." are exempt: the session
// loader then authenticates them by that API token alone (never by the
// cookie), and a cross-site page cannot make a browser attach that header.
func csrfProtect(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodGet, http.MethodHead, http.MethodOptions:
			next.ServeHTTP(w, r)
			return
		}
		if _, bearer := bearerFrom(r); bearer {
			next.ServeHTTP(w, r)
			return
		}
		cookie := csrfFromRequest(r)
		header := r.Header.Get(CSRFHeader)
		if cookie == "" || header == "" || !crypto.EqualString(cookie, header) {
			httpx.WriteError(w, http.StatusForbidden, "csrf_failed", "CSRF token missing or invalid")
			return
		}
		next.ServeHTTP(w, r)
	})
}
