package tournament

// Corporate 360 section of Tournament (PRD P3 FR-C360-04): the corporate
// tournaments a company hosts and the sponsorships it bought; internal/app
// wires it as engagement CorporateSections["tournament"].

import (
	"context"

	"github.com/google/uuid"

	"oneclub/internal/kernel/dbtx"
	"oneclub/internal/platform/handle"
)

// CorporateTournamentRef is a tournament hosted by a company.
type CorporateTournamentRef struct {
	ID              uuid.UUID `json:"id" db:"id"`
	Code            string    `json:"code" db:"code"`
	Name            string    `json:"name" db:"name"`
	StartDate       string    `json:"startDate" db:"start_date"`
	Status          string    `json:"status" db:"status"`
	Players         int       `json:"players" db:"players"`
	QuotationNumber *string   `json:"quotationNumber" db:"quotation_number"`
}

// CorporateSponsorshipRef is a sponsorship bought by a company.
type CorporateSponsorshipRef struct {
	ID             uuid.UUID `json:"id" db:"id"`
	TournamentID   uuid.UUID `json:"tournamentId" db:"tournament_id"`
	TournamentName string    `json:"tournamentName" db:"tournament_name"`
	Level          string    `json:"level" db:"sponsor_level"`
	Amount         string    `json:"amount" db:"amount"`
	Status         string    `json:"status" db:"status"`
}

// CorporateTournamentSection is the Tournament section of the Corporate 360.
type CorporateTournamentSection struct {
	Hosted       []CorporateTournamentRef  `json:"hosted"`
	Sponsorships []CorporateSponsorshipRef `json:"sponsorships"`
}

// CorporateSection lists the tournaments and sponsorships of a company.
func (m *Module) CorporateSection(ctx context.Context, q dbtx.Querier, property, corporate uuid.UUID) (any, error) {
	out := CorporateTournamentSection{}
	var err error
	if out.Hosted, err = handle.List[CorporateTournamentRef](q.Query(ctx, `SELECT t.id, t.code, t.name, to_char(t.start_date, 'YYYY-MM-DD') AS start_date,
		t.status, (SELECT count(*) FROM golf.tournament_registrations r WHERE r.tournament_id = t.id AND r.status <> 'withdrawn')::int AS players,
		t.quotation_number FROM golf.tournaments t WHERE t.property_id = $1 AND t.corporate_account_id = $2 ORDER BY t.start_date DESC LIMIT 20`,
		property, corporate)); err != nil {
		return out, err
	}
	out.Sponsorships, err = handle.List[CorporateSponsorshipRef](q.Query(ctx, `SELECT s.id, s.tournament_id, t.name AS tournament_name, s.sponsor_level,
		trim_scale(s.amount)::text AS amount, s.status FROM golf.tournament_sponsors s JOIN golf.tournaments t ON t.id = s.tournament_id
		WHERE s.property_id = $1 AND s.corporate_account_id = $2 ORDER BY t.start_date DESC LIMIT 20`, property, corporate))
	return out, err
}
