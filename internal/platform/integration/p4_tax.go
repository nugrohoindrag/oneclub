package integration

// PRD P4 FR-INT-P4-02: e-Faktur / Coretax. The tax invoice documents and
// the official export file (Coretax XML) are produced by the accounting
// module (Technical Doc §8.2); the integration layer provides the
// configuration (encrypted credentials, sandbox / production) and the
// transport: submit one invoice, check its status, cancel it, upload a
// batch file and follow the batch. "coretax-pjap" talks to a PJAP
// (Penyedia Jasa Aplikasi Perpajakan, the DJP-appointed host-to-host
// provider) REST API with HMAC-signed requests; "mock-efaktur" is the
// sandbox. When no API is used the accounting export file is uploaded in
// the Coretax portal manually (fallback, PRD P4 §15 risk #7).

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"oneclub/internal/kernel/id"
)

// TaxInvoiceBatch is an export file of tax invoices (produced by accounting).
type TaxInvoiceBatch struct {
	BatchRef     string // OneClub reference, e.g. EFK-2026-10
	Period       string // tax period YYYY-MM
	Format       string // coretax_xml | efaktur_csv
	Filename     string
	Content      []byte
	InvoiceCount int
}

// TaxBatchResult is the provider's view of a batch.
type TaxBatchResult struct {
	BatchID  string             `json:"batchId"`
	Status   string             `json:"status" enum:"received,processing,completed,failed"`
	Accepted int                `json:"accepted"`
	Rejected int                `json:"rejected"`
	Messages []string           `json:"messages"`
	Invoices []TaxInvoiceResult `json:"invoices"`
}

// TaxInvoiceManager follows and cancels submitted tax invoices.
type TaxInvoiceManager interface {
	InvoiceStatus(ctx context.Context, externalID string) (TaxInvoiceResult, error)
	CancelInvoice(ctx context.Context, externalID, reason string) (TaxInvoiceResult, error)
}

// TaxBatchUploader uploads official export files.
type TaxBatchUploader interface {
	UploadBatch(ctx context.Context, b TaxInvoiceBatch) (TaxBatchResult, error)
	BatchStatus(ctx context.Context, batchID string) (TaxBatchResult, error)
}

func init() {
	RegisterAdapter(AdapterInfo{
		Key: "coretax-pjap", Capability: CapTaxInvoice, Name: "Coretax via PJAP (host-to-host)",
		Description: "e-Faktur / Coretax through a DJP-appointed PJAP REST API: submit, status, cancel and batch upload of the Coretax XML " +
			"export produced by Accounting. Requests are signed with HMAC-SHA256.",
		Credentials: []Field{
			{Key: "apiKey", Label: "API key", Type: "secret", Required: true},
			{Key: "apiSecret", Label: "API secret (request signature)", Type: "secret", Required: true},
			{Key: "npwp", Label: "Seller NPWP (16 digits)", Type: "string", Required: true},
		},
		Settings: []Field{
			{Key: "baseUrl", Label: "PJAP API base URL", Type: "url", Required: true},
			{Key: "nitku", Label: "NITKU (branch identity)", Type: "string"},
		},
		New: func(env Env) (any, error) { return newCoretax(env) },
	})
}

// ── resolution ────────────────────────────────────────────────────────────

// TaxInvoiceService is what Accounting calls (FR-INT-P4-02, Technical Doc
// §8.2 "from module accounting"): submit one invoice, follow and cancel it,
// upload the official export file and follow the batch. Every call is
// logged (masked) as an integration call of the e-Faktur integration.
type TaxInvoiceService interface {
	TaxInvoiceAdapter
	TaxInvoiceManager
	TaxBatchUploader
}

var _ TaxInvoiceService = (*coretax)(nil)
var _ TaxInvoiceService = (*mockTaxInvoice)(nil)

