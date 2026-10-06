package integration

import (
	"context"
	"regexp"
	"strings"
	"testing"
)

// PRD P3 §16 #18: the sandbox e-Meterai issues MOCK serials, logs its calls
// without the document content and simulates provider failures.
func TestMockEMeterai(t *testing.T) {
	rec := &recorder{}
	m := &mockEMeterai{env: Env{Code: "mock-emeterai", Mode: "sandbox", Log: rec.log}}
	ctx := context.Background()
	s, err := m.Stamp(ctx, EMeteraiStampRequest{DocumentRef: "q-1", DocumentType: "quotation", DocumentNumber: "QUO-2026-00001 v1",
		Amount: "6000000", Currency: "IDR", Signer: "Budi", PDF: []byte("%PDF-1.4 secret content")})
	if err != nil || s.Status != "stamped" || !s.Sandbox || s.StampedAt.IsZero() || s.Reference == "" ||
		!regexp.MustCompile(`^MOCK-EMT-\d{8}-[0-9A-F]{8}$`).MatchString(s.SerialNumber) {
		t.Fatalf("stamp: %+v %v", s, err)
	}
	if st, err := m.Status(ctx, s.SerialNumber); err != nil || st.Status != "stamped" {
		t.Fatalf("status: %+v %v", st, err)
	}
	if _, err := m.Status(ctx, "EMT-UNKNOWN"); err == nil {
		t.Fatal("unknown serial")
	}
	if _, err := m.Stamp(ctx, EMeteraiStampRequest{DocumentRef: "q-2"}); err == nil {
		t.Fatal("a stamp needs the PDF")
	}
	if d := rec.dump(); strings.Contains(d, "secret content") || !strings.Contains(d, "QUO-2026-00001 v1") {
		t.Fatalf("call log: %s", d)
	}
	failing := &mockEMeterai{env: Env{Settings: map[string]any{"alwaysFail": true}}}
	if s, err := failing.Stamp(ctx, EMeteraiStampRequest{DocumentRef: "q-3", PDF: []byte("x")}); err == nil || s.Status != "failed" {
		t.Fatalf("simulated failure: %+v %v", s, err)
	}
	if _, err := m.TestConnection(ctx); err != nil {
		t.Fatal(err)
	}
}

// One-time codes marked with WithRedactions never reach the call log.
func TestRedactions(t *testing.T) {
	ctx := WithRedactions(context.Background(), "482913", "12") // too short values are ignored
	ctx = WithRedactions(ctx, `a"b\c`)
	got := string(redact(ctx, []byte(`{"text":"Your code is 482913","x":"a\"b\\c","n":"12"}`)))
	if want := `{"text":"Your code is [REDACTED]","x":"[REDACTED]","n":"12"}`; got != want {
		t.Fatalf("redact: %s", got)
	}
	if got := string(redact(context.Background(), []byte(`{"a":"482913"}`))); got != `{"a":"482913"}` {
		t.Fatalf("no redactions: %s", got)
	}
}
