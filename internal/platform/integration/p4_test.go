package integration

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

// recorder collects the calls an adapter logs (FR-INT-P4-08).
type recorder struct {
	mu    sync.Mutex
	calls []Call
}

func (r *recorder) log(_ context.Context, c Call) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.calls = append(r.calls, c)
}

func (r *recorder) dump() string {
	r.mu.Lock()
	defer r.mu.Unlock()
	raw, _ := json.Marshal(r.calls)
	var parts []string
	for _, c := range r.calls {
		parts = append(parts, fmt.Sprintf("%s %s %v %v", c.Operation, c.URL, c.Request, c.Response))
	}
	return string(raw) + strings.Join(parts, " ")
}

func TestXenditSettlementDetailsPerMethod(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if u, _, ok := r.BasicAuth(); !ok || u != "xnd_secret" || r.URL.Path != "/transactions" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		if r.URL.Query().Get("after_id") == "" {
			_, _ = io.WriteString(w, `{"has_more":true,"data":[
			 {"id":"txn_1","product_id":"inv_1","reference_id":"PAY-1","channel_category":"QR_CODE","channel_code":"QRIS","amount":100000,"net_amount":99300,
			  "status":"SUCCESS","settlement_status":"SETTLED","updated":"2026-10-01T03:00:00Z","fee":{"xendit_fee":630,"value_added_tax":70}},
			 {"id":"txn_2","product_id":"inv_2","reference_id":"PAY-2","channel_category":"VIRTUAL_ACCOUNT","channel_code":"BCA","amount":1500000,
			  "status":"SUCCESS","settlement_status":"PENDING","updated":"2026-10-01T04:00:00Z","fee":{"xendit_fee":4000,"value_added_tax":440}}]}`)
			return
		}
		_, _ = io.WriteString(w, `{"has_more":false,"data":[{"id":"txn_3","reference_id":"PAY-3","channel_category":"CARDS","channel_code":"CREDIT_CARD",
		 "amount":500000,"status":"FAILED","settlement_status":"","updated":"2026-10-01T05:00:00Z","fee":{}}]}`)
	}))
	defer srv.Close()
	rec := &recorder{}
	x, err := newXendit(Env{Code: "xendit", Mode: "production", Credentials: map[string]string{"secretKey": "xnd_secret"},
		Settings: map[string]any{"baseUrl": srv.URL}, Log: rec.log})
	if err != nil {
		t.Fatal(err)
	}
	items, err := x.SettlementDetails(context.Background(), time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC), time.Date(2026, 10, 2, 0, 0, 0, 0, time.UTC))
	if err != nil || len(items) != 3 {
		t.Fatalf("details: %v %v", items, err)
	}
	if items[0].Method != "qris" || items[0].Fee != "700" || items[0].Net != "99300" || items[1].Net != "1495560" || items[1].Status != "pending" ||
		items[2].Method != "card" || items[2].Status != "failed" {
		t.Fatalf("mapping: %+v", items)
	}
	sum := SummarizeSettlement("xendit", time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC), time.Date(2026, 10, 2, 0, 0, 0, 0, time.UTC), items, false)
	if len(sum.Methods) != 3 || sum.Total.Gross != "1600000" || sum.Total.Fees != "5140" || sum.Total.Failed != 1 || sum.To != "2026-10-01" {
		t.Fatalf("summary: %+v", sum)
	}
	if strings.Contains(rec.dump(), "xnd_secret") || len(rec.calls) != 2 {
		t.Fatalf("logged calls (no key): %s", rec.dump())
	}
}

