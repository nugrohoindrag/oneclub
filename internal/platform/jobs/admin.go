package jobs

import (
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"
	"github.com/riverqueue/river"
	"github.com/riverqueue/river/rivertype"

	"oneclub/internal/kernel/dbtx"
	"oneclub/internal/kernel/errs"
	"oneclub/internal/kernel/httpx"
	"oneclub/internal/kernel/route"
	"oneclub/internal/platform/audit"
)

// Job is the Background Jobs view of a River job (FR-JOB-04).
type Job struct {
	ID          int64           `json:"id"`
	Kind        string          `json:"kind"`
	Queue       string          `json:"queue"`
	State       string          `json:"state" enum:"available,scheduled,running,retryable,discarded,cancelled,completed,pending"`
	Attempt     int             `json:"attempt"`
	MaxAttempts int             `json:"maxAttempts"`
	Errors      []JobError      `json:"errors"`
	Args        json.RawMessage `json:"args"`
	CreatedAt   time.Time       `json:"createdAt"`
	ScheduledAt time.Time       `json:"scheduledAt"`
	AttemptedAt *time.Time      `json:"attemptedAt"`
	FinalizedAt *time.Time      `json:"finalizedAt"`
}

type JobError struct {
	At      time.Time `json:"at"`
	Attempt int       `json:"attempt"`
	Error   string    `json:"error"`
}

type DiscardRequest struct {
	Reason string `json:"reason"`
}

type RetryRequest struct {
	Reason string `json:"reason,omitempty"`
}

func toJob(r *rivertype.JobRow) Job {
	j := Job{ID: r.ID, Kind: r.Kind, Queue: r.Queue, State: string(r.State), Attempt: r.Attempt, MaxAttempts: r.MaxAttempts,
		Args: json.RawMessage(r.EncodedArgs), CreatedAt: r.CreatedAt, ScheduledAt: r.ScheduledAt, AttemptedAt: r.AttemptedAt,
		FinalizedAt: r.FinalizedAt, Errors: []JobError{}}
	for _, e := range r.Errors {
		j.Errors = append(j.Errors, JobError{At: e.At, Attempt: e.Attempt, Error: e.Error})
	}
	if len(j.Args) == 0 {
		j.Args = json.RawMessage("{}")
	}
	return j
}

// Admin exposes Settings → System Settings → Background Jobs.
type Admin struct {
	Client *Client
	DB     *dbtx.DB
}

func (a *Admin) list(w http.ResponseWriter, r *http.Request) {
	lp := httpx.ParseList(r)
	states := []rivertype.JobState{rivertype.JobStateRetryable, rivertype.JobStateDiscarded}
	if s := lp.Filters["state"]; s != "" {
		states = nil
		for _, st := range strings.Split(s, ",") {
			states = append(states, rivertype.JobState(strings.TrimSpace(st)))
		}
	}
	params := river.NewJobListParams().States(states...).First(min(lp.PageSize, 200)).OrderBy(river.JobListOrderByID, river.SortOrderDesc)
	if k := lp.Filters["kind"]; k != "" {
		params = params.Kinds(strings.Split(k, ",")...)
	}
	if lp.Cursor != "" {
		raw, err := httpx.DecodeCursor(lp.Cursor)
		if err != nil {
			httpx.WriteError(w, r, err)
			return
		}
		var c river.JobListCursor
		if err := c.UnmarshalText([]byte(raw)); err != nil {
			httpx.WriteError(w, r, errs.BadRequest("invalid_cursor", "invalid cursor"))
			return
		}
		params = params.After(&c)
	}
	res, err := a.Client.River.JobList(r.Context(), params)
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	page := httpx.Page[Job]{Items: []Job{}}
	for _, j := range res.Jobs {
		page.Items = append(page.Items, toJob(j))
	}
	if res.LastCursor != nil && len(res.Jobs) == min(lp.PageSize, 200) {
		if b, err := res.LastCursor.MarshalText(); err == nil {
			page.NextCursor = httpx.EncodeCursor(string(b))
		}
	}
	httpx.JSON(w, http.StatusOK, page)
}

func (a *Admin) action(kind string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		jid, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
		if err != nil {
			httpx.WriteError(w, r, errs.NotFound("job"))
			return
		}
		var reason string
		if kind == "discard" {
			var req DiscardRequest
			if err := httpx.Decode(r, &req); err != nil {
				httpx.WriteError(w, r, err)
				return
			}
			if strings.TrimSpace(req.Reason) == "" {
				httpx.WriteError(w, r, errs.Validation("reason_required", "a reason is required to discard a job", errs.Field("reason", "required", "reason is required")))
				return
			}
			reason = req.Reason
		} else {
			var req RetryRequest
			if err := httpx.Decode(r, &req); err != nil {
				httpx.WriteError(w, r, err)
				return
			}
			reason = req.Reason
		}
		ctx := r.Context()
		var out Job
		err = a.DB.WithTx(ctx, func(tx pgx.Tx) error {
			before, err := a.Client.River.JobGetTx(ctx, tx, jid)
			if err != nil {
				if strings.Contains(err.Error(), "not found") {
					return errs.NotFound("job")
				}
				return err
			}
			var row *rivertype.JobRow
			switch kind {
			case "retry":
				if before.State == rivertype.JobStateRunning || before.State == rivertype.JobStateCompleted {
					return errs.Conflict("job_not_retryable", "only failed, retrying or cancelled jobs can be retried")
				}
				row, err = a.Client.River.JobRetryTx(ctx, tx, jid)
			default:
				if before.State == rivertype.JobStateCompleted || before.State == rivertype.JobStateCancelled {
					return errs.Conflict("job_finalized", "job is already finalized")
				}
				row, err = a.Client.River.JobCancelTx(ctx, tx, jid)
			}
			if err != nil {
				return err
			}
			out = toJob(row)
			return audit.Record(ctx, tx, audit.Entry{Module: "platform", Action: "job_" + kind, Category: audit.CategorySystem,
				EntityType: "river.job", EntityID: strconv.FormatInt(jid, 10), EntityLabel: before.Kind, Reason: reason,
				Before: map[string]any{"state": before.State, "attempt": before.Attempt}, After: map[string]any{"state": row.State}})
		})
		if err != nil {
			httpx.WriteError(w, r, err)
			return
		}
		httpx.JSON(w, http.StatusOK, out)
	}
}

// Register adds the Background Jobs routes (PRD §10 Jobs).
func (a *Admin) Register(reg *route.Registry) {
	const tag = "Background Jobs"
	reg.Add(route.Route{Method: http.MethodGet, Path: "/api/v1/platform/jobs", Module: "platform", Tag: tag,
		Summary: "List failed and retrying jobs", Permission: "platform.job.view", Response: Job{}, List: true,
		Query:   []route.Param{{Name: "filter[state]", Description: "Comma separated; default retryable,discarded"}, {Name: "filter[kind]"}},
		Handler: a.list})
	reg.Add(route.Route{Method: http.MethodPost, Path: "/api/v1/platform/jobs/{id}:retry", Module: "platform", Tag: tag,
		Summary: "Retry job", Permission: "platform.job.retry", Request: RetryRequest{}, Response: Job{}, Status: http.StatusOK, Handler: a.action("retry")})
	reg.Add(route.Route{Method: http.MethodPost, Path: "/api/v1/platform/jobs/{id}:discard", Module: "platform", Tag: tag,
		Summary: "Discard job (reason required)", Permission: "platform.job.discard", Request: DiscardRequest{}, Response: Job{}, Status: http.StatusOK, Handler: a.action("discard")})
}
