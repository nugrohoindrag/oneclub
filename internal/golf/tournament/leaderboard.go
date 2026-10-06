package tournament

// FR-TRN-08 Leaderboard: live gross, net and stableford standings per
// division from the P2 scorecards — strokes received per hole from the
// playing handicap and the stroke index, stableford points, multi-round
// totals, cut, DQ / WD / NR — with the tie-break of Tournament Policies
// (countback on the last 9, 6, 3 and 1 holes; net countback with 1/2, 1/3,
// 1/6 and 1/18 of the handicap, R&A Appendix). Screens refresh through the
// realtime topic golf.tournament (< 2 s online, ≤ 1 minute after an offline
// tablet syncs).

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/shopspring/decimal"

	"oneclub/internal/golf/experience"
	"oneclub/internal/kernel/dbtx"
	"oneclub/internal/platform/handle"
)

// LeaderboardRound is a player's round on the leaderboard.
type TournamentLeaderboardRound struct {
	Round  int    `json:"round"`
	Thru   int    `json:"thru"`
	Gross  *int   `json:"gross" doc:"Complete rounds only"`
	Net    *int   `json:"net"`
	Points *int   `json:"points"`
	ToPar  int    `json:"toPar" doc:"Gross to par over the holes played"`
	Status string `json:"status" enum:"not_started,in_progress,submitted,finalized,dq,wd,nr"`
}

// TournamentLeaderboardEntry is one line of a leaderboard.
type TournamentLeaderboardEntry struct {
	Position        *int                         `json:"position"`
	PositionLabel   string                       `json:"positionLabel" doc:"1, T2 (tie), MC, WD, NR, DQ, - (not started)"`
	Tied            bool                         `json:"tied"`
	RegistrationID  *uuid.UUID                   `json:"registrationId,omitempty"`
	PlayerName      string                       `json:"playerName"`
	DivisionName    *string                      `json:"divisionName"`
	HandicapIndex   *string                      `json:"handicapIndex"`
	PlayingHandicap *int                         `json:"playingHandicap"`
	Thru            string                       `json:"thru" doc:"Holes completed in the current round; F = finished"`
	ToPar           *int                         `json:"toPar" doc:"Gross or net strokes to par (stroke categories)"`
	Points          *int                         `json:"points" doc:"Stableford points (stableford category)"`
	Score           *int                         `json:"score" doc:"Total of the category: gross / net strokes of complete rounds or stableford points"`
	Today           *int                         `json:"today" doc:"Current round: to par or points"`
	Rounds          []TournamentLeaderboardRound `json:"rounds"`
	Status          string                       `json:"status" enum:"not_started,playing,finished,mc,wd,nr,dq"`
	TieBreak        *string                      `json:"tieBreak" doc:"Countback that separated a tie"`

	customerID uuid.UUID
	divisionID *uuid.UUID
	consent    bool
}

// LeaderboardBoard is the leaderboard of a category, overall or per division.
type TournamentLeaderboardBoard struct {
	Category   string                       `json:"category" enum:"gross,net,stableford"`
	DivisionID *uuid.UUID                   `json:"divisionId"`
	Division   string                       `json:"division" doc:"Overall or the division name"`
	Entries    []TournamentLeaderboardEntry `json:"entries"`
}

// TournamentLeaderboard is the live (or final) leaderboard.
type TournamentLeaderboard struct {
	TournamentID uuid.UUID                    `json:"tournamentId"`
	Code         string                       `json:"code"`
	Name         string                       `json:"name"`
	Format       string                       `json:"format" enum:"stroke_play,stableford"`
	ScoringBasis string                       `json:"scoringBasis"`
	Status       string                       `json:"status"`
	StartType    string                       `json:"startType"`
	CurrentRound int                          `json:"currentRound"`
	RoundCount   int                          `json:"roundCount"`
	RoundStatus  string                       `json:"roundStatus"`
	TieBreak     string                       `json:"tieBreak" enum:"countback,shared"`
	Final        bool                         `json:"final" doc:"Results finalized"`
	UpdatedAt    time.Time                    `json:"updatedAt"`
	Sponsors     []TournamentSponsorLogo      `json:"sponsors"`
	Boards       []TournamentLeaderboardBoard `json:"boards"`
}