func TestCoretaxPJAPSignedCalls(t *testing.T) {
	var seen []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		if r.Header.Get("X-Api-Key") != "key-1" ||
			r.Header.Get("X-Signature") != CoretaxSignature("sec-1", r.Method, r.URL.Path, r.Header.Get("X-Timestamp"), body) {
			w.WriteHeader(http.StatusUnauthorized)
			_, _ = io.WriteString(w, `{"error":"bad signature"}`)
			return
		}
		seen = append(seen, r.Method+" "+r.URL.Path)
		switch {
		case r.URL.Path == "/v1/tax-invoices":
			var in map[string]any
			_ = json.Unmarshal(body, &in)
			if in["sellerNpwp"] != "0123456789012345" || in["vatAmount"] != "110000" {
				w.WriteHeader(http.StatusUnprocessableEntity)
				return
			}
			_, _ = io.WriteString(w, `{"id":"fk-1","taxInvoiceNumber":"04002600000001","status":"APPROVAL_SUCCESS"}`)
		case strings.HasSuffix(r.URL.Path, "/cancel"):
			_, _ = io.WriteString(w, `{"id":"fk-1","status":"CANCELLED"}`)
		case r.URL.Path == "/v1/tax-invoices/fk-1":
			_, _ = io.WriteString(w, `{"id":"fk-1","taxInvoiceNumber":"04002600000001","status":"approved"}`)
		case r.URL.Path == "/v1/tax-invoice-batches":
			var in map[string]any
			_ = json.Unmarshal(body, &in)
			raw, _ := base64.StdEncoding.DecodeString(fmt.Sprint(in["content"]))
			if !strings.HasPrefix(string(raw), "<TaxInvoiceBulk") {
				w.WriteHeader(http.StatusUnprocessableEntity)
				return
			}
			_, _ = io.WriteString(w, `{"id":"b-1","status":"PROCESSING","accepted":0,"rejected":0}`)
		case r.URL.Path == "/v1/tax-invoice-batches/b-1":
			_, _ = io.WriteString(w, `{"id":"b-1","status":"COMPLETED","accepted":2,"invoices":[{"id":"fk-2","taxInvoiceNumber":"2","status":"approved"}]}`)
		case r.URL.Path == "/v1/ping":
			_, _ = io.WriteString(w, `{"ok":true}`)
		}
	}))
	defer srv.Close()
	rec := &recorder{}
	if _, err := newCoretax(Env{Credentials: map[string]string{"apiKey": "k", "apiSecret": "s", "npwp": "123"}, Settings: map[string]any{"baseUrl": srv.URL}}); err == nil {
		t.Fatal("NPWP must have 15/16 digits")
	}
	a, err := newCoretax(Env{Mode: "sandbox", Credentials: map[string]string{"apiKey": "key-1", "apiSecret": "sec-1", "npwp": "0123456789012345"},
		Settings: map[string]any{"baseUrl": srv.URL}, Log: rec.log})
	if err != nil {
		t.Fatal(err)
	}
	var svc TaxInvoiceService = a
	ctx := context.Background()
	res, err := svc.SubmitInvoice(ctx, TaxInvoice{Reference: "INV-1", BuyerNPWP: "0987654321098765", BuyerName: "PT Buyer", TotalAmount: "1110000",
		TaxAmount: "110000", IssuedAt: time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)})
	if err != nil || res.Status != "approved" || res.Number != "04002600000001" {
		t.Fatalf("submit: %+v %v", res, err)
	}
	if st, err := svc.InvoiceStatus(ctx, "fk-1"); err != nil || st.Status != "approved" {
		t.Fatalf("status: %+v %v", st, err)
	}
	if c, err := svc.CancelInvoice(ctx, "fk-1", "wrong buyer"); err != nil || c.Status != "cancelled" {
		t.Fatalf("cancel: %+v %v", c, err)
	}
	b, err := svc.UploadBatch(ctx, TaxInvoiceBatch{BatchRef: "EFK-2026-10", Period: "2026-10", Format: "coretax_xml", Filename: "efaktur.xml",
		Content: []byte("<TaxInvoiceBulk/>"), InvoiceCount: 2})
	if err != nil || b.BatchID != "b-1" || b.Status != "processing" {
		t.Fatalf("upload: %+v %v", b, err)
	}
	if bs, err := svc.BatchStatus(ctx, "b-1"); err != nil || bs.Status != "completed" || len(bs.Invoices) != 1 {
		t.Fatalf("batch status: %+v %v", bs, err)
	}
	if _, err := a.TestConnection(ctx); err != nil {
		t.Fatal(err)
	}
	log := rec.dump()
	if strings.Contains(log, "sec-1") || strings.Contains(log, "PEZhdEludm9pY2VCdWxrLz4") || !strings.Contains(log, "bytes base64") {
		t.Fatalf("log keeps no secret and no file content: %s", log)
	}
	// A tampered signature is refused by the provider.
	bad, _ := newCoretax(Env{Credentials: map[string]string{"apiKey": "key-1", "apiSecret": "other", "npwp": "0123456789012345"}, Settings: map[string]any{"baseUrl": srv.URL}})
	if _, err := bad.InvoiceStatus(ctx, "fk-1"); err == nil || !strings.Contains(err.Error(), "401") {
		t.Fatalf("bad signature: %v", err)
	}
	if len(seen) != 6 {
		t.Fatalf("calls: %v", seen)
	}
	// The sandbox implements the whole service too.
	var mock TaxInvoiceService = &mockTaxInvoice{env: Env{}}
	mb, err := mock.UploadBatch(ctx, TaxInvoiceBatch{Format: "coretax_xml", Content: []byte("<x/>"), InvoiceCount: 2})
	if err != nil || mb.Accepted != 2 {
		t.Fatalf("mock batch: %+v %v", mb, err)
	}
	if got, err := mock.BatchStatus(ctx, mb.BatchID); err != nil || got.BatchID != mb.BatchID {
		t.Fatalf("mock batch status: %v", err)
	}
	if _, err := mock.CancelInvoice(ctx, "x", ""); err == nil {
		t.Fatal("mock cancel needs a reason")
	}
}

