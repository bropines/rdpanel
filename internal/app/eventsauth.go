package app

import (
	"context"
	"errors"
	"math"
	"net/http"

	"github.com/bropines/rdpanel/internal/adminapi"
	"github.com/bropines/rdpanel/internal/events"
	"github.com/bropines/rdpanel/internal/store"
)

var errNoSession = errors.New("no active panel session")

// panelSessionAuth authenticates SSE streams with the panel session cookie.
// It reuses the admin API session loader; sessions still owing an MFA step
// or a mandatory TOTP enrolment are rejected.
func panelSessionAuth(d adminapi.Deps) events.Authenticator {
	load := adminapi.SessionMiddleware(d)
	return events.AuthenticatorFunc(func(r *http.Request) (events.Principal, error) {
		var (
			p  events.Principal
			ok bool
		)
		load(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
			s := adminapi.CurrentSession(r.Context())
			if s == nil || s.User == nil || s.MFAPending || s.MustEnrollTOTP {
				return
			}
			role := string(store.RoleUser)
			if s.User.Role == store.RoleAdmin {
				role = events.RoleAdmin
			}
			p = events.Principal{UserID: int64(s.User.ID), Role: role}
			ok = true
		})).ServeHTTP(discardResponse{}, r)
		if !ok {
			return events.Principal{}, errNoSession
		}
		return p, nil
	})
}

// clientTOTP adapts adminapi.TOTPService (uint32 user ids, the client JWT
// claim width) to clientapi.TOTPVerifier (int64 store ids).
type clientTOTP struct{ s *adminapi.TOTPService }

func (t clientTOTP) Enrolled(ctx context.Context, userID int64) (bool, error) {
	id, ok := userIDu32(userID)
	if !ok {
		return false, nil
	}
	return t.s.Enrolled(ctx, id)
}

func (t clientTOTP) Verify(ctx context.Context, userID int64, code string) (bool, error) {
	id, ok := userIDu32(userID)
	if !ok {
		return false, nil
	}
	return t.s.Verify(ctx, id, code)
}

func userIDu32(id int64) (uint32, bool) {
	if id <= 0 || id > math.MaxUint32 {
		return 0, false
	}
	return uint32(id), true
}

// discardResponse swallows anything the session loader would write.
type discardResponse struct{}

func (discardResponse) Header() http.Header         { return http.Header{} }
func (discardResponse) Write(b []byte) (int, error) { return len(b), nil }
func (discardResponse) WriteHeader(int)             {}