// categories of a tournament: stroke play → gross / net; stableford →
// stableford points (net) and best gross.
func categories(t Tournament) []string {
	var out []string
	gross := t.ScoringBasis != "net"
	net := t.ScoringBasis != "gross"
	if t.Format == "stableford" {
		if net {
			out = append(out, "stableford")
		}
		if gross {
			out = append(out, "gross")
		}
		return out
	}
	if gross {
		out = append(out, "gross")
	}
	if net {
		out = append(out, "net")
	}
	return out
}

// primaryCategory decides the cut, the standings draw and the champion.
func primaryCategory(t Tournament) string {
	if t.Format == "stableford" && t.ScoringBasis != "gross" {
		return "stableford"
	}
	if t.ScoringBasis == "net" {
		return "net"
	}
	return "gross"
}

// ── card arithmetic ───────────────────────────────────────────────────────

type holeCalc struct {
	Seq      int
	Number   int
	Section  string
	Par      int
	SI       *int
	Received int
	Strokes  *int
	Putts    *int
	Term     *string
}

type cardCalc struct {
	holes      []holeCalc
	n          int
	ph         int
	thru       int
	complete   bool
	gross      int
	toPar      int
	netToPar   int
	points     int
	grossPts   int
	par        int
	playedPar  int
	front      int
	back       int
	frontOK    bool
	backOK     bool
	handicapOK bool
}

// received distributes the playing handicap over the holes by stroke
// index (plus handicaps give strokes back on the easiest holes).
func received(holes []experience.ScoreHole, ph int) map[int]int {
	n := len(holes)
	out := map[int]int{}
	if n == 0 {
		return out
	}
	order := make([]experience.ScoreHole, n)
	copy(order, holes)
	sort.SliceStable(order, func(i, j int) bool {
		a, b := order[i].StrokeIndex, order[j].StrokeIndex
		switch {
		case a == nil && b == nil:
			return order[i].Seq < order[j].Seq
		case a == nil:
			return false
		case b == nil:
			return true
		case *a != *b:
			return *a < *b
		}
		return order[i].Seq < order[j].Seq
	})
	abs := ph
	if abs < 0 {
		abs = -abs
	}
	base, extra := abs/n, abs%n
	for rank, h := range order {
		r := base
		if ph >= 0 && rank < extra {
			r++
		}
		if ph < 0 && rank >= n-extra {
			r++
		}
		if ph < 0 {
			r = -r
		}
		out[h.Seq] = r
	}
	return out
}

func calcCard(sc experience.Scorecard, playing *int) cardCalc {
	c := cardCalc{n: len(sc.Scores), handicapOK: playing != nil}
	if playing != nil {
		c.ph = *playing
	}
	rec := received(sc.Scores, c.ph)
	half := c.n / 2
	c.frontOK, c.backOK = true, true
	for i, h := range sc.Scores {
		hc := holeCalc{Seq: h.Seq, Number: h.HoleNumber, Section: h.SectionCode, Par: h.Par, SI: h.StrokeIndex, Received: rec[h.Seq], Strokes: h.Strokes,
			Putts: h.Putts, Term: h.Term}
		c.par += h.Par
		if h.Strokes != nil {
			s := *h.Strokes
			c.thru++
			c.gross += s
			c.playedPar += h.Par
			c.toPar += s - h.Par
			c.netToPar += s - h.Par - hc.Received
			c.points += max(0, 2+h.Par+hc.Received-s)
			c.grossPts += max(0, 2+h.Par-s)
			if i < half {
				c.front += s
			} else {
				c.back += s
			}
		} else if i < half {
			c.frontOK = false
		} else {
			c.backOK = false
		}
		c.holes = append(c.holes, hc)
	}
	c.complete = c.n > 0 && c.thru == c.n
	return c
}

