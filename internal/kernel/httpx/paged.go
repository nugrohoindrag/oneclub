package httpx

// Server-side paging of every list route. A client asking for a page sends
// ?limit=<page size> and the nextCursor it received. List handlers either
// page themselves (keyset or offset cursors; they read ListParams.PageSize)
// or simply query the first ListParams.Limit rows: for those ParseList widens
// Limit to offset + lookAhead pages + 1 and Paged cuts the response to the
// page, so the client only ever receives one page while the rows read ahead
// give the pager its total ("11–20 of 47", or "of 50+" beyond the look-ahead).
// Requests without ?limit keep the old behaviour.

import (
	"bytes"
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
)

// genericPrefix marks the offset cursors issued by Paged.
const genericPrefix = "o:"

// lookAhead is how many pages past the offset a widened list reads.
const lookAhead = 5

// GenericOffset returns the offset of a cursor issued by Paged.
func GenericOffset(cursor string) (int, bool) {
	if cursor == "" {
		return 0, true
	}
	raw, err := DecodeCursor(cursor)
	if err != nil || !strings.HasPrefix(raw, genericPrefix) {
		return 0, false
	}
	n, err := strconv.Atoi(strings.TrimPrefix(raw, genericPrefix))
	if err != nil || n < 0 {
		return 0, false
	}
	return n, true
}

type pagedRecorder struct {
	header http.Header
	status int
	body   bytes.Buffer
}

func (p *pagedRecorder) Header() http.Header         { return p.header }
func (p *pagedRecorder) Write(b []byte) (int, error) { return p.body.Write(b) }
func (p *pagedRecorder) WriteHeader(code int) {
	if p.status == 0 {
		p.status = code
	}
}

// PagedEnvelope is a list response cut to one page.
type pagedEnvelope struct {
	Items      []json.RawMessage `json:"items"`
	NextCursor string            `json:"nextCursor,omitempty"`
	Total      *int              `json:"total,omitempty"`
	// TotalAtLeast is a lower bound when the rows go on past the look-ahead.
	TotalAtLeast *int `json:"totalAtLeast,omitempty"`
}

// Paged cuts the response of a list route to the requested page (see the
// package comment). Handlers that already returned a nextCursor page
// themselves and are passed through.
func Paged(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		size, err := strconv.Atoi(q.Get("limit"))
		offset, generic := GenericOffset(q.Get("cursor"))
		if err != nil || size <= 0 || !generic {
			next.ServeHTTP(w, r)
			return
		}
		size = min(size, maxPage)
		rec := &pagedRecorder{header: http.Header{}}
		next.ServeHTTP(rec, r)
		for k, v := range rec.header {
			w.Header()[k] = v
		}
		status := rec.status
		if status == 0 {
			status = http.StatusOK
		}
		var env pagedEnvelope
		if status != http.StatusOK || !strings.HasPrefix(rec.header.Get("Content-Type"), "application/json") ||
			json.Unmarshal(rec.body.Bytes(), &env) != nil || env.Items == nil || env.NextCursor != "" {
			w.WriteHeader(status)
			_, _ = w.Write(rec.body.Bytes())
			return
		}
		all := len(env.Items)
		from, to := min(offset, all), min(offset+size, all)
		out := pagedEnvelope{Items: env.Items[from:to]}
		if all > offset+size {
			out.NextCursor = EncodeCursor(genericPrefix + strconv.Itoa(offset+size))
		}
		// Fewer rows than the widened limit (ParseList): the handler returned
		// every row and the count is exact; otherwise at least the rows read
		// (without the sentinel row of the look-ahead).
		if ahead := offset + lookAhead*size; all <= ahead {
			out.Total = &all
		} else {
			n := all
			if all == ahead+1 {
				n = ahead
			}
			out.TotalAtLeast = &n
		}
		b, err := json.Marshal(out)
		if err != nil {
			w.WriteHeader(status)
			_, _ = w.Write(rec.body.Bytes())
			return
		}
		w.Header().Del("Content-Length")
		w.WriteHeader(status)
		_, _ = w.Write(b)
	})
}

// maxPage is the largest page a client may ask for.
const maxPage = 500
