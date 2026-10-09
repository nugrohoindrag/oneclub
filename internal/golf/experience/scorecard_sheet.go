package experience

// Scorecard to print and share (demo feedback 9 Oct 2026; Smartscore
// printed the card on the course): after Complete Round the front desk
// prints the card of each player as a PDF or sends the player a link to
// it — guests without a Member App account too. The card shows the player,
// date, course, tee, the score per hole, the totals, the course handicap,
// the net score and the caddy.

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"oneclub/internal/crm"
	"oneclub/internal/kernel/clock"
	"oneclub/internal/kernel/dbtx"
	"oneclub/internal/kernel/errs"
	"oneclub/internal/kernel/httpx"
	"oneclub/internal/kernel/pdf"
	"oneclub/internal/kernel/route"
	"oneclub/internal/kernel/secret"
	"oneclub/internal/platform/handle"
	"oneclub/internal/platform/notify"
	"oneclub/internal/platform/org"
)

// ScorecardSheet is a scorecard as printed: the card with the club, the
// course, the handicaps, the net score and the caddy.
type ScorecardSheet struct {
	Scorecard
	ClubName       string  `json:"clubName"`
	CourseName     string  `json:"courseName"`
	HandicapIndex  *string `json:"handicapIndex"`
	CourseHandicap *int    `json:"courseHandicap" doc:"WHS course handicap from the tee of the card"`
	Net            *int    `json:"net" doc:"Gross minus the course handicap"`
	CaddyName      *string `json:"caddyName"`
	Email          string  `json:"email,omitempty" doc:"Player contact for sharing"`
	Phone          string  `json:"phone,omitempty"`
	Shared         bool    `json:"shared" doc:"A share link exists"`
}

// ScorecardShareInput shares a card: the link only, or sent by e-mail /
// WhatsApp (default: the player's contact).
type ScorecardShareInput struct {
	Send  bool   `json:"send,omitempty" doc:"Send the link by e-mail / WhatsApp"`
	Email string `json:"email,omitempty"`
	Phone string `json:"phone,omitempty"`
}

// ScorecardShare is the public link of a card.
type ScorecardShare struct {
	Link   string   `json:"link" doc:"Public PDF of the card (no account needed)"`
	SentTo []string `json:"sentTo"`
}

func (m *Module) scorecardSheet(ctx context.Context, q dbtx.Querier, sid uuid.UUID) (ScorecardSheet, error) {
	sc, err := m.GetScorecard(ctx, q, sid)
	if err != nil {
		return ScorecardSheet{}, err
	}
	s := ScorecardSheet{Scorecard: sc}
	if s.Gross == nil {
		// a card not validated yet: the strokes entered so far
		g, entered := 0, false
		for _, h := range sc.Scores {
			if h.Strokes != nil {
				g, entered = g+*h.Strokes, true
			}
		}
		if entered {
			s.Gross = &g
		}
	}
	if s.ClubName, err = org.PropertyName(ctx, q, sc.PropertyID); err != nil {
		return s, err
	}
	if err := q.QueryRow(ctx, `SELECT c.name FROM golf.playing_routes r JOIN golf.courses c ON c.id = r.course_id WHERE r.id = $1`, sc.RouteID).Scan(&s.CourseName); err != nil {
		return s, err
	}
	var guest *uuid.UUID
	var phone *string
	if sc.PlayerID != nil {
		if err := q.QueryRow(ctx, `SELECT trim_scale(handicap_index)::text, guest_id, phone FROM golf.booking_players WHERE id = $1`, *sc.PlayerID).
			Scan(&s.HandicapIndex, &guest, &phone); err != nil && !dbtx.IsNoRows(err) {
			return s, err
		}
		if err := q.QueryRow(ctx, `SELECT c.name FROM golf.caddy_assignments a JOIN golf.caddies c ON c.id = a.caddy_id
			WHERE $1 = ANY(a.player_ids) AND a.status IN ('assigned', 'in_play', 'completed') ORDER BY lower(a.period) DESC LIMIT 1`, *sc.PlayerID).
			Scan(&s.CaddyName); err != nil && !dbtx.IsNoRows(err) {
			return s, err
		}
	}
	if s.HandicapIndex == nil && sc.CustomerID != nil {
		s.HandicapIndex = currentHandicap(ctx, q, *sc.CustomerID)
	}
	if s.HandicapIndex != nil {
		ch := CourseHandicap(dec(*s.HandicapIndex), sc.CourseRating, sc.SlopeRating, sc.Par, sc.Holes)
		s.CourseHandicap = &ch
		if s.Gross != nil {
			n := *s.Gross - ch
			s.Net = &n
		}
	}
	if sc.CustomerID != nil || guest != nil {
		if _, s.Email, s.Phone, err = crm.Contact(ctx, q, sc.CustomerID, guest); err != nil {
			return s, err
		}
	}
	if s.Phone == "" && phone != nil {
		s.Phone = *phone
	}
	if err := q.QueryRow(ctx, `SELECT share_token IS NOT NULL FROM golf.scorecards WHERE id = $1`, sid).Scan(&s.Shared); err != nil {
		return s, err
	}
	return s, nil
}