// segment is the countback score of the last k holes (lower is better for
// strokes; net subtracts k/n of the playing handicap).
func (c cardCalc) segment(cat string, k int) decimal.Decimal {
	s := decimal.Zero
	for i := c.n - k; i < c.n; i++ {
		if i < 0 {
			continue
		}
		h := c.holes[i]
		if h.Strokes == nil {
			continue
		}
		switch cat {
		case "stableford":
			s = s.Add(decimal.NewFromInt(int64(max(0, 2+h.Par+h.Received-*h.Strokes))))
		default:
			s = s.Add(decimal.NewFromInt(int64(*h.Strokes)))
		}
	}
	if cat == "net" && c.n > 0 {
		s = s.Sub(decimal.NewFromInt(int64(c.ph * k)).Div(decimal.NewFromInt(int64(c.n))))
	}
	return s
}

// ── leaderboard ───────────────────────────────────────────────────────────

type lbRound struct {
	no      int
	status  string
	card    cardCalc
	playing *int
}

type lbPlayer struct {
	id         uuid.UUID
	customer   uuid.UUID
	name       string
	division   *uuid.UUID
	divName    *string
	divSeq     int
	hcp        *string
	consent    bool
	madeCut    *bool
	rounds     []lbRound
	lastRound  int
	finished   bool
	out        string // dq | wd | nr | mc
	hasHoles   bool
	latestCard *cardCalc
}

type computeOptions struct {
	public   bool   // mask players without public consent
	category string // only this category
	division string // "overall" or a division id
	limit    int    // rows per board
}

type entryKey struct {
	group int
	value int
}

