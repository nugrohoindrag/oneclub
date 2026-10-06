package notification

import (
	"reflect"
	"testing"

	"oneclub/internal/kernel/mask"
)

// PO decision 4f: secret template variables are erased from the stored
// delivery (payload, subject, body) once it reached its final status.
func TestEraseSecrets(t *testing.T) {
	payload := map[string]any{"number": "QUO-1", "otpCode": "482913", "token": "ab", "name": "Dian", "expiresInMinutes": "10"}
	p, subj, body := eraseSecrets(payload, "Code for QUO-1", "482913 is your code for QUO-1. Hi Dian, valid 10 minutes.")
	want := map[string]any{"number": "QUO-1", "otpCode": mask.Redacted, "token": mask.Redacted, "name": "Dian", "expiresInMinutes": "10"}
	if !reflect.DeepEqual(p, want) {
		t.Fatalf("payload: %v", p)
	}
	if subj != "Code for QUO-1" || body != "[REDACTED] is your code for QUO-1. Hi Dian, valid 10 minutes." {
		t.Fatalf("subject %q body %q", subj, body)
	}
	if payload["otpCode"] != "482913" {
		t.Fatal("the input payload is not modified")
	}
	if !secretsErased(p) || secretsErased(payload) {
		t.Fatal("secretsErased")
	}
	// No secret: unchanged.
	p, subj, body = eraseSecrets(map[string]any{"code": "BK-1234"}, "s", "BK-1234")
	if p["code"] != "BK-1234" || subj != "s" || body != "BK-1234" {
		t.Fatalf("no secret: %v %q %q", p, subj, body)
	}
	if p, _, _ := eraseSecrets(nil, "", ""); p != nil {
		t.Fatal("nil payload")
	}
}
