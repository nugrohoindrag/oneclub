package sales

import (
	"strings"
	"testing"

	"github.com/google/uuid"
)

func TestAcceptanceOtpHash(t *testing.T) {
	m := &Module{OTPKey: []byte("instance-secret-0123456789abcdef")}
	q := uuid.New()
	h := m.otpHash("salt", q, "482913")
	if len(h) != 64 || strings.Contains(h, "482913") {
		t.Fatalf("hash: %s", h)
	}
	if !m.otpMatches("salt", q, "482913", h) {
		t.Fatal("the right code matches")
	}
	for _, bad := range []struct {
		salt string
		q    uuid.UUID
		code string
	}{{"salt", q, "482914"}, {"other", q, "482913"}, {"salt", uuid.New(), "482913"}} {
		if m.otpMatches(bad.salt, bad.q, bad.code, h) {
			t.Fatalf("must not match: %+v", bad)
		}
	}
	if (&Module{OTPKey: []byte("another-instance")}).otpMatches("salt", q, "482913", h) {
		t.Fatal("the pepper is part of the hash")
	}
	if m.otpMatches("salt", q, "482913", "not-hex") {
		t.Fatal("a corrupt hash never matches")
	}
	for range 50 {
		c, err := randomDigits(otpDigits)
		if err != nil || len(c) != 6 || strings.Trim(c, "0123456789") != "" {
			t.Fatalf("code %q %v", c, err)
		}
	}
}

func TestEMeteraiThreshold(t *testing.T) {
	pol := NewDefaultPolicy()
	if !pol.RequireAcceptanceOtp || pol.OtpTTLMinutes != 10 || pol.OtpMaxAttempts != 5 || !pol.EMeteraiOnAcceptance {
		t.Fatalf("defaults: %+v", pol)
	}
	for total, want := range map[string]bool{"5000000": false, "5000000.01": true, "4999999": false, "6000000": true} {
		if got := eMeteraiRequired(pol, total); got != want {
			t.Fatalf("%s: %v", total, got)
		}
	}
	pol.EMeteraiThreshold = ""
	if eMeteraiRequired(pol, "900000000") {
		t.Fatal("an empty threshold disables e-Meterai")
	}
	pol.OtpTTLMinutes, pol.OtpMaxAttempts = 0, 99
	if otpTTL(pol).Minutes() != 1 || otpMaxAttempts(pol) != 10 {
		t.Fatal("bounds of the code policy")
	}
}