// compute builds the leaderboard of a tournament.
func (m *Module) compute(ctx context.Context, q dbtx.Querier, property uuid.UUID, t Tournament, o computeOptions) (TournamentLeaderboard, error) {
	pol, _, err := LoadPolicy(ctx, q, property)
	if err != nil {
		return TournamentLeaderboard{}, err
	}
	tie := pol.TieBreak
	if t.TieBreak != nil {
		tie = *t.TieBreak
	}
	lb := TournamentLeaderboard{TournamentID: t.ID, Code: t.Code, Name: t.Name, Format: t.Format, ScoringBasis: t.ScoringBasis, Status: t.Status,
		StartType: t.StartType, CurrentRound: t.CurrentRound, RoundCount: t.RoundCount, TieBreak: tie, Final: t.Status == "completed", UpdatedAt: now(),
		Boards: []TournamentLeaderboardBoard{}}
	rounds, err := listRounds(ctx, q, t.ID)
	if err != nil {
		return lb, err
	}
	for _, r := range rounds {
		if r.RoundNo == t.CurrentRound {
			lb.RoundStatus = r.Status
		}
	}
	ss, err := listSponsors(ctx, q, t.ID)
	if err != nil {
		return lb, err
	}
	lb.Sponsors = sponsorLogos(ss, true)
	type regRow struct {
		ID         uuid.UUID  `db:"id"`
		CustomerID uuid.UUID  `db:"customer_id"`
		Name       string     `db:"player_name"`
		Division   *uuid.UUID `db:"division_id"`
		DivName    *string    `db:"division_name"`
		DivSeq     *int       `db:"division_seq"`
		Hcp        *string    `db:"handicap_index"`
		Consent    bool       `db:"public_consent"`
		MadeCut    *bool      `db:"made_cut"`
	}
	regs, err := handle.List[regRow](q.Query(ctx, `SELECT r.id, r.customer_id, r.player_name, r.division_id, d.name AS division_name, d.sequence AS division_seq,
		trim_scale(r.handicap_index)::text AS handicap_index, r.public_consent, r.made_cut FROM golf.tournament_registrations r
		LEFT JOIN golf.tournament_divisions d ON d.id = r.division_id WHERE r.tournament_id = $1 AND r.status IN ('registered', 'checked_in')
		ORDER BY r.player_name`, t.ID))
	if err != nil {
		return lb, err
	}
	type scoreRow struct {
		Registration uuid.UUID `db:"registration_id"`
		RoundNo      int       `db:"round_no"`
		Scorecard    uuid.UUID `db:"scorecard_id"`
		Status       string    `db:"status"`
		Playing      *int      `db:"playing_handicap"`
	}
	scores, err := handle.List[scoreRow](q.Query(ctx, `SELECT s.registration_id, x.round_no, s.scorecard_id, s.status, s.playing_handicap
		FROM golf.tournament_scores s JOIN golf.tournament_rounds x ON x.id = s.round_id WHERE s.tournament_id = $1 ORDER BY x.round_no`, t.ID))
	if err != nil {
		return lb, err
	}
	ids := make([]uuid.UUID, 0, len(scores))
	for _, s := range scores {
		ids = append(ids, s.Scorecard)
	}
	cards := map[uuid.UUID]experience.Scorecard{}
	if m.Experience != nil {
		if cards, err = m.Experience.TournamentScorecards(ctx, q, ids); err != nil {
			return lb, err
		}
	}
	players := map[uuid.UUID]*lbPlayer{}
	var order []*lbPlayer
	for _, r := range regs {
		p := &lbPlayer{id: r.ID, customer: r.CustomerID, name: r.Name, division: r.Division, divName: r.DivName, hcp: r.Hcp, consent: r.Consent, madeCut: r.MadeCut,
			divSeq: 999}
		if r.DivSeq != nil {
			p.divSeq = *r.DivSeq
		}
		players[r.ID] = p
		order = append(order, p)
	}
	for _, s := range scores {
		p := players[s.Registration]
		if p == nil {
			continue
		}
		c := calcCard(cards[s.Scorecard], s.Playing)
		p.rounds = append(p.rounds, lbRound{no: s.RoundNo, status: s.Status, card: c, playing: s.Playing})
		switch s.Status {
		case "dq":
			p.out = "dq"
		case "wd", "nr":
			if p.out != "dq" {
				p.out = s.Status
			}
		}
		if c.thru > 0 {
			p.hasHoles = true
		}
	}
	for _, p := range order {
		if p.madeCut != nil && !*p.madeCut && p.out == "" {
			p.out = "mc"
		}
		if len(p.rounds) > 0 {
			last := p.rounds[len(p.rounds)-1]
			p.lastRound = last.no
			p.latestCard = &last.card
			p.finished = last.card.complete
		}
	}
	cats := categories(t)
	type board struct {
		division *uuid.UUID
		name     string
		seq      int
	}
	boards := []board{{name: "Overall", seq: -1}}
	divs, err := listDivisions(ctx, q, t.ID)
	if err != nil {
		return lb, err
	}
	for i := range divs {
		if divs[i].Status == "active" {
			boards = append(boards, board{division: &divs[i].ID, name: divs[i].Name, seq: divs[i].Sequence})
		}
	}
	for _, cat := range cats {
		if o.category != "" && o.category != cat {
			continue
		}
		for _, b := range boards {
			if o.division != "" && ((o.division == "overall") != (b.division == nil) || (b.division != nil && o.division != b.division.String())) {
				continue
			}
			var members []*lbPlayer
			for _, p := range order {
				if b.division == nil || (p.division != nil && *p.division == *b.division) {
					members = append(members, p)
				}
			}
			entries := rank(members, cat, tie, t)
			if o.public {
				for i := range entries {
					mask(&entries[i])
				}
			}
			if o.limit > 0 && len(entries) > o.limit {
				entries = entries[:o.limit]
			}
			lb.Boards = append(lb.Boards, TournamentLeaderboardBoard{Category: cat, DivisionID: b.division, Division: b.name, Entries: entries})
		}
	}
	return lb, nil
}

