// Package mask hides personal data (UU PDP No. 27/2022) and secrets in logs,
// integration payloads and audit views.
package mask

import (
	"strings"
	"unicode/utf8"
)

// secretKeys are always fully redacted, everywhere (logs, integration logs,
// audit before/after). Matching is on the lower-cased key with "_" removed.
var secretKeys = map[string]bool{
	"password": true, "passwordhash": true, "newpassword": true, "currentpassword": true,
	"token": true, "secret": true, "apikey": true, "pin": true, "pinhash": true,
	"mfasecret": true, "mfasecretenc": true, "credentials": true, "credentialsenc": true,
	"authorization": true, "cvv": true, "cardnumber": true, "webhooksecret": true,
	"clientsecret": true, "privatekey": true, "signature": true, "tokenhash": true,
	"secrethash": true, "code": false,
}

// personalKeys are personal data; masked for viewers without the sensitive
// permission (FR-AUD-06) and always masked in logs.
var personalKeys = map[string]string{
	"email": "email", "phone": "phone", "mobile": "phone", "phonenumber": "phone",
	"nik": "id", "idnumber": "id", "npwp": "id", "passportnumber": "id",
	"dateofbirth": "dob", "birthdate": "dob", "address": "text",
}

func norm(k string) string {
	return strings.ReplaceAll(strings.ToLower(k), "_", "")
}

// IsSecretKey reports whether a field must always be redacted.
func IsSecretKey(k string) bool { return secretKeys[norm(k)] }

// PersonalKind returns the personal data kind for a key, or "".
func PersonalKind(k string) string { return personalKeys[norm(k)] }

// Email masks "dian.prasetyo@example.com" as "di***@example.com".
func Email(s string) string {
	at := strings.LastIndex(s, "@")
	if at <= 0 {
		return Text(s)
	}
	local := s[:at]
	keep := 2
	if utf8.RuneCountInString(local) <= 2 {
		keep = 1
	}
	return string([]rune(local)[:keep]) + "***" + s[at:]
}

// Phone masks all but the last 3 digits.
func Phone(s string) string {
	r := []rune(s)
	if len(r) <= 4 {
		return "***"
	}
	return strings.Repeat("*", len(r)-3) + string(r[len(r)-3:])
}

// Text masks all but the first character.
func Text(s string) string {
	r := []rune(s)
	if len(r) == 0 {
		return ""
	}
	return string(r[:1]) + "***"
}

// Value masks v according to kind.
func Value(kind string, v any) any {
	s, ok := v.(string)
	if !ok || s == "" {
		return v
	}
	switch kind {
	case "email":
		return Email(s)
	case "phone":
		return Phone(s)
	default:
		return Text(s)
	}
}

const Redacted = "[REDACTED]"

// Map returns a deep copy of m with secrets redacted and, when personal is
// true, personal data masked.
func Map(m map[string]any, personal bool) map[string]any {
	if m == nil {
		return nil
	}
	out := make(map[string]any, len(m))
	for k, v := range m {
		switch {
		case IsSecretKey(k):
			if v != nil && v != "" {
				out[k] = Redacted
			} else {
				out[k] = v
			}
		case personal && PersonalKind(k) != "":
			out[k] = Value(PersonalKind(k), v)
		default:
			out[k] = walk(v, personal)
		}
	}
	return out
}

func walk(v any, personal bool) any {
	switch t := v.(type) {
	case map[string]any:
		return Map(t, personal)
	case []any:
		cp := make([]any, len(t))
		for i := range t {
			cp[i] = walk(t[i], personal)
		}
		return cp
	default:
		return v
	}
}

// StripSecrets removes secret keys entirely (used before persisting audit
// before/after snapshots, so password hashes never reach the audit log).
func StripSecrets(m map[string]any) map[string]any {
	if m == nil {
		return nil
	}
	out := make(map[string]any, len(m))
	for k, v := range m {
		if IsSecretKey(k) {
			continue
		}
		if sub, ok := v.(map[string]any); ok {
			out[k] = StripSecrets(sub)
			continue
		}
		out[k] = v
	}
	return out
}