// TaxInvoice resolves the e-Faktur capability (manager / batch uploader
// through type assertions).
func (s *Service) TaxInvoice(ctx context.Context) (TaxInvoiceAdapter, string, error) {
	a, code, err := s.Resolve(ctx, CapTaxInvoice)
	if err != nil {
		return nil, "", err
	}
	t, ok := a.(TaxInvoiceAdapter)
	if !ok {
		return nil, "", fmt.Errorf("integration %s does not implement tax invoices", code)
	}
	return t, code, nil
}

// TaxInvoices resolves the enabled e-Faktur integration with its full
// service (ErrNotConfigured: Accounting falls back to the export file that
// Finance uploads in the Coretax portal).
func (s *Service) TaxInvoices(ctx context.Context) (TaxInvoiceService, string, error) {
	a, code, err := s.Resolve(ctx, CapTaxInvoice)
	if err != nil {
		return nil, "", err
	}
	t, ok := a.(TaxInvoiceService)
	if !ok {
		return nil, "", fmt.Errorf("integration %s does not implement the e-Faktur service", code)
	}
	return t, code, nil
}

// Residents resolves the Modernland resident data capability (FR-INT-P4-07).
func (s *Service) Residents(ctx context.Context) (ResidentDataAdapter, string, error) {
	a, code, err := s.Resolve(ctx, CapResidentData)
	if err != nil {
		return nil, "", err
	}
	r, ok := a.(ResidentDataAdapter)
	if !ok {
		return nil, "", fmt.Errorf("integration %s does not implement resident data", code)
	}
	return r, code, nil
}

// ── coretax via PJAP ──────────────────────────────────────────────────────

type coretax struct {
	env  Env
	base string
}

var npwpRe = regexp.MustCompile(`^[0-9]{15,16}$`)

func newCoretax(env Env) (*coretax, error) {
	base := strings.TrimRight(setting(env, "baseUrl", ""), "/")
	if base == "" {
		return nil, errors.New("coretax: API base URL is required")
	}
	if env.Credentials["apiKey"] == "" || env.Credentials["apiSecret"] == "" {
		return nil, errors.New("coretax: API key and secret are required")
	}
	if n := strings.NewReplacer(".", "", "-", "").Replace(env.Credentials["npwp"]); !npwpRe.MatchString(n) {
		return nil, errors.New("coretax: seller NPWP must have 15 or 16 digits")
	}
	return &coretax{env: env, base: base}, nil
}

// CoretaxSignature signs a PJAP request: hex(HMAC-SHA256(secret,
// METHOD \n path \n timestamp \n hex(sha256(body)))).
func CoretaxSignature(secret, method, path, timestamp string, body []byte) string {
	sum := sha256.Sum256(body)
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(method + "\n" + path + "\n" + timestamp + "\n" + hex.EncodeToString(sum[:])))
	return hex.EncodeToString(mac.Sum(nil))
}

func (c *coretax) call(ctx context.Context, op, method, path string, body any, out any) error {
	start := time.Now()
	var raw []byte
	if body != nil {
		var err error
		if raw, err = json.Marshal(body); err != nil {
			return err
		}
	}
	u, err := url.Parse(c.base + path)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, method, u.String(), bytes.NewReader(raw))
	if err != nil {
		return err
	}
	ts := strconv.FormatInt(time.Now().Unix(), 10)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Api-Key", c.env.Credentials["apiKey"])
	req.Header.Set("X-Timestamp", ts)
	req.Header.Set("X-Signature", CoretaxSignature(c.env.Credentials["apiSecret"], method, u.Path, ts, raw))
	resp, err := HTTPClient.Do(req)
	code := 0
	var respBody []byte
	if err == nil {
		code = resp.StatusCode
		respBody, _ = io.ReadAll(io.LimitReader(resp.Body, 1<<20))
		resp.Body.Close()
		if code >= 300 {
			err = fmt.Errorf("%s: HTTP %d: %s", op, code, truncate(string(respBody), 300))
		} else if out != nil && len(respBody) > 0 {
			err = json.Unmarshal(respBody, out)
		}
	}
	if c.env.Log != nil {
		var logged any
		_ = json.Unmarshal(respBody, &logged)
		reqLog := any(body)
		if b, ok := body.(map[string]any); ok && b["content"] != nil {
			cp := map[string]any{}
			for k, v := range b {
				cp[k] = v
			}
			cp["content"] = fmt.Sprintf("<%d bytes base64>", len(fmt.Sprint(b["content"])))
			reqLog = cp
		}
		c.env.Log(ctx, Call{Operation: op, Method: method, URL: u.Path, Request: reqLog, Response: logged, StatusCode: code, Err: err, Duration: time.Since(start)})
	}
	return err
}