// mask hides players without public consent on the website (UU PDP).
func mask(e *TournamentLeaderboardEntry) {
	e.RegistrationID = nil
	if e.consent {
		return
	}
	var initials []string
	for _, w := range strings.Fields(e.PlayerName) {
		r := []rune(w)
		initials = append(initials, strings.ToUpper(string(r[0]))+".")
	}
	e.PlayerName = "Player " + strings.Join(initials, "")
	e.HandicapIndex = nil
}

// metricOf is the ranking value of a player in a category: cumulative to
// par (gross / net) or points (stableford, higher is better).
func metricOf(p *lbPlayer, cat string) (value int, total *int, today *int, toPar *int, points *int) {
	tot, totOK := 0, true
	v := 0
	for _, r := range p.rounds {
		if r.status == "dq" || r.status == "wd" || r.status == "nr" {
			continue
		}
		c := r.card
		switch cat {
		case "stableford":
			v += c.points
			tot += c.points
		case "net":
			v += c.netToPar
			if c.complete {
				tot += c.gross - c.ph
			} else if c.thru > 0 {
				totOK = false
			}
		default:
			v += c.toPar
			if c.complete {
				tot += c.gross
			} else if c.thru > 0 {
				totOK = false
			}
		}
	}
	if p.latestCard != nil && p.latestCard.thru > 0 {
		c := p.latestCard
		switch cat {
		case "stableford":
			today = ptr(c.points)
		case "net":
			today = ptr(c.netToPar)
		default:
			today = ptr(c.toPar)
		}
	}
	if cat == "stableford" {
		points = ptr(v)
	} else {
		toPar = ptr(v)
	}
	if totOK && p.hasHoles {
		total = ptr(tot)
	}
	return v, total, today, toPar, points
}

// countback compares two players who finished level: last round, last 9,
// 6, 3 and 1 holes, then hole by hole backwards. It returns <0 when a is
// better, the detail of the deciding segment.
func countback(a, b *lbPlayer, cat string) (int, string) {
	ca, cb := a.latestCard, b.latestCard
	if ca == nil || cb == nil || ca.n != cb.n || ca.n == 0 {
		return 0, ""
	}
	better := func(x, y decimal.Decimal) int {
		if cat == "stableford" {
			return y.Cmp(x)
		}
		return x.Cmp(y)
	}
	n := ca.n
	seen := map[int]bool{}
	for _, k := range []int{n / 2, n / 3, n / 6, 1} {
		if k <= 0 || seen[k] {
			continue
		}
		seen[k] = true
		x, y := ca.segment(cat, k), cb.segment(cat, k)
		if c := better(x, y); c != 0 {
			return c, fmt.Sprintf("C/B last %d: %s v %s", k, x.Round(2).String(), y.Round(2).String())
		}
	}
	for i := n - 2; i >= 0; i-- {
		x, y := segmentAt(ca, cat, i), segmentAt(cb, cat, i)
		if c := better(x, y); c != 0 {
			return c, fmt.Sprintf("C/B hole %d: %s v %s", ca.holes[i].Number, x.String(), y.String())
		}
	}
	return 0, ""
}

func segmentAt(c *cardCalc, cat string, i int) decimal.Decimal {
	h := c.holes[i]
	if h.Strokes == nil {
		return decimal.Zero
	}
	if cat == "stableford" {
		return decimal.NewFromInt(int64(max(0, 2+h.Par+h.Received-*h.Strokes)))
	}
	return decimal.NewFromInt(int64(*h.Strokes))
}

