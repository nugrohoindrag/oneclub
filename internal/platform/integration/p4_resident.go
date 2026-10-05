package integration

// Modernland residence verification in production (PRD P4 FR-INT-P4-07,
// Should; Technical Doc §8.2 "Data residen Modernland: adapter pull/sync").
// The estate's resident registry is read through a REST lookup:
//
//	GET <baseUrl><lookupPath>?q=<name, unit or resident number>&limit=20
//	X-Api-Key: <apiKey>
//	→ 200 {"items": [{"id": "RES-0001", "name": "…", "address": "…", "unit": "A-1", "status": "active"}]}
//
// Membership (resident-only types, residence rates) calls LookupResident
// through the resident_data capability; "mock-resident" stays the sandbox.
// Names and addresses are personal data: the integration log keeps only the
// number of matches, never the residents (UU PDP).

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"strings"
)

func init() {
	RegisterAdapter(AdapterInfo{
		Key: "modernland-resident", Capability: CapResidentData, Name: "Modernland Resident Registry",
		Description: "Residence verification against the Modernland resident registry (REST lookup with an API key). " +
			"Only the number of matches is written to the integration log.",
		Credentials: []Field{{Key: "apiKey", Label: "API key", Type: "secret", Required: true}},
		Settings: []Field{
			{Key: "baseUrl", Label: "Registry API base URL", Type: "url", Required: true},
			{Key: "lookupPath", Label: "Lookup path", Type: "string", Help: "Default /residents"},
		},
		New: func(env Env) (any, error) { return newModernland(env) },
	})
}

type modernland struct {
	env  Env
	base string
	path string
}

func newModernland(env Env) (*modernland, error) {
	base := strings.TrimRight(setting(env, "baseUrl", ""), "/")
	if u, err := url.Parse(base); base == "" || err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return nil, errors.New("modernland: registry API base URL must be an http(s) address")
	}
	if env.Credentials["apiKey"] == "" {
		return nil, errors.New("modernland: API key is required")
	}
	p := setting(env, "lookupPath", "/residents")
	if !strings.HasPrefix(p, "/") {
		p = "/" + p
	}
	return &modernland{env: env, base: base, path: p}, nil
}

type modernlandResident struct {
	ID      string `json:"id"`
	Name    string `json:"name"`
	Address string `json:"address"`
	Unit    string `json:"unit"`
	Status  string `json:"status"`
}

func residentStatus(s string) string {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "active", "aktif", "resident":
		return "active"
	case "moved_out", "pindah", "inactive", "nonaktif":
		return "inactive"
	}
	return "unknown"
}

func (m *modernland) lookup(ctx context.Context, op, q string, limit string) ([]Resident, error) {
	hdr := http.Header{"X-Api-Key": {m.env.Credentials["apiKey"]}}
	u := m.base + m.path + "?" + url.Values{"q": {q}, "limit": {limit}}.Encode()
	var out struct {
		Items []modernlandResident `json:"items"`
	}
	// The query is personal data too (a name): only its length is logged.
	if _, err := p4Call(ctx, m.env, op, http.MethodGet, u, hdr, nil, map[string]any{"queryLength": len([]rune(q)), "limit": limit},
		countOnly("items"), &out); err != nil {
		return nil, err
	}
	res := make([]Resident, 0, len(out.Items))
	for _, r := range out.Items {
		addr := r.Address
		if r.Unit != "" {
			addr = strings.TrimSpace(addr + " (" + r.Unit + ")")
		}
		res = append(res, Resident{ResidentID: r.ID, Name: r.Name, Address: addr, Status: residentStatus(r.Status)})
	}
	return res, nil
}

// LookupResident searches the registry by name, unit or resident number.
func (m *modernland) LookupResident(ctx context.Context, q string) ([]Resident, error) {
	if strings.TrimSpace(q) == "" {
		return nil, errors.New("modernland: a search term is required")
	}
	return m.lookup(ctx, "lookup_resident", q, "20")
}

func (m *modernland) TestConnection(ctx context.Context) (string, error) {
	if _, err := m.lookup(ctx, "test_connection", "connection-test", "1"); err != nil {
		return "", err
	}
	return "Modernland resident registry reachable", nil
}