type pjapInvoice struct {
	ID     string `json:"id"`
	Number string `json:"taxInvoiceNumber"`
	Status string `json:"status"`
}

func pjapStatus(s string) string {
	switch strings.ToLower(s) {
	case "approved", "approval_success", "uploaded":
		return "approved"
	case "cancelled", "canceled":
		return "cancelled"
	case "rejected", "failed":
		return "rejected"
	}
	return "pending"
}

func (c *coretax) SubmitInvoice(ctx context.Context, inv TaxInvoice) (TaxInvoiceResult, error) {
	var out pjapInvoice
	err := c.call(ctx, "submit_invoice", http.MethodPost, "/v1/tax-invoices", map[string]any{
		"sellerNpwp": c.env.Credentials["npwp"], "nitku": setting(c.env, "nitku", ""), "reference": inv.Reference,
		"buyerNpwp": inv.BuyerNPWP, "buyerName": inv.BuyerName, "totalAmount": inv.TotalAmount, "vatAmount": inv.TaxAmount,
		"issueDate": inv.IssuedAt.Format("2006-01-02"),
	}, &out)
	return TaxInvoiceResult{ExternalID: out.ID, Number: out.Number, Status: pjapStatus(out.Status)}, err
}

func (c *coretax) InvoiceStatus(ctx context.Context, externalID string) (TaxInvoiceResult, error) {
	var out pjapInvoice
	err := c.call(ctx, "invoice_status", http.MethodGet, "/v1/tax-invoices/"+url.PathEscape(externalID), nil, &out)
	return TaxInvoiceResult{ExternalID: out.ID, Number: out.Number, Status: pjapStatus(out.Status)}, err
}

func (c *coretax) CancelInvoice(ctx context.Context, externalID, reason string) (TaxInvoiceResult, error) {
	var out pjapInvoice
	err := c.call(ctx, "cancel_invoice", http.MethodPost, "/v1/tax-invoices/"+url.PathEscape(externalID)+"/cancel", map[string]any{"reason": reason}, &out)
	return TaxInvoiceResult{ExternalID: out.ID, Number: out.Number, Status: pjapStatus(out.Status)}, err
}

type pjapBatch struct {
	ID       string        `json:"id"`
	Status   string        `json:"status"`
	Accepted int           `json:"accepted"`
	Rejected int           `json:"rejected"`
	Messages []string      `json:"messages"`
	Invoices []pjapInvoice `json:"invoices"`
}

func (b pjapBatch) result() TaxBatchResult {
	out := TaxBatchResult{BatchID: b.ID, Status: strings.ToLower(b.Status), Accepted: b.Accepted, Rejected: b.Rejected, Messages: b.Messages,
		Invoices: []TaxInvoiceResult{}}
	if out.Messages == nil {
		out.Messages = []string{}
	}
	for _, i := range b.Invoices {
		out.Invoices = append(out.Invoices, TaxInvoiceResult{ExternalID: i.ID, Number: i.Number, Status: pjapStatus(i.Status)})
	}
	return out
}

func (c *coretax) UploadBatch(ctx context.Context, b TaxInvoiceBatch) (TaxBatchResult, error) {
	if len(b.Content) == 0 {
		return TaxBatchResult{}, errors.New("coretax: the export file is empty")
	}
	var out pjapBatch
	err := c.call(ctx, "upload_batch", http.MethodPost, "/v1/tax-invoice-batches", map[string]any{
		"sellerNpwp": c.env.Credentials["npwp"], "reference": b.BatchRef, "period": b.Period, "format": b.Format, "filename": b.Filename,
		"invoiceCount": b.InvoiceCount, "content": base64.StdEncoding.EncodeToString(b.Content),
	}, &out)
	return out.result(), err
}

