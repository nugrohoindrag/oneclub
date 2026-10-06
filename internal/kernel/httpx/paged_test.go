package httpx

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"
)

// list serves the first ListParams.Limit of n rows, like the list handlers.
func list(n int) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		lp := ParseList(r)
		items := []int{}
		for i := 0; i < min(n, lp.Limit); i++ {
			items = append(items, i)
		}
		JSON(w, http.StatusOK, Page[int]{Items: items})
	})
}

func TestPagedTotals(t *testing.T) {
	for _, c := range []struct {
		rows, page            int
		total, atLeast, items int
		next                  bool
	}{
		{rows: 7, page: 1, total: 7, items: 7},
		{rows: 47, page: 1, total: 47, items: 10, next: true},
		{rows: 50, page: 1, total: 50, items: 10, next: true},
		{rows: 51, page: 1, atLeast: 50, items: 10, next: true},
		{rows: 300, page: 1, atLeast: 50, items: 10, next: true},
		{rows: 300, page: 3, atLeast: 70, items: 10, next: true},
		{rows: 57, page: 6, total: 57, items: 7},
	} {
		cursor := ""
		if c.page > 1 {
			cursor = "&cursor=" + EncodeCursor(genericPrefix+strconv.Itoa((c.page-1)*10))
		}
		rec := httptest.NewRecorder()
		Paged(list(c.rows)).ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/x?limit=10"+cursor, nil))
		var env pagedEnvelope
		if err := json.Unmarshal(rec.Body.Bytes(), &env); err != nil {
			t.Fatal(err)
		}
		got := func(p *int) int {
			if p == nil {
				return 0
			}
			return *p
		}
		if len(env.Items) != c.items || got(env.Total) != c.total || got(env.TotalAtLeast) != c.atLeast || (env.NextCursor != "") != c.next {
			t.Errorf("%d rows, page %d: items %d total %d atLeast %d next %q", c.rows, c.page, len(env.Items), got(env.Total), got(env.TotalAtLeast), env.NextCursor)
		}
	}
}
