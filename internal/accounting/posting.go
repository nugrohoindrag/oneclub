package accounting

// EP-17 automatic posting framework (Technical Doc §4.3): one outbox
// subscriber per consumed event type. Each event is processed once
// (accounting.processed_events on top of the outbox marker, FR-PST-05),
// serialised per property, skipped before the property's book / cut-over,
// and posted through the posting rules. What has no rule goes to the
// suspense account and the posting exception queue (FR-PST-04); a fixed
// exception is reposted: the suspense journal is reversed and the stored
// event processed again with the rules in force.

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/shopspring/decimal"

	"oneclub/internal/kernel/dbtx"
	"oneclub/internal/kernel/errs"
	"oneclub/internal/kernel/id"
	"oneclub/internal/platform/outbox"
)

// Ev is a consumed event.
type Ev struct {
	ID         uuid.UUID
	Type       string
	Property   uuid.UUID
	OccurredAt time.Time
	Payload    json.RawMessage
}

func (e Ev) decode(v any) error { return json.Unmarshal(e.Payload, v) }

// outcome of an event handler.
type outcome struct {
	Status   string // posted | no_posting | skipped
	Journals []uuid.UUID
	Note     string
	Missing  []Missing
}

func (o *outcome) add(j *AccountingJournal, missing []Missing) {
	if j != nil {
		o.Journals = append(o.Journals, j.ID)
		o.Status = "posted"
	}
	o.Missing = append(o.Missing, missing...)
}

type evHandler func(ctx context.Context, tx pgx.Tx, ev Ev) (outcome, error)

// invalidPayload marks a payload that cannot be posted.
type invalidPayload struct{ msg string }

func (e invalidPayload) Error() string { return e.msg }

func badPayload(format string, a ...any) error {
	return invalidPayload{msg: errf(format, a...).Error()}
}

// Handler adapts an event handler to the outbox (subscriber per event type).
func (m *Module) handler(h evHandler) outbox.Handler {
	return func(ctx context.Context, tx pgx.Tx, e outbox.Event) error {
		if e.PropertyID == nil {
			return nil
		}
		ev := Ev{ID: e.ID, Type: e.Type, Property: *e.PropertyID, OccurredAt: e.OccurredAt, Payload: e.Payload}
		return m.process(ctx, tx, ev, false)
	}
}