func TestModernlandResidentLookupLogsNoPersonalData(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-Api-Key") != "ml-key" {
			w.WriteHeader(http.StatusForbidden)
			return
		}
		_, _ = io.WriteString(w, `{"items":[{"id":"RES-7","name":"Budi Santoso","address":"Jl. Modernland 7","unit":"B-7","status":"aktif"},
		 {"id":"RES-8","name":"Budi Lama","address":"Jl. Modernland 8","status":"pindah"}]}`)
	}))
	defer srv.Close()
	rec := &recorder{}
	if _, err := newModernland(Env{Credentials: map[string]string{"apiKey": "x"}, Settings: map[string]any{"baseUrl": "ftp://x"}}); err == nil {
		t.Fatal("base URL must be http(s)")
	}
	m, err := newModernland(Env{Credentials: map[string]string{"apiKey": "ml-key"}, Settings: map[string]any{"baseUrl": srv.URL}, Log: rec.log})
	if err != nil {
		t.Fatal(err)
	}
	res, err := m.LookupResident(context.Background(), "Budi Santoso")
	if err != nil || len(res) != 2 || res[0].Status != "active" || res[1].Status != "inactive" || res[0].Address != "Jl. Modernland 7 (B-7)" {
		t.Fatalf("lookup: %+v %v", res, err)
	}
	if log := rec.dump(); strings.Contains(log, "Budi") || strings.Contains(log, "Modernland 7") || strings.Contains(log, "ml-key") || !strings.Contains(log, "count") {
		t.Fatalf("personal data in the log: %s", log)
	}
	if _, err := m.LookupResident(context.Background(), " "); err == nil {
		t.Fatal("empty search")
	}
}

