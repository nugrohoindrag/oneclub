package integration

// HTTP plumbing of the P4 vendor adapters (PRD P4 FR-INT-P4-08): every
// outbound call is logged through the adapter's CallLogger with the
// operation, method, URL, a masked request, a masked response, the HTTP
// status, the duration and the error. Headers (API keys, signatures,
// tokens) are never logged; adapters pass the request / response they
// want logged, already reduced when it carries personal data.

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"oneclub/internal/kernel/mask"
)

// p4Call sends body (already encoded) and decodes a JSON answer into out.
// logReq is what the integration log keeps of the request (nil: the
// decoded body); logResp reduces the decoded response before logging (nil:
// the whole response, masked by Service.LogCall).
func p4Call(ctx context.Context, env Env, op, method, u string, hdr http.Header, body []byte, logReq any, logResp func(any) any, out any) (int, error) {
	start := time.Now()
	var rd io.Reader
	if body != nil {
		rd = bytes.NewReader(body)
	}
	req, err := http.NewRequestWithContext(ctx, method, u, rd)
	if err != nil {
		return 0, err
	}
	for k, v := range hdr {
		req.Header[k] = v
	}
	if body != nil && req.Header.Get("Content-Type") == "" {
		req.Header.Set("Content-Type", "application/json")
	}
	req.Header.Set("Accept", "application/json")
	resp, err := HTTPClient.Do(req) //nolint:gosec // G704: vendor base URL is operator configuration (Settings → Integrations)
	code := 0
	var respBody []byte
	if err == nil {
		code = resp.StatusCode
		respBody, _ = io.ReadAll(io.LimitReader(resp.Body, 1<<20))
		resp.Body.Close()
		switch {
		case code >= 300:
			err = fmt.Errorf("%s: HTTP %d: %s", op, code, truncate(strings.TrimSpace(string(respBody)), 300))
		case out != nil && len(respBody) > 0:
			if jerr := json.Unmarshal(respBody, out); jerr != nil {
				err = fmt.Errorf("%s: invalid JSON response: %w", op, jerr)
			}
		}
	}
	if env.Log != nil {
		var logged any
		_ = json.Unmarshal(respBody, &logged)
		if logResp != nil && logged != nil {
			logged = logResp(logged)
		}
		if logReq == nil && body != nil {
			var decoded any
			if json.Unmarshal(body, &decoded) == nil {
				logReq = decoded
			}
		}
		env.Log(ctx, Call{Operation: op, Method: method, URL: redactURL(u), Request: logReq, Response: logged, StatusCode: code, Err: err,
			Duration: time.Since(start)})
	}
	return code, err
}

// searchKeys are query parameters carrying what a person typed (names,
// numbers): personal data, never logged.
var searchKeys = map[string]bool{"q": true, "query": true, "search": true, "name": true}

// redactURL drops the query string values of secret parameters (api keys,
// tokens) and of search terms from a logged URL.
func redactURL(u string) string {
	base, query, ok := strings.Cut(u, "?")
	if !ok {
		return u
	}
	parts := strings.Split(query, "&")
	for i, p := range parts {
		k, _, _ := strings.Cut(p, "=")
		if mask.IsSecretKey(k) || mask.PersonalKind(k) != "" || searchKeys[strings.ToLower(k)] {
			parts[i] = k + "=" + mask.Redacted
		}
	}
	return base + "?" + strings.Join(parts, "&")
}

// countOnly logs only the number of items of a list response (personal
// data such as resident names never reaches the integration log).
func countOnly(key string) func(any) any {
	return func(v any) any {
		switch t := v.(type) {
		case map[string]any:
			if list, ok := t[key].([]any); ok {
				return map[string]any{"count": len(list)}
			}
			return map[string]any{"keys": len(t)}
		case []any:
			return map[string]any{"count": len(t)}
		}
		return nil
	}
}
