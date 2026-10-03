package mask

import "testing"

func TestMap(t *testing.T) {
	in := map[string]any{"email": "dian.prasetyo@example.com", "phone": "+6281234567890", "password": "x", "name": "Dian",
		"nested": map[string]any{"apiKey": "k", "nik": "3171234567890001"}}
	out := Map(in, true)
	if out["email"] != "di***@example.com" || out["password"] != Redacted || out["name"] != "Dian" {
		t.Fatalf("%v", out)
	}
	n := out["nested"].(map[string]any)
	if n["apiKey"] != Redacted || n["nik"] != "3***" {
		t.Fatalf("%v", n)
	}
	if p := out["phone"].(string); p[len(p)-3:] != "890" || p[:3] != "***" {
		t.Fatalf("phone %s", p)
	}
	if s := StripSecrets(map[string]any{"passwordHash": "h", "a": 1}); len(s) != 1 {
		t.Fatalf("%v", s)
	}
}