func TestWebsiteRevalidationSigned(t *testing.T) {
	var got RevalidateRequest
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		if VerifyHMAC(r.Header.Get(SignatureHeader), "0123456789abcdef-shared", body, time.Now()) != nil {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		_ = json.Unmarshal(body, &got)
		_, _ = io.WriteString(w, `{"revalidated":true}`)
	}))
	defer srv.Close()
	if _, err := newNextWebsite(Env{Credentials: map[string]string{"revalidateSecret": "short"}, Settings: map[string]any{"baseUrl": srv.URL}}); err == nil {
		t.Fatal("short secret")
	}
	w, err := newNextWebsite(Env{Credentials: map[string]string{"revalidateSecret": "0123456789abcdef-shared"}, Settings: map[string]any{"baseUrl": srv.URL}})
	if err != nil {
		t.Fatal(err)
	}
	if err := w.Revalidate(context.Background(), RevalidateRequest{Revision: 9, Paths: []string{"/id/golf"}}); err != nil || got.Revision != 9 ||
		got.Tags[0] != "cms" || got.Paths[0] != "/id/golf" {
		t.Fatalf("revalidate: %+v %v", got, err)
	}
	wrong, _ := newNextWebsite(Env{Credentials: map[string]string{"revalidateSecret": "another-secret-of-16+"}, Settings: map[string]any{"baseUrl": srv.URL}})
	if err := wrong.Revalidate(context.Background(), RevalidateRequest{}); err == nil {
		t.Fatal("wrong secret must be refused")
	}
	mw := &mockWebsite{env: Env{Settings: map[string]any{"alwaysFail": true}}}
	if err := mw.Revalidate(context.Background(), RevalidateRequest{}); err == nil {
		t.Fatal("simulated outage")
	}
}

func TestSendGridMailAndMaskedLog(t *testing.T) {
	var body map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer SG.key" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		switch r.URL.Path {
		case "/v3/mail/send":
			_ = json.NewDecoder(r.Body).Decode(&body)
			w.WriteHeader(http.StatusAccepted)
		case "/v3/scopes":
			_, _ = io.WriteString(w, `{"scopes":["mail.send"]}`)
		}
	}))
	defer srv.Close()
	rec := &recorder{}
	if _, err := newSendGrid(Env{Credentials: map[string]string{"apiKey": "SG.key"}, Settings: map[string]any{"from": "not an address"}}); err == nil {
		t.Fatal("from address")
	}
	sg, err := newSendGrid(Env{Mode: "sandbox", Credentials: map[string]string{"apiKey": "SG.key"},
		Settings: map[string]any{"from": "no-reply@moderngolf.test", "fromName": "Modern Golf", "baseUrl": srv.URL, "replyTo": "info@moderngolf.test"}, Log: rec.log})
	if err != nil {
		t.Fatal(err)
	}
	if err := sg.SendEmail(context.Background(), Email{To: "dian.prasetyo@example.com", ToName: "Dian", Subject: "PO-0001", Text: "secret body text",
		HTML: "<p>secret body</p>"}); err != nil {
		t.Fatal(err)
	}
	from := body["from"].(map[string]any)
	if from["email"] != "no-reply@moderngolf.test" || from["name"] != "Modern Golf" || len(body["content"].([]any)) != 2 ||
		body["mail_settings"].(map[string]any)["sandbox_mode"].(map[string]any)["enable"] != true {
		t.Fatalf("mail: %v", body)
	}
	if msg, err := sg.TestConnection(context.Background()); err != nil || !strings.Contains(msg, "SendGrid") {
		t.Fatalf("test: %v %v", msg, err)
	}
	log := rec.dump()
	if strings.Contains(log, "secret body") || strings.Contains(log, "SG.key") || !strings.Contains(log, "PO-0001") {
		t.Fatalf("log: %s", log)
	}
	if err := sg.SendEmail(context.Background(), Email{To: "bad"}); err == nil {
		t.Fatal("invalid recipient")
	}
}