// rank orders the players of a board and assigns positions.
func rank(players []*lbPlayer, cat, tie string, t Tournament) []TournamentLeaderboardEntry {
	type row struct {
		p     *lbPlayer
		key   entryKey
		e     TournamentLeaderboardEntry
		value int
	}
	rows := make([]row, 0, len(players))
	for _, p := range players {
		v, total, today, toPar, points := metricOf(p, cat)
		e := TournamentLeaderboardEntry{RegistrationID: ptr(p.id), PlayerName: p.name, DivisionName: p.divName, HandicapIndex: p.hcp, Score: total, Today: today,
			ToPar: toPar, Points: points, Rounds: []TournamentLeaderboardRound{}, customerID: p.customer, divisionID: p.division, consent: p.consent}
		if p.latestCard != nil {
			for _, r := range p.rounds {
				if r.no == p.lastRound {
					e.PlayingHandicap = r.playing
				}
			}
		}
		for _, r := range p.rounds {
			lr := TournamentLeaderboardRound{Round: r.no, Thru: r.card.thru, ToPar: r.card.toPar, Status: r.status}
			if r.card.complete {
				lr.Gross, lr.Net = ptr(r.card.gross), ptr(r.card.gross-r.card.ph)
			}
			if r.card.thru > 0 {
				lr.Points = ptr(r.card.points)
			}
			e.Rounds = append(e.Rounds, lr)
		}
		key := entryKey{value: v}
		switch {
		case p.out == "dq":
			key.group, e.Status, e.PositionLabel = 4, "dq", "DQ"
		case p.out == "wd" || p.out == "nr":
			key.group, e.Status, e.PositionLabel = 3, p.out, strings.ToUpper(p.out)
		case p.out == "mc":
			key.group, e.Status, e.PositionLabel = 2, "mc", "MC"
		case !p.hasHoles:
			key.group, e.Status, e.PositionLabel = 1, "not_started", "-"
		case p.finished:
			e.Status = "finished"
		default:
			e.Status = "playing"
		}
		if p.latestCard == nil || p.latestCard.thru == 0 {
			e.Thru = "-"
		} else if p.latestCard.complete {
			e.Thru = "F"
		} else {
			e.Thru = itoa(p.latestCard.thru)
		}
		if cat == "stableford" {
			key.value = -v // higher points first
		}
		rows = append(rows, row{p: p, key: key, e: e, value: v})
	}
	useCountback := tie != "shared"
	cmp := func(a, b row) (int, string) {
		if a.key.group != b.key.group {
			return a.key.group - b.key.group, ""
		}
		if a.key.group != 0 {
			return 0, ""
		}
		if a.key.value != b.key.value {
			return a.key.value - b.key.value, ""
		}
		if useCountback && a.p.finished && b.p.finished && a.p.lastRound == b.p.lastRound {
			return countback(a.p, b.p, cat)
		}
		return 0, ""
	}
	sort.SliceStable(rows, func(i, j int) bool {
		c, _ := cmp(rows[i], rows[j])
		if c != 0 {
			return c < 0
		}
		return rows[i].p.name < rows[j].p.name
	})
	out := make([]TournamentLeaderboardEntry, len(rows))
	for i := range rows {
		e := rows[i].e
		if rows[i].key.group == 0 {
			pos := i + 1
			if i > 0 {
				if c, _ := cmp(rows[i-1], rows[i]); c == 0 && rows[i-1].key.group == 0 {
					pos = *out[i-1].Position
					e.Tied = true
					out[i-1].Tied = true
				} else if _, detail := cmp(rows[i-1], rows[i]); detail != "" {
					d := detail
					e.TieBreak = &d
					if out[i-1].TieBreak == nil {
						out[i-1].TieBreak = &d
					}
				}
			}
			e.Position = &pos
			e.PositionLabel = itoa(pos)
		}
		out[i] = e
	}
	for i := range out {
		if out[i].Tied && out[i].Position != nil {
			out[i].PositionLabel = "T" + itoa(*out[i].Position)
		}
	}
	_ = t
	return out
}

// Leaderboard returns the live leaderboard of a tournament.
func (m *Module) Leaderboard(ctx context.Context, q dbtx.Querier, property, tid uuid.UUID, category, division string) (TournamentLeaderboard, error) {
	t, err := tournamentAt(ctx, q, property, tid)
	if err != nil {
		return TournamentLeaderboard{}, err
	}
	return m.compute(ctx, q, property, t, computeOptions{category: category, division: division})
}
