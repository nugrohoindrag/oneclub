package integration

// PRD P3 §16 #18: quotations / contracts above the Sales Policies threshold
// (Rp5 jt) carry an e-Meterai (electronic stamp duty). The integration layer
// provides the capability "e_meterai": stamp a document (the PDF) and read
// the status of a serial. Until a PERURI distributor is contracted the trial
// runs on the sandbox adapter "mock-emeterai", which issues serials of the
// form MOCK-EMT-<yyyymmdd>-<8 hex> and logs every call (masked) like the
// other adapters. Certified e-signatures (PSrE) stay deferred.

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"time"

	"oneclub/internal/kernel/id"
)

// CapEMeterai is the e-Meterai capability.
const CapEMeterai = "e_meterai"

// EMeteraiStampRequest is a document to stamp.
type EMeteraiStampRequest struct {
	DocumentRef    string // OneClub id of the document (e.g. the quotation id)
	DocumentType   string // e.g. "quotation"
	DocumentNumber string // e.g. QUO-2026-00012 v2
	Amount         string // document value (decimal string)
	Currency       string
	Signer         string // name of the signing party
	PDF            []byte // document to stamp
}

// EMeteraiStamp is a stamp issued by the provider.
type EMeteraiStamp struct {
	SerialNumber string    `json:"serialNumber"`
	StampedAt    time.Time `json:"stampedAt"`
	Reference    string    `json:"reference"`
	Status       string    `json:"status" enum:"stamped,pending,failed"`
	Sandbox      bool      `json:"sandbox" doc:"Issued by a sandbox (mock / trial) adapter"`
}

// EMeteraiService is what modules call to stamp documents.
type EMeteraiService interface {
	Stamp(ctx context.Context, r EMeteraiStampRequest) (EMeteraiStamp, error)
	Status(ctx context.Context, serial string) (EMeteraiStamp, error)
}

var _ EMeteraiService = (*mockEMeterai)(nil)

func init() {
	RegisterAdapter(AdapterInfo{
		Key: "mock-emeterai", Capability: CapEMeterai, Name: "Mock e-Meterai", Sandbox: true,
		Description: "Sandbox e-Meterai (stamp duty) for quotations and contracts above the threshold until a PERURI distributor is " +
			"contracted. Serials are marked MOCK and have no legal value.",
		Settings: []Field{{Key: "alwaysFail", Label: "Always fail (testing)", Type: "boolean"}},
		New:      func(env Env) (any, error) { return &mockEMeterai{env: env}, nil },
	})
}

// EMeterai resolves the enabled e-Meterai integration (ErrNotConfigured:
// the document stays pending until an integration is enabled).
func (s *Service) EMeterai(ctx context.Context) (EMeteraiService, string, error) {
	a, code, err := s.Resolve(ctx, CapEMeterai)
	if err != nil {
		return nil, "", err
	}
	e, ok := a.(EMeteraiService)
	if !ok {
		return nil, "", fmt.Errorf("integration %s does not implement e-Meterai", code)
	}
	return e, code, nil
}

// ── sandbox (mock-emeterai) ───────────────────────────────────────────────

type mockEMeterai struct{ env Env }

// MockEMeteraiPrefix starts every serial of the sandbox.
const MockEMeteraiPrefix = "MOCK-EMT-"

func (m *mockEMeterai) Stamp(ctx context.Context, r EMeteraiStampRequest) (EMeteraiStamp, error) {
	now := time.Now().UTC()
	sum := sha256.Sum256([]byte(r.DocumentRef + "|" + id.New().String()))
	out := EMeteraiStamp{SerialNumber: MockEMeteraiPrefix + now.Format("20060102") + "-" + strings.ToUpper(hex.EncodeToString(sum[:4])),
		StampedAt: now, Reference: "mockemt_" + id.New().String(), Status: "stamped", Sandbox: true}
	err := timed(m.env, ctx, "stamp", map[string]any{"documentRef": r.DocumentRef, "documentType": r.DocumentType, "documentNumber": r.DocumentNumber,
		"amount": r.Amount, "currency": r.Currency, "signer": r.Signer, "pdfBytes": len(r.PDF)}, func() (any, int, error) {
		if v, _ := m.env.Settings["alwaysFail"].(bool); v {
			return nil, 503, errors.New("simulated e-Meterai provider failure")
		}
		if r.DocumentRef == "" || len(r.PDF) == 0 {
			return nil, 422, errors.New("document reference and PDF are required")
		}
		return out, 201, nil
	})
	if err != nil {
		return EMeteraiStamp{Status: "failed", Sandbox: true}, err
	}
	return out, nil
}

func (m *mockEMeterai) Status(ctx context.Context, serial string) (EMeteraiStamp, error) {
	out := EMeteraiStamp{SerialNumber: serial, Status: "stamped", Sandbox: true}
	return out, timed(m.env, ctx, "status", map[string]any{"serialNumber": serial}, func() (any, int, error) {
		if !strings.HasPrefix(serial, MockEMeteraiPrefix) {
			return nil, 404, errors.New("serial not found")
		}
		return out, 200, nil
	})
}

func (m *mockEMeterai) TestConnection(ctx context.Context) (string, error) {
	return "Mock e-Meterai reachable", timed(m.env, ctx, "test_connection", nil, func() (any, int, error) { return map[string]any{"ok": true}, 200, nil })
}
