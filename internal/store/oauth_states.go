package store

import (
	"context"
	"database/sql"
	"time"

	"github.com/bropines/rustdesk-panel/internal/db"
)

// OAuthAction is what a pending OAuth flow will do once the IdP answers.
type OAuthAction string

// OAuth actions (oauth_states.action).
const (
	OAuthLogin   OAuthAction = "login"
	OAuthBind    OAuthAction = "bind"
	OAuthWebAuth OAuthAction = "webauth"
)

// OAuthFlow says who started the flow.
type OAuthFlow string

// OAuth flows (oauth_states.flow).
const (
	// OAuthFlowClient: RustDesk app polling /api/oidc/auth-query.
	OAuthFlowClient OAuthFlow = "client"
	// OAuthFlowWeb: panel browser session.
	OAuthFlowWeb OAuthFlow = "web"
)

// OAuthStateProcessing is the result_error sentinel written while a callback
// is exchanging the code (claims the state so a second callback cannot run).
// Pollers treat it as "still pending"; it is never shown to users.
const OAuthStateProcessing = "\x00processing"

// OAuthState is a row of oauth_states. The plain state is never stored.
type OAuthState struct {
	ID               int64
	StateSHA256      []byte
	Action           OAuthAction
	Flow             OAuthFlow
	ProviderID       *int64
	PKCEVerifierEnc  []byte
	Nonce            string
	DeviceRustdeskID string
	DeviceUUID       string
	DeviceName       string
	DeviceOS         string
	DeviceType       string
	RedirectPath     string
	InitiatorUserID  *int64
	ResultUserID     *int64
	ResultError      string
	ProfileJSON      string
	ConsumedAt       *time.Time
	CreatedAt        time.Time
	ExpiresAt        time.Time
}

// OAuthStates is the oauth_states repository.
type OAuthStates struct {
	q   db.Querier
	now func() time.Time
}

const oauthStateColumns = `id, state_sha256, action, flow, provider_id, pkce_verifier_enc, nonce, device_rustdesk_id,
	device_uuid, device_name, device_os, device_type, redirect_path, initiator_user_id, result_user_id, result_error,
	profile_json, consumed_at, created_at, expires_at`

func scanOAuthState(row interface{ Scan(...any) error }) (*OAuthState, error) {
	var (
		s                                         OAuthState
		providerID, initiator, result, consumed   sql.NullInt64
		nonce, devID, devUUID, devName, devOS     sql.NullString
		devType, redirect, resultErr, profileJSON sql.NullString
		createdAt, expiresAt                      int64
	)
	err := row.Scan(&s.ID, &s.StateSHA256, &s.Action, &s.Flow, &providerID, &s.PKCEVerifierEnc, &nonce, &devID,
		&devUUID, &devName, &devOS, &devType, &redirect, &initiator, &result, &resultErr, &profileJSON, &consumed,
		&createdAt, &expiresAt)
	if err != nil {
		return nil, mapErr(err)
	}
	s.ProviderID, s.InitiatorUserID, s.ResultUserID = ptrID(providerID), ptrID(initiator), ptrID(result)
	s.Nonce, s.DeviceRustdeskID, s.DeviceUUID, s.DeviceName = nonce.String, devID.String, devUUID.String, devName.String
	s.DeviceOS, s.DeviceType, s.RedirectPath = devOS.String, devType.String, redirect.String
	s.ResultError, s.ProfileJSON = resultErr.String, profileJSON.String
	s.ConsumedAt = ptrMs(consumed)
	s.CreatedAt, s.ExpiresAt = fromMs(createdAt), fromMs(expiresAt)
	return &s, nil
}

// Create inserts s; ID and CreatedAt are set. ExpiresAt must be set by the caller.
func (r *OAuthStates) Create(ctx context.Context, s *OAuthState) error {
	if s.CreatedAt.IsZero() {
		s.CreatedAt = r.now()
	}
	return mapErr(r.q.QueryRowContext(ctx, `INSERT INTO oauth_states (state_sha256, action, flow, provider_id,
		pkce_verifier_enc, nonce, device_rustdesk_id, device_uuid, device_name, device_os, device_type, redirect_path,
		initiator_user_id, result_user_id, result_error, profile_json, consumed_at, created_at, expires_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?) RETURNING id`,
		s.StateSHA256, s.Action, s.Flow, nullID(s.ProviderID), s.PKCEVerifierEnc, nullStr(s.Nonce),
		nullStr(s.DeviceRustdeskID), nullStr(s.DeviceUUID), nullStr(s.DeviceName), nullStr(s.DeviceOS),
		nullStr(s.DeviceType), nullStr(s.RedirectPath), nullID(s.InitiatorUserID), nullID(s.ResultUserID),
		nullStr(s.ResultError), nullStr(s.ProfileJSON), nullMs(s.ConsumedAt), toMs(s.CreatedAt),
		toMs(s.ExpiresAt)).Scan(&s.ID))
}

// GetActive returns the unconsumed, unexpired state with digest hash.
func (r *OAuthStates) GetActive(ctx context.Context, hash []byte) (*OAuthState, error) {
	return scanOAuthState(r.q.QueryRowContext(ctx, `SELECT `+oauthStateColumns+` FROM oauth_states
		WHERE state_sha256 = ? AND consumed_at IS NULL AND expires_at > ?`, hash, toMs(r.now())))
}

// Claim atomically marks an active state as being processed by a callback
// (result_error = OAuthStateProcessing). It fails with ErrNotFound when the
// state is unknown, expired, consumed or already claimed, which makes every
// callback single-use.
func (r *OAuthStates) Claim(ctx context.Context, id int64) error {
	return expectOne(r.q.ExecContext(ctx, `UPDATE oauth_states SET result_error = ?
		WHERE id = ? AND consumed_at IS NULL AND expires_at > ? AND result_user_id IS NULL AND result_error IS NULL`,
		OAuthStateProcessing, id, toMs(r.now())))
}

// Complete records the outcome of a claimed state: a user id or an error.
func (r *OAuthStates) Complete(ctx context.Context, id int64, userID *int64, errMsg string) error {
	return expectOne(r.q.ExecContext(ctx, `UPDATE oauth_states SET result_user_id = ?, result_error = ?,
		pkce_verifier_enc = NULL WHERE id = ? AND result_error = ?`,
		nullID(userID), nullStr(errMsg), id, OAuthStateProcessing))
}

// Consume atomically marks a state consumed (single use). ErrNotFound when it
// was already consumed or has expired.
func (r *OAuthStates) Consume(ctx context.Context, id int64) error {
	return expectOne(r.q.ExecContext(ctx, `UPDATE oauth_states SET consumed_at = ?, pkce_verifier_enc = NULL
		WHERE id = ? AND consumed_at IS NULL AND expires_at > ?`, toMs(r.now()), id, toMs(r.now())))
}

// PurgeExpired deletes states that expired before cutoff.
func (r *OAuthStates) PurgeExpired(ctx context.Context, cutoff time.Time) (int64, error) {
	res, err := r.q.ExecContext(ctx, `DELETE FROM oauth_states WHERE expires_at < ?`, toMs(cutoff))
	if err != nil {
		return 0, mapErr(err)
	}
	return res.RowsAffected()
}
