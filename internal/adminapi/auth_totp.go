package adminapi

import (
	"context"
	"errors"
	"strings"

	"github.com/bropines/rustdesk-panel/internal/auth"
	"github.com/bropines/rustdesk-panel/internal/crypto"
	"github.com/bropines/rustdesk-panel/internal/store"
)

// TOTPService verifies and manages TOTP second factors and recovery codes.
// It satisfies the client API's TOTPVerifier (POST /api/login tfa step):
//
//	Enrolled(ctx, userID uint32) (bool, error)
//	Verify(ctx, userID uint32, code string) (bool, error)
type TOTPService struct {
	st   *store.Store
	aead *crypto.AEAD
}

// NewTOTPService returns a TOTPService.
func NewTOTPService(st *store.Store, aead *crypto.AEAD) *TOTPService {
	return &TOTPService{st: st, aead: aead}
}

// Enrolled reports whether the user has confirmed TOTP (2FA active).
func (t *TOTPService) Enrolled(ctx context.Context, userID uint32) (bool, error) {
	return t.enrolled(ctx, int64(userID))
}

// Verify checks a 6-digit TOTP code (with replay protection) or, for any
// other input, a one-time recovery code (consumed on success). It returns
// false without error for wrong codes and for users without 2FA.
func (t *TOTPService) Verify(ctx context.Context, userID uint32, code string) (bool, error) {
	return t.verify(ctx, int64(userID), code)
}

func (t *TOTPService) enrolled(ctx context.Context, userID int64) (bool, error) {
	s, err := t.st.TOTPSecrets.Get(ctx, userID)
	if errors.Is(err, store.ErrNotFound) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return s.Confirmed(), nil
}

func isTOTPCode(code string) bool {
	code = strings.ReplaceAll(strings.TrimSpace(code), " ", "")
	if len(code) != 6 {
		return false
	}
	for _, c := range code {
		if c < '0' || c > '9' {
			return false
		}
	}
	return true
}

func (t *TOTPService) verify(ctx context.Context, userID int64, code string) (bool, error) {
	if code == "" || len(code) > 64 {
		return false, nil
	}
	if isTOTPCode(code) {
		return t.verifyTOTP(ctx, userID, code, true)
	}
	ok, err := t.enrolled(ctx, userID)
	if err != nil || !ok {
		return false, err
	}
	err = t.st.RecoveryCodes.Use(ctx, userID, auth.HashRecoveryCode(code))
	if errors.Is(err, store.ErrNotFound) {
		return false, nil
	}
	return err == nil, err
}

// verifyTOTP checks code against the user's seed; confirmed selects whether
// an active (true) or pending (false) enrolment is expected.
func (t *TOTPService) verifyTOTP(ctx context.Context, userID int64, code string, confirmed bool) (bool, error) {
	s, err := t.st.TOTPSecrets.Get(ctx, userID)
	if errors.Is(err, store.ErrNotFound) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if s.Confirmed() != confirmed {
		return false, nil
	}
	secret, err := t.aead.OpenString(s.SecretEnc, crypto.TOTPSecretAD(userID))
	if err != nil {
		return false, err
	}
	step, err := auth.ValidateTOTP(secret, code, t.st.Now(), s.LastUsedStep)
	if err != nil {
		return false, nil
	}
	if !confirmed {
		err = t.st.TOTPSecrets.Confirm(ctx, userID, step)
	} else {
		err = t.st.TOTPSecrets.UseStep(ctx, userID, step)
	}
	if errors.Is(err, store.ErrNotFound) {
		return false, nil // lost a race with a concurrent use of the same code
	}
	return err == nil, err
}

// enroll starts (or restarts) a pending enrolment and returns the seed.
func (t *TOTPService) enroll(ctx context.Context, user *store.User, issuer string) (auth.TOTPKey, error) {
	key, err := auth.GenerateTOTP(issuer, user.Username)
	if err != nil {
		return auth.TOTPKey{}, err
	}
	enc, err := t.aead.SealString(key.Secret, crypto.TOTPSecretAD(user.ID))
	if err != nil {
		return auth.TOTPKey{}, err
	}
	if err := t.st.TOTPSecrets.StartEnrolment(ctx, user.ID, enc); err != nil {
		return auth.TOTPKey{}, err
	}
	return key, nil
}

// newRecoveryCodes replaces the user's recovery codes and returns the plain
// codes (shown once).
func (t *TOTPService) newRecoveryCodes(ctx context.Context, st *store.Store, userID int64) ([]string, error) {
	codes, err := auth.GenerateRecoveryCodes(auth.RecoveryCodeCount)
	if err != nil {
		return nil, err
	}
	plain := make([]string, len(codes))
	hashes := make([][]byte, len(codes))
	for i, c := range codes {
		plain[i], hashes[i] = c.Plain, c.Hash
	}
	if err := st.RecoveryCodes.Replace(ctx, userID, hashes); err != nil {
		return nil, err
	}
	return plain, nil
}

// disable removes TOTP and recovery codes of a user.
func (t *TOTPService) disable(ctx context.Context, userID int64) error {
	return t.st.Tx(ctx, func(tx *store.Store) error {
		if err := tx.TOTPSecrets.Delete(ctx, userID); err != nil {
			return err
		}
		return tx.RecoveryCodes.DeleteAll(ctx, userID)
	})
}

// ResetUser removes a user's 2FA (admin "force 2FA reset"; for stream B2/CLI).
func (t *TOTPService) ResetUser(ctx context.Context, userID int64) error {
	return t.disable(ctx, userID)
}