func (c *coretax) BatchStatus(ctx context.Context, batchID string) (TaxBatchResult, error) {
	var out pjapBatch
	err := c.call(ctx, "batch_status", http.MethodGet, "/v1/tax-invoice-batches/"+url.PathEscape(batchID), nil, &out)
	return out.result(), err
}

func (c *coretax) TestConnection(ctx context.Context) (string, error) {
	var out map[string]any
	if err := c.call(ctx, "test_connection", http.MethodGet, "/v1/ping", nil, &out); err != nil {
		return "", err
	}
	return "PJAP API reachable (" + c.env.Mode + ")", nil
}

// ── sandbox (mock-efaktur) ────────────────────────────────────────────────

var (
	mockTaxMu      sync.Mutex
	mockTaxBatches = map[string]TaxBatchResult{}
)

func (m *mockTaxInvoice) InvoiceStatus(ctx context.Context, externalID string) (TaxInvoiceResult, error) {
	out := TaxInvoiceResult{ExternalID: externalID, Number: "010.000-26." + strings.ToUpper(externalID[:min(8, len(externalID))]), Status: "approved"}
	return out, timed(m.env, ctx, "invoice_status", map[string]any{"externalId": externalID}, func() (any, int, error) { return out, 200, nil })
}

func (m *mockTaxInvoice) CancelInvoice(ctx context.Context, externalID, reason string) (TaxInvoiceResult, error) {
	out := TaxInvoiceResult{ExternalID: externalID, Status: "cancelled"}
	return out, timed(m.env, ctx, "cancel_invoice", map[string]any{"externalId": externalID, "reason": reason}, func() (any, int, error) {
		if strings.TrimSpace(reason) == "" {
			return nil, 422, errors.New("a cancellation reason is required")
		}
		return out, 200, nil
	})
}

func (m *mockTaxInvoice) UploadBatch(ctx context.Context, b TaxInvoiceBatch) (TaxBatchResult, error) {
	out := TaxBatchResult{BatchID: "mockbatch_" + id.New().String(), Status: "completed", Messages: []string{}, Invoices: []TaxInvoiceResult{}}
	err := timed(m.env, ctx, "upload_batch", map[string]any{"reference": b.BatchRef, "period": b.Period, "format": b.Format, "bytes": len(b.Content)},
		func() (any, int, error) {
			if len(b.Content) == 0 {
				return nil, 422, errors.New("the export file is empty")
			}
			if b.Format == "coretax_xml" && !bytes.Contains(b.Content, []byte("<")) {
				return nil, 422, errors.New("the Coretax export must be XML")
			}
			out.Accepted = b.InvoiceCount
			for i := 0; i < b.InvoiceCount; i++ {
				out.Invoices = append(out.Invoices, TaxInvoiceResult{ExternalID: fmt.Sprintf("%s-%d", out.BatchID, i+1),
					Number: fmt.Sprintf("010.000-26.%08d", time.Now().Unix()%1e8+int64(i)), Status: "approved"})
			}
			mockTaxMu.Lock()
			mockTaxBatches[out.BatchID] = out
			mockTaxMu.Unlock()
			return out, 201, nil
		})
	return out, err
}

func (m *mockTaxInvoice) BatchStatus(ctx context.Context, batchID string) (TaxBatchResult, error) {
	mockTaxMu.Lock()
	out, ok := mockTaxBatches[batchID]
	mockTaxMu.Unlock()
	return out, timed(m.env, ctx, "batch_status", map[string]any{"batchId": batchID}, func() (any, int, error) {
		if !ok {
			return nil, 404, errors.New("batch not found")
		}
		return out, 200, nil
	})
}
