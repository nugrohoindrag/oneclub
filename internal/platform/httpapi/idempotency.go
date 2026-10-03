package httpapi

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"net/http"
	"strings"

	"oneclub/internal/kernel/authz"
	"oneclub/internal/kernel/errs"
	"oneclub/internal/kernel/httpx"
)

// withIdempotency implements the Idempotency-Key contract (FR-JOB-06,
// Technical Doc §8.1): the first request with a key is executed and its
// response stored; repeats with the same key and payload get the stored
// response; a different payload is rejected; a concurrent repeat gets 409.
func (s *Server) withIdempotency(w *statusWriter, r *http.Request, h http.HandlerFunc) {
	ctx := r.Context()
	key := r.Header.Get("Idempotency-Key")
	if len(key) > 200 {
		httpx.WriteError(w, r, errs.BadRequest("idempotency_key_invalid", "Idempotency-Key too long"))
		return
	}
	body, err := io.ReadAll(io.LimitReader(r.Body, 4<<20))
	if err != nil {
		httpx.WriteError(w, r, errs.BadRequest("body_unreadable", "request body could not be read"))
		return
	}
	r.Body = io.NopCloser(bytes.NewReader(body))
	sum := sha256.Sum256(append([]byte(r.Method+" "+r.URL.Path+"\n"+r.Header.Get("X-Property-Id")+"\n"), body...))
	hash := hex.EncodeToString(sum[:])

	actor := "anonymous"
	if p := authz.From(ctx); p != nil {
		switch p.Kind {
		case authz.ActorAPIKey:
			actor = "key:" + p.APIKeyID.String()
		default:
			actor = "user:" + p.UserID.String()
		}
	}

	tag, err := s.DB.Primary.Exec(ctx, `
		INSERT INTO platform.idempotency_keys (actor_id, key, method, path, request_hash)
		VALUES ($1, $2, $3, $4, $5) ON CONFLICT (actor_id, key) DO NOTHING`,
		actor, key, r.Method, r.URL.Path, hash)
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	if tag.RowsAffected() == 0 {
		var status string
		var storedHash string
		var code *int
		var respBody []byte
		var respType *string
		err := s.DB.Primary.QueryRow(ctx, `
			SELECT status, request_hash, response_code, response_body, response_type
			FROM platform.idempotency_keys WHERE actor_id = $1 AND key = $2`, actor, key).
			Scan(&status, &storedHash, &code, &respBody, &respType)
		if err != nil {
			httpx.WriteError(w, r, err)
			return
		}
		if storedHash != hash {
			httpx.WriteError(w, r, errs.Validation("idempotency_key_reused", "Idempotency-Key was already used with a different request"))
			return
		}
		if status != "completed" || code == nil {
			httpx.WriteError(w, r, errs.Conflict("idempotency_in_progress", "a request with this Idempotency-Key is still being processed"))
			return
		}
		if respType != nil {
			w.Header().Set("Content-Type", *respType)
		}
		w.Header().Set("Idempotent-Replayed", "true")
		w.WriteHeader(*code)
		_, _ = w.Write(respBody)
		return
	}

	w.body = &strings.Builder{}
	h(w, r)
	if w.status >= 500 {
		// allow the client to retry with the same key
		_, _ = s.DB.Primary.Exec(ctx, `DELETE FROM platform.idempotency_keys WHERE actor_id = $1 AND key = $2`, actor, key)
		return
	}
	_, _ = s.DB.Primary.Exec(ctx, `
		UPDATE platform.idempotency_keys SET status = 'completed', response_code = $3, response_body = $4,
		  response_type = $5, completed_at = now() WHERE actor_id = $1 AND key = $2`,
		actor, key, w.status, []byte(w.body.String()), w.Header().Get("Content-Type"))
}
