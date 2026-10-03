// Package password hashes passwords and PINs with argon2id (FR-IAM-02) and
// enforces the minimum password policy (FR-IAM-04).
package password

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"fmt"
	"strings"
	"unicode"

	"golang.org/x/crypto/argon2"

	"oneclub/internal/kernel/errs"
)

// Parameters follow OWASP guidance for argon2id (m=64 MiB, t=3, p=2).
const (
	memory  = 64 * 1024
	time    = 3
	threads = 2
	keyLen  = 32
	saltLen = 16
)

// Hash returns an encoded argon2id hash:
// $argon2id$v=19$m=65536,t=3,p=2$<salt>$<hash>
func Hash(plain string) (string, error) {
	salt := make([]byte, saltLen)
	if _, err := rand.Read(salt); err != nil {
		return "", err
	}
	key := argon2.IDKey([]byte(plain), salt, time, memory, threads, keyLen)
	return fmt.Sprintf("$argon2id$v=%d$m=%d,t=%d,p=%d$%s$%s", argon2.Version, memory, time, threads,
		base64.RawStdEncoding.EncodeToString(salt), base64.RawStdEncoding.EncodeToString(key)), nil
}

var errFormat = errors.New("password: invalid hash format")

// Verify checks plain against an encoded hash in constant time.
func Verify(plain, encoded string) (bool, error) {
	parts := strings.Split(encoded, "$")
	if len(parts) != 6 || parts[1] != "argon2id" {
		return false, errFormat
	}
	var v int
	if _, err := fmt.Sscanf(parts[2], "v=%d", &v); err != nil || v != argon2.Version {
		return false, errFormat
	}
	var m uint32
	var t uint32
	var p uint8
	if _, err := fmt.Sscanf(parts[3], "m=%d,t=%d,p=%d", &m, &t, &p); err != nil {
		return false, errFormat
	}
	salt, err := base64.RawStdEncoding.DecodeString(parts[4])
	if err != nil {
		return false, errFormat
	}
	want, err := base64.RawStdEncoding.DecodeString(parts[5])
	if err != nil {
		return false, errFormat
	}
	got := argon2.IDKey([]byte(plain), salt, t, m, p, uint32(len(want)))
	return subtle.ConstantTimeCompare(got, want) == 1, nil
}

// dummyHash is verified against when the user does not exist, so login
// timing does not reveal which e-mail addresses are registered.
var dummyHash, _ = Hash("oneclub-timing-equaliser")

// VerifyDummy burns the same CPU as a real verification.
func VerifyDummy(plain string) { _, _ = Verify(plain, dummyHash) }

// MinLength is the minimum password length.
const MinLength = 10

// CheckPolicy validates the minimum password policy (FR-IAM-04): at least
// 10 characters with upper case, lower case and a digit, and not containing
// the e-mail local part.
func CheckPolicy(plain, email string) error {
	var fields []errs.FieldError
	if len([]rune(plain)) < MinLength {
		fields = append(fields, errs.Field("password", "password_too_short", fmt.Sprintf("must be at least %d characters", MinLength)))
	}
	var up, low, digit bool
	for _, r := range plain {
		switch {
		case unicode.IsUpper(r):
			up = true
		case unicode.IsLower(r):
			low = true
		case unicode.IsDigit(r):
			digit = true
		}
	}
	if !up || !low || !digit {
		fields = append(fields, errs.Field("password", "password_too_weak", "must contain upper case, lower case and a digit"))
	}
	if local, _, ok := strings.Cut(strings.ToLower(email), "@"); ok && len(local) >= 3 && strings.Contains(strings.ToLower(plain), local) {
		fields = append(fields, errs.Field("password", "password_contains_email", "must not contain your e-mail name"))
	}
	if len(fields) > 0 {
		return errs.Validation("password_policy", "password does not meet the policy", fields...)
	}
	return nil
}

// CheckPIN validates a staff PIN (FR-IAM-09): 6 digits, not trivially weak.
func CheckPIN(pin string) error {
	if len(pin) != 6 {
		return errs.Validation("pin_invalid", "PIN must be 6 digits", errs.Field("pin", "pin_invalid", "PIN must be 6 digits"))
	}
	for _, r := range pin {
		if r < '0' || r > '9' {
			return errs.Validation("pin_invalid", "PIN must be 6 digits", errs.Field("pin", "pin_invalid", "PIN must be 6 digits"))
		}
	}
	if strings.Count(pin, pin[:1]) == 6 || pin == "123456" || pin == "654321" {
		return errs.Validation("pin_weak", "PIN is too easy to guess", errs.Field("pin", "pin_weak", "PIN is too easy to guess"))
	}
	return nil
}

// Generate returns a random password satisfying the policy.
func Generate() string {
	const (
		upper = "ABCDEFGHJKLMNPQRSTUVWXYZ"
		lower = "abcdefghijkmnopqrstuvwxyz"
		digit = "23456789"
		all   = upper + lower + digit
	)
	b := make([]byte, 16)
	_, _ = rand.Read(b)
	out := []byte{upper[int(b[0])%len(upper)], lower[int(b[1])%len(lower)], digit[int(b[2])%len(digit)]}
	for _, x := range b[3:] {
		out = append(out, all[int(x)%len(all)])
	}
	return string(out)
}