// BookingScorecards lists the cards of a booking's players.
func (m *Module) BookingScorecards(ctx context.Context, q dbtx.Querier, property, bid uuid.UUID) ([]ScorecardSheet, error) {
	rows, err := q.Query(ctx, `SELECT s.id FROM golf.scorecards s JOIN golf.booking_players p ON p.id = s.booking_player_id
		WHERE p.booking_id = $1 AND s.property_id = $2 ORDER BY p.seq`, bid, property)
	if err != nil {
		return nil, err
	}
	ids, err := pgx.CollectRows(rows, pgx.RowTo[uuid.UUID])
	if err != nil {
		return nil, err
	}
	out := []ScorecardSheet{}
	for _, sid := range ids {
		s, err := m.scorecardSheet(ctx, q, sid)
		if err != nil {
			return nil, err
		}
		out = append(out, s)
	}
	return out, nil
}

func signed(n int) string {
	if n > 0 {
		return fmt.Sprintf("+%d", n)
	}
	return fmt.Sprint(n)
}

func intOr(p *int, none string) string {
	if p == nil {
		return none
	}
	return fmt.Sprint(*p)
}

func strOr(p *string, none string) string {
	if p == nil || *p == "" {
		return none
	}
	return *p
}

// ScorecardPDF renders the card on one A4 page (WinAnsi text: no dashes
// beyond Latin-1).
func ScorecardPDF(s ScorecardSheet, printed time.Time) []byte {
	d := pdf.New()
	d.Row(18, true, s.ClubName)
	d.Row(12, false, "Scorecard · "+s.CourseName+" · "+s.RouteName)
	d.Rule(d.Y + 6)
	d.Space(8)
	info := []float64{pdf.Margin, pdf.Margin + 100, 310, 410}
	tee := strOr(s.TeeSetName, "-")
	if s.TeeColor != nil && !strings.EqualFold(*s.TeeColor, tee) {
		tee += " (" + *s.TeeColor + ")"
	}
	rating := "-"
	if s.CourseRating != nil {
		rating = *s.CourseRating + " / " + intOr(s.SlopeRating, "-")
	}
	ch := "-"
	if s.CourseHandicap != nil {
		ch = fmt.Sprint(*s.CourseHandicap)
	}
	d.Columns(10, false, info, []string{"Player", s.PlayerName, "Date", s.PlayedOn.Format("02 Jan 2006")})
	d.Columns(10, false, info, []string{"Tee", tee, "Rating / Slope", rating})
	d.Columns(10, false, info, []string{"Handicap index", strOr(s.HandicapIndex, "-"), "Course handicap", ch})
	d.Columns(10, false, info, []string{"Caddy", strOr(s.CaddyName, "-"), "Status", s.Status})
	d.Space(8)

	cols := []float64{pdf.Margin, pdf.Margin + 70, -330, -260, -190, -120, -40}
	d.Columns(10, true, cols, []string{"Hole", "Course hole", "Par", "S.I.", "Strokes", "Putts", "+/-"})
	d.Rule(d.Y + 7)
	type sum struct{ par, strokes, putts int }
	var part, total sum
	line := func(label string, t sum) {
		d.Columns(10, true, cols, []string{label, "", fmt.Sprint(t.par), "", fmt.Sprint(t.strokes), fmt.Sprint(t.putts), ""})
	}
	for i, h := range s.Scores {
		diff := ""
		if h.Strokes != nil {
			diff = signed(*h.Strokes - h.Par)
			part.strokes += *h.Strokes
			total.strokes += *h.Strokes
		}
		if h.Putts != nil {
			part.putts += *h.Putts
			total.putts += *h.Putts
		}
		part.par += h.Par
		total.par += h.Par
		d.Columns(10, false, cols, []string{fmt.Sprint(h.Seq), fmt.Sprintf("%s-%d", h.SectionCode, h.HoleNumber), fmt.Sprint(h.Par),
			intOr(h.StrokeIndex, ""), intOr(h.Strokes, "-"), intOr(h.Putts, ""), diff})
		if len(s.Scores) == 18 && i == 8 {
			line("Out", part)
			part = sum{}
		}
	}
	if len(s.Scores) == 18 {
		line("In", part)
	}
	d.Rule(d.Y + 7)
	line("Total", total)
	d.Space(10)
	d.Row(12, true, "Gross", intOr(s.Gross, "-"))
	d.Row(12, false, "Course handicap", ch)
	d.Row(12, true, "Net", intOr(s.Net, "-"))
	if s.Gross != nil {
		d.Row(10, false, "To par", signed(*s.Gross-s.Par))
	}
	d.Space(16)
	d.Row(8, false, "Printed "+printed.Format("02 Jan 2006 15:04")+" · OneClub")
	return d.Bytes()
}