func TestCheckEmailDomain(t *testing.T) {
	old := LookupTXT
	defer func() { LookupTXT = old }()
	dns := map[string][]string{
		"club.test":               {"v=spf1 include:sendgrid.net ~all"},
		"s1._domainkey.club.test": {"v=DKIM1; k=rsa; p=MIGf"},
		"_dmarc.club.test":        {"v=DMARC1; p=none"},
		"two.test":                {"v=spf1 -all", "v=spf1 include:x ~all"},
		"open.test":               {"v=spf1 +all"},
	}
	LookupTXT = func(_ context.Context, name string) ([]string, error) {
		if name == "broken.test" {
			return nil, &net.DNSError{Err: "server misbehaving", Name: name, IsTemporary: true}
		}
		if v, ok := dns[name]; ok {
			return v, nil
		}
		return nil, &net.DNSError{Err: "no such host", Name: name, IsNotFound: true}
	}
	st := func(checks []DNSRecordCheck) string {
		var out []string
		for _, c := range checks {
			out = append(out, c.Record+"="+c.Status)
		}
		return strings.Join(out, ",")
	}
	c, err := CheckEmailDomain(context.Background(), "Club.Test.", []string{"s1", "s2"}, "sendgrid.net")
	if err != nil || st(c) != "spf=pass,dkim=pass,dkim=missing,dmarc=warning" {
		t.Fatalf("club: %s %v", st(c), err)
	}
	if c, _ := CheckEmailDomain(context.Background(), "two.test", nil, ""); st(c) != "spf=invalid,dmarc=missing" {
		t.Fatalf("two SPF records: %s", st(c))
	}
	if c, _ := CheckEmailDomain(context.Background(), "open.test", nil, ""); st(c) != "spf=invalid,dmarc=missing" {
		t.Fatalf("+all: %s", st(c))
	}
	if _, err := CheckEmailDomain(context.Background(), "broken.test", nil, ""); err == nil {
		t.Fatal("DNS failure")
	}
	if _, err := CheckEmailDomain(context.Background(), "no domain", nil, ""); err == nil {
		t.Fatal("invalid domain")
	}
	d, sels, inc := emailDomainDefaults("sendgrid", map[string]any{"from": "Club <a@club.test>"}, nil, EmailDomainCheckInput{})
	if d != "club.test" || len(sels) != 2 || inc != "sendgrid.net" {
		t.Fatalf("defaults: %s %v %s", d, sels, inc)
	}
	if d, _, _ := emailDomainDefaults("smtp", nil, map[string]string{"from": "x@smtp.test"}, EmailDomainCheckInput{}); d != "smtp.test" {
		t.Fatalf("smtp from: %s", d)
	}
}

func TestRedactedURLAndHardwareProfiles(t *testing.T) {
	if u := redactURL("https://x.test/r?q=Budi&apiKey=abc&limit=20&email=a@b.c"); strings.Contains(u, "Budi") || strings.Contains(u, "abc") ||
		strings.Contains(u, "a@b.c") || !strings.Contains(u, "limit=20") {
		t.Fatalf("redact: %s", u)
	}
	for _, c := range []struct {
		kind, cmd string
		payload   map[string]any
		ok        bool
	}{
		{"locker", "assign", map[string]any{"locker": "L-12", "cardUid": "04A2"}, true},
		{"locker", "assign", map[string]any{"locker": "L-12"}, false},
		{"locker", "open", map[string]any{"locker": "L-1", "reason": "lost key", "force": true}, false},
		{"ball_dispenser", "dispense", map[string]any{"balls": 1000}, false},
		{"turnstile", "sync_access_list", map[string]any{"credentials": []any{"a", "b"}}, true},
		{"turnstile", "grant", map[string]any{"credential": "QR1", "zones": "pool"}, false},
		{"golf_cart_gps", "stop", nil, false},
		{"printer", "print", nil, false},
	} {
		if err := ValidateHardwareCommand(c.kind, c.cmd, c.payload); (err == nil) != c.ok {
			t.Fatalf("%s %s %v: %v", c.kind, c.cmd, c.payload, err)
		}
	}
}
