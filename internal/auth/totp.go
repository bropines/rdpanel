package auth

import (
	"crypto/subtle"
	"errors"
	"strings"
	"time"

	"github.com/pquerna/otp"
	"github.com/pquerna/otp/totp"
)

// TOTP parameters: RFC 6238 defaults understood by every authenticator app.
const (
	totpPeriod = 30
	totpDigits = otp.DigitsSix
	totpSkew   = 1 // accept one step before/after
)

// ErrTOTPInvalid is returned for a wrong, malformed or replayed code.
var ErrTOTPInvalid = errors.New("auth: invalid TOTP code")

// TOTPKey is a newly generated TOTP secret for enrolment.
type TOTPKey struct {
	// Secret is the base32 seed; store it AEAD-encrypted.
	Secret string
	// URL is the otpauth:// URI to render as a QR code.
	URL string
}

// GenerateTOTP creates a new TOTP seed for account (usually the username).
func GenerateTOTP(issuer, account string) (TOTPKey, error) {
	k, err := totp.Generate(totp.GenerateOpts{
		Issuer:      issuer,
		AccountName: account,
		Period:      totpPeriod,
		Digits:      totpDigits,
		Algorithm:   otp.AlgorithmSHA1,
		SecretSize:  20,
	})
	if err != nil {
		return TOTPKey{}, err
	}
	return TOTPKey{Secret: k.Secret(), URL: k.URL()}, nil
}

// TOTPCode returns the code for secret at t (used by tests and tooling).
func TOTPCode(secret string, t time.Time) (string, error) {
	return totp.GenerateCodeCustom(secret, t, totp.ValidateOpts{
		Period: totpPeriod, Digits: totpDigits, Algorithm: otp.AlgorithmSHA1,
	})
}

// ValidateTOTP checks code against secret at time now, allowing ±1 step of
// clock skew. lastStep is the last step accepted for this user (0 if none);
// a code whose step is <= lastStep is rejected as a replay. On success the
// matched step is returned and must be persisted as the new lastStep.
func ValidateTOTP(secret, code string, now time.Time, lastStep int64) (int64, error) {
	code = strings.ReplaceAll(strings.TrimSpace(code), " ", "")
	if len(code) != int(totpDigits) {
		return 0, ErrTOTPInvalid
	}
	cur := now.Unix() / totpPeriod
	for d := int64(-totpSkew); d <= totpSkew; d++ {
		step := cur + d
		if step <= lastStep {
			continue
		}
		want, err := TOTPCode(secret, time.Unix(step*totpPeriod, 0))
		if err != nil {
			return 0, ErrTOTPInvalid
		}
		if subtle.ConstantTimeCompare([]byte(want), []byte(code)) == 1 {
			return step, nil
		}
	}
	return 0, ErrTOTPInvalid
}