func writePDF(w http.ResponseWriter, name string, b []byte) {
	w.Header().Set("Content-Type", "application/pdf")
	w.Header().Set("Content-Disposition", `inline; filename="`+name+`.pdf"`)
	_, _ = w.Write(b)
}

func sheetFile(s ScorecardSheet) string {
	return "scorecard-" + s.PlayedOn.Format("2006-01-02") + "-" + strings.ReplaceAll(strings.ToLower(s.PlayerName), " ", "-")
}

// requestOrigin is the scheme and host the request came in on (the public
// link opens on the same domain).
func requestOrigin(r *http.Request) string {
	scheme := "https"
	if p := r.Header.Get("X-Forwarded-Proto"); p != "" {
		scheme = p
	} else if r.TLS == nil {
		scheme = "http"
	}
	host := r.Header.Get("X-Forwarded-Host")
	if host == "" {
		host = r.Host
	}
	return scheme + "://" + host
}

// ShareScorecard makes the public link of a card (once) and sends it.
func (m *Module) ShareScorecard(ctx context.Context, tx pgx.Tx, property, sid uuid.UUID, origin string, in ScorecardShareInput) (ScorecardShare, error) {
	s, err := m.scorecardSheet(ctx, tx, sid)
	if err != nil {
		return ScorecardShare{}, err
	}
	if s.PropertyID != property {
		return ScorecardShare{}, errs.NotFound("scorecard")
	}
	var token string
	if err := tx.QueryRow(ctx, `UPDATE golf.scorecards SET share_token = coalesce(share_token, $2) WHERE id = $1 RETURNING share_token`,
		sid, secret.RandomToken(24)).Scan(&token); err != nil {
		return ScorecardShare{}, err
	}
	out := ScorecardShare{Link: origin + "/api/v1/public/golf/scorecards/" + token, SentTo: []string{}}
	if in.Send {
		email, phone := strings.TrimSpace(in.Email), strings.TrimSpace(in.Phone)
		if email == "" && phone == "" {
			email, phone = s.Email, s.Phone
		}
		var ch []string
		if email != "" {
			ch = append(ch, notify.ChannelEmail)
			out.SentTo = append(out.SentTo, email)
		}
		if phone != "" {
			ch = append(ch, notify.ChannelWhatsApp)
			out.SentTo = append(out.SentTo, phone)
		}
		if len(ch) == 0 {
			return out, errs.Validation("recipient_required", "no e-mail or phone to send the scorecard to", errs.Field("email", "required", "e-mail or phone"))
		}
		if m.Notify != nil {
			if err := m.Notify.Send(ctx, tx, notify.Message{Event: "golf.scorecard_shared", Category: "golf", Email: email, Phone: phone, Name: s.PlayerName,
				Channels: ch, PropertyID: &property, Data: map[string]any{"name": s.PlayerName, "club": s.ClubName, "course": s.CourseName,
					"date": s.PlayedOn.Format("02 Jan 2006"), "gross": intOr(s.Gross, "—"), "net": intOr(s.Net, "—"), "link": out.Link}}); err != nil {
				return out, err
			}
		}
	}
	return out, record(ctx, tx, "golf.scorecard", sid, s.PlayerName, "share", property, nil, map[string]any{"sentTo": out.SentTo}, "")
}