// process runs one event (or re-runs it for a repost).
func (m *Module) process(ctx context.Context, tx pgx.Tx, ev Ev, repost bool) error {
	h, ok := m.handlers()[ev.Type]
	if !ok {
		return nil
	}
	if err := lockProperty(ctx, tx, ev.Property); err != nil {
		return err
	}
	if !repost {
		var done bool
		if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM accounting.processed_events WHERE event_id = $1)`, ev.ID).Scan(&done); err != nil || done {
			return err
		}
	}
	if _, err := GetBook(ctx, tx, ev.Property); err != nil {
		if e, ok := errs.As(err); ok && e.Kind == errs.KindNotFound {
			return m.recordEvent(ctx, tx, ev, outcome{Status: "skipped", Note: "accounting is not set up for the property"}, "")
		}
		return err
	}
	// Documents dated before the cut-over date are covered by the opening
	// balances (EP-23): postSources leaves them out.
	sp, err := tx.Begin(ctx)
	if err != nil {
		return err
	}
	out, herr := h(ctx, sp, ev)
	reason := ""
	if herr != nil {
		_ = sp.Rollback(ctx)
		var ip invalidPayload
		switch {
		case errors.As(herr, &ip):
			reason = "invalid_payload"
		case errors.Is(herr, ErrPeriodClosed):
			reason = "closed_period"
		default:
			if e, ok := errs.As(herr); ok && (e.Kind == errs.KindValidation || e.Kind == errs.KindConflict || e.Kind == errs.KindNotFound) {
				reason = "unbalanced"
				if e.Code == "invalid_account" || e.Code == "unknown_account" {
					reason = "missing_account"
				}
			} else if dbtx.IsRetryable(herr) || ctx.Err() != nil {
				return herr // deadlock / serialization: the outbox retries
			} else {
				reason = "processing_error" // kept in the exception queue, reposted after the fix
			}
		}
		out = outcome{Status: "exception", Note: herr.Error()}
	} else if err := sp.Commit(ctx); err != nil {
		return err
	}
	if out.Status == "" {
		out.Status = "no_posting"
	}
	if len(out.Missing) > 0 && reason == "" {
		reason = out.Missing[0].Reason
		if out.Status != "posted" {
			out.Status = "exception"
		}
	}
	return m.recordEvent(ctx, tx, ev, out, reason)
}

// recordEvent stores the processed event and, when something could not be
// posted, the posting exception.
func (m *Module) recordEvent(ctx context.Context, tx pgx.Tx, ev Ev, out outcome, reason string) error {
	payload := ev.Payload
	if len(payload) == 0 {
		payload = json.RawMessage(`{}`)
	}
	journals := out.Journals
	if journals == nil {
		journals = []uuid.UUID{}
	}
	if _, err := tx.Exec(ctx, `INSERT INTO accounting.processed_events (event_id, property_id, event_type, occurred_at, payload, status, journal_ids, note)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8) ON CONFLICT (event_id) DO UPDATE SET status = EXCLUDED.status, journal_ids = EXCLUDED.journal_ids,
		note = EXCLUDED.note, attempts = accounting.processed_events.attempts + 1`,
		ev.ID, ev.Property, ev.Type, ev.OccurredAt, payload, out.Status, journals, nz(out.Note)); err != nil {
		return err
	}
	if reason == "" {
		return nil
	}
	msg := out.Note
	amount := decimal.Zero
	for _, x := range out.Missing {
		amount = amount.Add(dec(x.Amount))
		if msg == "" {
			msg = x.Detail
		}
	}
	if msg == "" {
		msg = reason
	}
	details, _ := json.Marshal(map[string]any{"missing": out.Missing})
	var jid *uuid.UUID
	if len(out.Journals) > 0 {
		jid = &out.Journals[0]
	}
	eid := id.New()
	if _, err := tx.Exec(ctx, `INSERT INTO accounting.posting_exceptions (id, property_id, event_id, event_type, reason, message, details, amount, journal_id, attempts)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8::numeric,$9,1)`, eid, ev.Property, ev.ID, ev.Type, reason, msg, details, amount.String(), jid); err != nil {
		return err
	}
	if m.Events != nil {
		p := ev.Property
		if _, err := m.Events.Publish(ctx, tx, EventPostingException, "accounting.posting_exception", &eid, &p, map[string]any{"exceptionId": eid,
			"eventId": ev.ID, "eventType": ev.Type, "reason": reason, "message": msg, "amount": amount.String(), "journalId": jid}); err != nil {
			return err
		}
	}
	return nil
}

// ── posting of items with source markers ──────────────────────────────────

// sourceMark is a source document posted by a journal.
type sourceMark struct {
	Type         string
	ID           uuid.UUID
	Part         int
	BusinessDate *time.Time
	Key1, Key2   string
	Amount       decimal.Decimal
	Items        []Item
}

// posting is one journal built from source documents.
type posting struct {
	Entry   Entry
	Sources []sourceMark
	Items   []Item // items without a source marker
}

// postSources resolves the items of every source (a source whose items
// cannot all be mapped is held back in hold mode), posts one journal and
// marks the sources. Returns nil when nothing had a ledger effect.
func (m *Module) postSources(ctx context.Context, tx pgx.Tx, p posting) (*AccountingJournal, []Missing, error) {
	if cut := m.cutOver(ctx, tx, p.Entry.Property); !cut.IsZero() && dateOnly(p.Entry.Date).Before(cut) {
		return nil, nil, nil // before the cut-over: in the opening balances (EP-23)
	}
	r, err := newResolver(ctx, tx, p.Entry.Property, p.Entry.Date)
	if err != nil {
		return nil, nil, err
	}
	var lines []Line
	var posted []sourceMark
	for _, s := range p.Sources {
		var done bool
		if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM accounting.posted_sources WHERE source_type = $1 AND source_id = $2 AND part = $3)`,
			s.Type, s.ID, s.Part).Scan(&done); err != nil {
			return nil, nil, err
		}
		if done {
			continue // posted before (another event or a sweep): never twice, FR-PST-05
		}
		r.held = map[int]bool{}
		ls, err := r.resolve(s.Items)
		if err != nil {
			return nil, nil, err
		}
		if len(r.held) > 0 {
			continue
		}
		lines = append(lines, ls...)
		posted = append(posted, s)
	}
	if len(p.Items) > 0 {
		r.held = map[int]bool{}
		ls, err := r.resolve(p.Items)
		if err != nil {
			return nil, nil, err
		}
		if len(r.held) == 0 {
			lines = append(lines, ls...)
		}
	}
	e := p.Entry
	e.Lines, e.Merge = lines, true
	if e.Type == "" {
		e.Type = "automatic"
	}
	var j *AccountingJournal
	if len(mergeLines(lines)) >= 2 {
		cfg := r.cfg
		jj, err := m.post(ctx, tx, e, cfg.ClosedPeriodPosting != "exception")
		if err != nil {
			return nil, r.missing, err
		}
		j = &jj
	}
	for _, s := range posted {
		var jid *uuid.UUID
		if j != nil {
			jid = &j.ID
		}
		if _, err := tx.Exec(ctx, `INSERT INTO accounting.posted_sources (property_id, source_type, source_id, part, journal_id, business_date, key1, key2, amount)
			VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9::numeric) ON CONFLICT (source_type, source_id, part) DO NOTHING`, e.Property, s.Type, s.ID, s.Part, jid,
			s.BusinessDate, nz(s.Key1), nz(s.Key2), s.Amount.String()); err != nil {
			return nil, nil, err
		}
	}
	return j, r.missing, nil
}