func (m *Module) registerScorecardSheets(reg *route.Registry, add func(tag string, rt route.Route)) {
	db := m.DB
	add("Scoring", route.Route{Method: http.MethodGet, Path: "/api/v1/golf/bookings/{id}/scorecards", Summary: "Scorecards of a booking's players (print and share at the front desk)",
		Permission: "golf.booking.view", Response: ScorecardSheet{}, List: true,
		Handler: handle.Read(db, func(ctx context.Context, tx pgx.Tx, r *http.Request) (httpx.Page[ScorecardSheet], error) {
			bid, err := handle.ID(r)
			if err != nil {
				return httpx.Page[ScorecardSheet]{}, err
			}
			return handle.Page(m.BookingScorecards(ctx, tx, handle.Property(ctx), bid))
		})})
	add("Scoring", route.Route{Method: http.MethodGet, Path: "/api/v1/golf/scorecards/{id}/pdf", Summary: "Printable scorecard (PDF)",
		Permission: "golf.booking.view", RawContent: "application/pdf", Query: []route.Param{{Name: "propertyId", Description: "Active property (a link cannot send X-Property-Id)"}},
		Handler: func(w http.ResponseWriter, r *http.Request) {
			ctx := r.Context()
			var s ScorecardSheet
			var at time.Time
			err := db.WithReadTx(ctx, func(tx pgx.Tx) error {
				sid, err := handle.ID(r)
				if err != nil {
					return err
				}
				if s, err = m.scorecardSheet(ctx, tx, sid); err == nil && s.PropertyID != handle.Property(ctx) {
					err = errs.NotFound("scorecard")
				}
				at = clock.Now().In(location(ctx, tx, s.PropertyID))
				return err
			})
			if err != nil {
				httpx.WriteError(w, r, err)
				return
			}
			writePDF(w, sheetFile(s), ScorecardPDF(s, at))
		}})
	add("Scoring", route.Route{Method: http.MethodPost, Path: "/api/v1/golf/scorecards/{id}:share", Summary: "Share a scorecard: public PDF link, sent by e-mail / WhatsApp",
		Permission: "golf.check_in.perform", Request: ScorecardShareInput{}, Response: ScorecardShare{}, Status: http.StatusOK,
		Handler: handle.Write(db, http.StatusOK, func(ctx context.Context, tx pgx.Tx, r *http.Request, in ScorecardShareInput) (ScorecardShare, error) {
			sid, err := handle.ID(r)
			if err != nil {
				return ScorecardShare{}, err
			}
			return m.ShareScorecard(ctx, tx, handle.Property(ctx), sid, requestOrigin(r), in)
		})})
	crm.PublicRoute(reg, "golf", route.Route{Method: http.MethodGet, Path: "/api/v1/public/golf/scorecards/{token}", Summary: "Shared scorecard (PDF, no account needed)",
		RawContent: "application/pdf",
		Handler: func(w http.ResponseWriter, r *http.Request) {
			// the random token is the key: looked up across the properties
			ctx := dbtx.System(r.Context())
			var s ScorecardSheet
			var at time.Time
			err := db.WithReadTx(ctx, func(tx pgx.Tx) error {
				var sid uuid.UUID
				if err := tx.QueryRow(ctx, `SELECT id FROM golf.scorecards WHERE share_token = $1`, chi.URLParam(r, "token")).Scan(&sid); err != nil {
					return errs.NotFound("scorecard")
				}
				var err error
				s, err = m.scorecardSheet(ctx, tx, sid)
				at = clock.Now().In(location(ctx, tx, s.PropertyID))
				return err
			})
			if err != nil {
				httpx.WriteError(w, r, err)
				return
			}
			writePDF(w, sheetFile(s), ScorecardPDF(s, at))
		}})
}