// isPosted reports whether a source document is already posted.
func isPosted(ctx context.Context, q dbtx.Querier, sourceType string, sid uuid.UUID) (bool, error) {
	var ok bool
	err := q.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM accounting.posted_sources WHERE source_type = $1 AND source_id = $2)`, sourceType, sid).Scan(&ok)
	return ok, err
}

// repostException reverses the suspense journal of an exception, releases
// its sources and processes the stored event again.
func (m *Module) repostException(ctx context.Context, tx pgx.Tx, exID uuid.UUID) (AccountingPostingException, error) {
	x, err := getException(ctx, tx, exID)
	if err != nil {
		return x, err
	}
	if x.Status != "open" {
		return x, errs.Conflict("exception_closed", "the exception is already "+x.Status)
	}
	if err := lockProperty(ctx, tx, x.PropertyID); err != nil {
		return x, err
	}
	if x.EventID == nil {
		return x, errs.Conflict("no_event", "the exception has no stored event to repost")
	}
	var ev Ev
	ev.ID, ev.Property = *x.EventID, x.PropertyID
	if err := tx.QueryRow(ctx, `SELECT event_type, coalesce(occurred_at, processed_at), payload, journal_ids FROM accounting.processed_events WHERE event_id = $1`,
		ev.ID).Scan(&ev.Type, &ev.OccurredAt, &ev.Payload, new([]uuid.UUID)); err != nil {
		if dbtx.IsNoRows(err) {
			return x, errs.Conflict("no_event", "the stored event is missing")
		}
		return x, err
	}
	var journals []uuid.UUID
	_ = tx.QueryRow(ctx, `SELECT journal_ids FROM accounting.processed_events WHERE event_id = $1`, ev.ID).Scan(&journals)
	for _, jid := range journals {
		var reversed bool
		if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM accounting.journals WHERE reverses_journal_id = $1)`, jid).Scan(&reversed); err != nil {
			return x, err
		}
		if !reversed {
			if _, err := m.reverse(ctx, tx, jid, nil, "repost of posting exception", true); err != nil {
				return x, err
			}
		}
		if _, err := tx.Exec(ctx, `DELETE FROM accounting.posted_sources WHERE journal_id = $1`, jid); err != nil {
			return x, err
		}
	}
	// held items of the event have no journal: their sources stay unposted
	before := map[uuid.UUID]bool{}
	rows, err := tx.Query(ctx, `SELECT id FROM accounting.posting_exceptions WHERE event_id = $1`, ev.ID)
	if err != nil {
		return x, err
	}
	for rows.Next() {
		var eid uuid.UUID
		if err := rows.Scan(&eid); err != nil {
			rows.Close()
			return x, err
		}
		before[eid] = true
	}
	rows.Close()
	if err := m.process(ctx, tx, ev, true); err != nil {
		return x, err
	}
	var newJournals []uuid.UUID
	if err := tx.QueryRow(ctx, `SELECT journal_ids FROM accounting.processed_events WHERE event_id = $1`, ev.ID).Scan(&newJournals); err != nil {
		return x, err
	}
	// a repost that raised a new exception keeps this one open (the new
	// exception carries the remaining problem); otherwise it is resolved.
	var still bool
	if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM accounting.posting_exceptions WHERE event_id = $1 AND status = 'open' AND NOT (id = ANY($2)))`,
		ev.ID, keys(before)).Scan(&still); err != nil {
		return x, err
	}
	status := "resolved"
	note := "reposted"
	if still {
		note = "reposted; a new exception remains"
	}
	if newJournals == nil {
		newJournals = []uuid.UUID{}
	}
	if _, err := tx.Exec(ctx, `UPDATE accounting.posting_exceptions SET status = $2, attempts = attempts + 1, resolved_at = now(), resolved_by = $3,
		resolution_note = $4, resolution_journal_ids = $5 WHERE id = $1`, exID, status, actorPtr(ctx), note, newJournals); err != nil {
		return x, err
	}
	return getException(ctx, tx, exID)
}

func keys(m map[uuid.UUID]bool) []uuid.UUID {
	out := make([]uuid.UUID, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}
