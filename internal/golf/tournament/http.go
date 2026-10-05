package tournament

// HTTP API of Tournament Management (PRD P3 §11: /api/v1/golf/tournaments
// with :open-registration, :close-registration, :publish-draw, :start,
// :finalize, registrations (+ :withdraw), flights, start sheet, leaderboard
// (+ stream SSE), sponsors and prizes), the Tournament Desk and the caddy
// tablet (scores, offline sync), the Member App (FR-APP-P3-04) and the
// website (FR-WEB-P3-03/06, K5 public data).

import (
	"context"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"oneclub/internal/crm"
	"oneclub/internal/golf/experience"
	"oneclub/internal/kernel/dbtx"
	"oneclub/internal/kernel/errs"
	"oneclub/internal/kernel/httpx"
	"oneclub/internal/kernel/reqctx"
	"oneclub/internal/kernel/route"
	"oneclub/internal/platform/handle"
	"oneclub/internal/platform/integration"
)

const (
	tagTournaments = "Golf Tournaments"
	base           = "/api/v1/golf/tournaments"
)

// publicLimiter limits anonymous reads per client IP and path.
var publicLimiter = &handle.Limiter{N: 120, Period: time.Minute}

func (m *Module) add(reg *route.Registry, rt route.Route) {
	rt.Module, rt.Scope = "golf", route.ScopeProperty
	if rt.Tag == "" {
		rt.Tag = tagTournaments
	}
	reg.Add(rt)
}

// RegisterRoutes adds every tournament route.
func (m *Module) RegisterRoutes(reg *route.Registry) {
	m.registerSetup(reg)
	m.registerRegistrations(reg)
	m.registerDraw(reg)
	m.registerScoring(reg)
	m.registerAwards(reg)
	m.registerMember(reg)
	m.registerPublic(reg)
}

// tournamentID parses {id} and checks the tournament is of the property.
func tournamentID(ctx context.Context, q dbtx.Querier, r *http.Request) (uuid.UUID, error) {
	tid, err := handle.ID(r)
	if err != nil {
		return tid, err
	}
	_, err = tournamentAt(ctx, q, handle.Property(ctx), tid)
	return tid, err
}

func pathID(r *http.Request, name string) (uuid.UUID, error) {
	id, err := uuid.Parse(chi.URLParam(r, name))
	if err != nil {
		return id, errs.BadRequest("invalid_id", name+" must be a UUID")
	}
	return id, nil
}

func roundParam(r *http.Request) int { return handle.QueryInt(r, "round", 0) }

// ── setup ─────────────────────────────────────────────────────────────────

func (m *Module) registerSetup(reg *route.Registry) {
	db := m.DB
	m.add(reg, route.Route{Method: http.MethodGet, Path: base, Summary: "Tournament Schedule (filter by status, type, dates)", Permission: "golf.tournament.view",
		Response: Tournament{}, List: true, Query: []route.Param{{Name: "q"}, {Name: "filter[status]"}, {Name: "filter[type]"}, {Name: "filter[source]",
			Description: "oneclub, import (history archive)"}, {Name: "from"}, {Name: "to"}},
		Handler: handle.Read(db, func(ctx context.Context, tx pgx.Tx, r *http.Request) (httpx.Page[Tournament], error) {
			lp := httpx.ParseList(r)
			q := r.URL.Query()
			return handle.Page(List(ctx, tx, handle.Property(ctx), ListFilter{Status: lp.Filters["status"], Type: lp.Filters["type"], Source: lp.Filters["source"],
				From: q.Get("from"), To: q.Get("to"), Search: lp.Q, Limit: lp.Limit}))
		})})
	m.add(reg, route.Route{Method: http.MethodPost, Path: base, Summary: "Create Tournament (draft): dates & rounds, course & playing route, format, field",
		Permission: "golf.tournament.manage", Request: TournamentInput{}, Response: TournamentDetail{},
		Handler: handle.Write(db, http.StatusCreated, func(ctx context.Context, tx pgx.Tx, r *http.Request, in TournamentInput) (TournamentDetail, error) {
			return m.Create(ctx, tx, handle.Property(ctx), in)
		})})
	m.add(reg, route.Route{Method: http.MethodPost, Path: base + ":import", Summary: "Import tournament history & results with Hall of Fame champions (CSV; preview or commit)",
		Permission: "golf.tournament.manage", Request: TournamentHistoryImportInput{}, Response: TournamentHistoryImportResult{}, Status: http.StatusOK,
		Handler: handle.Write(db, http.StatusOK, func(ctx context.Context, tx pgx.Tx, r *http.Request, in TournamentHistoryImportInput) (TournamentHistoryImportResult, error) {
			return m.ImportHistory(ctx, tx, handle.Property(ctx), in)
		})})
	m.add(reg, route.Route{Method: http.MethodGet, Path: base + "/{id}", Summary: "Tournament with rounds, divisions, packages, fees, sponsors and prizes",
		Permission: "golf.tournament.view", Response: TournamentDetail{},
		Handler: handle.Read(db, func(ctx context.Context, tx pgx.Tx, r *http.Request) (TournamentDetail, error) {
			tid, err := handle.ID(r)
			if err != nil {
				return TournamentDetail{}, err
			}
			return m.Detail(ctx, tx, handle.Property(ctx), tid)
		})})
	m.add(reg, route.Route{Method: http.MethodPatch, Path: base + "/{id}", Summary: "Update a tournament (format & schedule frozen once started)",
		Permission: "golf.tournament.manage", Request: TournamentPatch{}, Response: TournamentDetail{},
		Handler: handle.Write(db, http.StatusOK, func(ctx context.Context, tx pgx.Tx, r *http.Request, in TournamentPatch) (TournamentDetail, error) {
			tid, err := handle.ID(r)
			if err != nil {
				return TournamentDetail{}, err
			}
			return m.Update(ctx, tx, handle.Property(ctx), tid, in)
		})})
	m.add(reg, route.Route{Method: http.MethodPost, Path: base + "/{id}:open-registration", Summary: "Open registration (course blocked on the tee sheet)",
		Permission: "golf.tournament.manage", Request: TournamentOpenRegistrationInput{}, Response: TournamentDetail{}, Status: http.StatusOK,
		Handler: handle.Write(db, http.StatusOK, func(ctx context.Context, tx pgx.Tx, r *http.Request, in TournamentOpenRegistrationInput) (TournamentDetail, error) {
			tid, err := handle.ID(r)
			if err != nil {
				return TournamentDetail{}, err
			}
			return m.OpenRegistration(ctx, tx, handle.Property(ctx), tid, in)
		})})
	m.add(reg, route.Route{Method: http.MethodPost, Path: base + "/{id}:close-registration", Summary: "Close registration", Permission: "golf.tournament.manage",
		Request: TournamentReasonInput{}, Response: TournamentDetail{}, Status: http.StatusOK,
		Handler: handle.Write(db, http.StatusOK, func(ctx context.Context, tx pgx.Tx, r *http.Request, in TournamentReasonInput) (TournamentDetail, error) {
			tid, err := handle.ID(r)
			if err != nil {
				return TournamentDetail{}, err
			}
			return m.CloseRegistration(ctx, tx, handle.Property(ctx), tid, in.Reason)
		})})
	m.add(reg, route.Route{Method: http.MethodPost, Path: base + "/{id}:cancel", Summary: "Cancel a tournament (registrations withdrawn with a full refund, tee sheet re-opened)",
		Permission: "golf.tournament.manage", Request: TournamentReasonInput{}, Response: TournamentDetail{}, Status: http.StatusOK,
		Handler: handle.Write(db, http.StatusOK, func(ctx context.Context, tx pgx.Tx, r *http.Request, in TournamentReasonInput) (TournamentDetail, error) {
			tid, err := handle.ID(r)
			if err != nil {
				return TournamentDetail{}, err
			}
			return m.Cancel(ctx, tx, handle.Property(ctx), tid, in.Reason)
		})})
	m.add(reg, route.Route{Method: http.MethodPost, Path: base + "/{id}:start", Summary: "Start the current round — Shotgun Start: every flight tees off at once",
		Permission: "golf.tournament.start", Response: TournamentDetail{}, Status: http.StatusOK,
		Handler: handle.Write(db, http.StatusOK, func(ctx context.Context, tx pgx.Tx, r *http.Request, _ handle.Empty) (TournamentDetail, error) {
			tid, err := handle.ID(r)
			if err != nil {
				return TournamentDetail{}, err
			}
			return m.Start(ctx, tx, handle.Property(ctx), tid)
		})})
	m.add(reg, route.Route{Method: http.MethodPost, Path: base + "/{id}:finalize", Summary: "Finalize: results locked, prizes, Hall of Fame champions, results published",
		Permission: "golf.tournament.finalize", Response: TournamentResults{}, Status: http.StatusOK,
		Handler: handle.Write(db, http.StatusOK, func(ctx context.Context, tx pgx.Tx, r *http.Request, _ handle.Empty) (TournamentResults, error) {
			tid, err := handle.ID(r)
			if err != nil {
				return TournamentResults{}, err
			}
			return m.Finalize(ctx, tx, handle.Property(ctx), tid)
		})})
	m.add(reg, route.Route{Method: http.MethodGet, Path: base + "/{id}/results", Summary: "Final results, champions and awards", Permission: "golf.tournament.view",
		Response: TournamentResults{}, Handler: handle.Read(db, func(ctx context.Context, tx pgx.Tx, r *http.Request) (TournamentResults, error) {
			tid, err := handle.ID(r)
			if err != nil {
				return TournamentResults{}, err
			}
			return m.Results(ctx, tx, handle.Property(ctx), tid)
		})})

	// divisions, packages, fees (Tournament Packages, Tournament Fees)
	sub := func(name, perm string, req, res any, save func(ctx context.Context, tx pgx.Tx, property, tid uuid.UUID, sid *uuid.UUID, r *http.Request) (any, error),
		del func(ctx context.Context, tx pgx.Tx, property, tid, sid uuid.UUID) error, label string) {
		m.add(reg, route.Route{Method: http.MethodPost, Path: base + "/{id}/" + name, Summary: "Add " + label, Permission: perm, Request: req, Response: res,
			Status: http.StatusCreated, Handler: m.write(http.StatusCreated, func(ctx context.Context, tx pgx.Tx, r *http.Request) (any, error) {
				tid, err := handle.ID(r)
				if err != nil {
					return nil, err
				}
				return save(ctx, tx, handle.Property(ctx), tid, nil, r)
			})})
		m.add(reg, route.Route{Method: http.MethodPatch, Path: base + "/{id}/" + name + "/{sid}", Summary: "Update " + label, Permission: perm, Request: req,
			Response: res, Handler: m.write(http.StatusOK, func(ctx context.Context, tx pgx.Tx, r *http.Request) (any, error) {
				tid, err := handle.ID(r)
				if err != nil {
					return nil, err
				}
				sid, err := pathID(r, "sid")
				if err != nil {
					return nil, err
				}
				return save(ctx, tx, handle.Property(ctx), tid, &sid, r)
			})})
		m.add(reg, route.Route{Method: http.MethodDelete, Path: base + "/{id}/" + name + "/{sid}", Summary: "Delete " + label, Permission: perm,
			Handler: m.write(http.StatusNoContent, func(ctx context.Context, tx pgx.Tx, r *http.Request) (any, error) {
				tid, err := handle.ID(r)
				if err != nil {
					return nil, err
				}
				sid, err := pathID(r, "sid")
				if err != nil {
					return nil, err
				}
				return nil, del(ctx, tx, handle.Property(ctx), tid, sid)
			})})
	}
	sub("divisions", "golf.tournament.manage", TournamentDivisionInput{}, TournamentDivision{},
		func(ctx context.Context, tx pgx.Tx, p, tid uuid.UUID, sid *uuid.UUID, r *http.Request) (any, error) {
			var in TournamentDivisionInput
			if err := httpx.Decode(r, &in); err != nil {
				return nil, err
			}
			return m.SaveDivision(ctx, tx, p, tid, sid, in)
		}, m.DeleteDivision, "a division")
	sub("packages", "golf.tournament.manage", TournamentPackageInput{}, TournamentPackage{},
		func(ctx context.Context, tx pgx.Tx, p, tid uuid.UUID, sid *uuid.UUID, r *http.Request) (any, error) {
			var in TournamentPackageInput
			if err := httpx.Decode(r, &in); err != nil {
				return nil, err
			}
			return m.SavePackage(ctx, tx, p, tid, sid, in)
		}, m.DeletePackage, "a Tournament Package")
	sub("fees", "golf.tournament.manage", TournamentFeeInput{}, TournamentFee{},
		func(ctx context.Context, tx pgx.Tx, p, tid uuid.UUID, sid *uuid.UUID, r *http.Request) (any, error) {
			var in TournamentFeeInput
			if err := httpx.Decode(r, &in); err != nil {
				return nil, err
			}
			return m.SaveFee(ctx, tx, p, tid, sid, in)
		}, m.DeleteFee, "a Tournament Fee")
	sub("sponsors", "golf.tournament_sponsor.manage", TournamentSponsorInput{}, TournamentSponsor{},
		func(ctx context.Context, tx pgx.Tx, p, tid uuid.UUID, sid *uuid.UUID, r *http.Request) (any, error) {
			var in TournamentSponsorInput
			if err := httpx.Decode(r, &in); err != nil {
				return nil, err
			}
			return m.SaveSponsor(ctx, tx, p, tid, sid, in)
		}, m.DeleteSponsor, "a sponsor")
	sub("prizes", "golf.tournament_prize.manage", TournamentPrizeInput{}, TournamentPrize{},
		func(ctx context.Context, tx pgx.Tx, p, tid uuid.UUID, sid *uuid.UUID, r *http.Request) (any, error) {
			var in TournamentPrizeInput
			if err := httpx.Decode(r, &in); err != nil {
				return nil, err
			}
			return m.SavePrize(ctx, tx, p, tid, sid, in)
		}, m.DeletePrize, "a prize")
}

// write runs fn in a transaction (handlers that decode themselves).
func (m *Module) write(status int, fn func(ctx context.Context, tx pgx.Tx, r *http.Request) (any, error)) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ctx := r.Context()
		var out any
		err := m.DB.WithTx(ctx, func(tx pgx.Tx) error {
			var err error
			out, err = fn(ctx, tx, r)
			return err
		})
		if err != nil {
			httpx.WriteError(w, r, err)
			return
		}
		if status == http.StatusNoContent {
			httpx.NoContent(w)
			return
		}
		httpx.JSON(w, status, out)
	}
}

// ── registrations ─────────────────────────────────────────────────────────

func (m *Module) registerRegistrations(reg *route.Registry) {
	db := m.DB
	m.add(reg, route.Route{Method: http.MethodGet, Path: base + "/{id}/registrations", Summary: "Participants (Registered, Waitlisted, Withdrawn, Checked-in)",
		Permission: "golf.tournament_registration.view", Response: TournamentRegistration{}, List: true,
		Query: []route.Param{{Name: "q"}, {Name: "filter[status]"}, {Name: "filter[divisionId]"}},
		Handler: handle.Read(db, func(ctx context.Context, tx pgx.Tx, r *http.Request) (httpx.Page[TournamentRegistration], error) {
			tid, err := tournamentID(ctx, tx, r)
			if err != nil {
				return httpx.Page[TournamentRegistration]{}, err
			}
			lp := httpx.ParseList(r)
			limit := lp.Limit
			if r.URL.Query().Get("limit") == "" {
				limit = 500
			}
			return handle.Page(Registrations(ctx, tx, tid, lp.Filters["status"], lp.Filters["divisionId"], lp.Q, limit))
		})})
	m.add(reg, route.Route{Method: http.MethodPost, Path: base + "/{id}/registrations", Summary: "Register a player (member, customer or guest) with the Tournament Fee",
		Permission: "golf.tournament_registration.manage", Request: TournamentRegistrationInput{}, Response: TournamentRegistrationDetail{}, Idempotent: true,
		Handler: handle.Write(db, http.StatusCreated, func(ctx context.Context, tx pgx.Tx, r *http.Request, in TournamentRegistrationInput) (TournamentRegistrationDetail, error) {
			tid, err := handle.ID(r)
			if err != nil {
				return TournamentRegistrationDetail{}, err
			}
			return m.Register(ctx, tx, handle.Property(ctx), tid, in)
		})})
	regID := func(ctx context.Context, q dbtx.Querier, r *http.Request) (uuid.UUID, uuid.UUID, error) {
		tid, err := handle.ID(r)
		if err != nil {
			return tid, uuid.Nil, err
		}
		rid, err := pathID(r, "rid")
		return tid, rid, err
	}
	m.add(reg, route.Route{Method: http.MethodGet, Path: base + "/{id}/registrations/{rid}", Summary: "Participant with folio, payment and start",
		Permission: "golf.tournament_registration.view", Response: TournamentRegistrationDetail{},
		Handler: handle.Read(db, func(ctx context.Context, tx pgx.Tx, r *http.Request) (TournamentRegistrationDetail, error) {
			tid, rid, err := regID(ctx, tx, r)
			if err != nil {
				return TournamentRegistrationDetail{}, err
			}
			d, err := m.registrationDetail(ctx, tx, rid)
			if err == nil && (d.TournamentID != tid || d.PropertyID != handle.Property(ctx)) {
				return TournamentRegistrationDetail{}, errs.NotFound("registration")
			}
			return d, err
		})})
	m.add(reg, route.Route{Method: http.MethodPatch, Path: base + "/{id}/registrations/{rid}", Summary: "Change a participant (division, package, handicap, pairing)",
		Permission: "golf.tournament_registration.manage", Request: TournamentRegistrationPatch{}, Response: TournamentRegistrationDetail{},
		Handler: handle.Write(db, http.StatusOK, func(ctx context.Context, tx pgx.Tx, r *http.Request, in TournamentRegistrationPatch) (TournamentRegistrationDetail, error) {
			tid, rid, err := regID(ctx, tx, r)
			if err != nil {
				return TournamentRegistrationDetail{}, err
			}
			return m.UpdateRegistration(ctx, tx, handle.Property(ctx), tid, rid, in)
		})})
	m.add(reg, route.Route{Method: http.MethodPost, Path: base + "/{id}/registrations/{rid}:withdraw", Summary: "Withdraw (refund per Tournament Policies; waitlist promoted)",
		Permission: "golf.tournament_registration.manage", Request: TournamentReasonInput{}, Response: TournamentWithdrawResult{}, Status: http.StatusOK,
		Handler: handle.Write(db, http.StatusOK, func(ctx context.Context, tx pgx.Tx, r *http.Request, in TournamentReasonInput) (TournamentWithdrawResult, error) {
			tid, rid, err := regID(ctx, tx, r)
			if err != nil {
				return TournamentWithdrawResult{}, err
			}
			return m.Withdraw(ctx, tx, handle.Property(ctx), tid, rid, in.Reason)
		})})
	m.add(reg, route.Route{Method: http.MethodPost, Path: base + "/{id}/registrations/{rid}:check-in", Summary: "Check in a participant (Tournament Desk)",
		Permission: "golf.tournament_registration.check_in", Response: TournamentRegistrationDetail{}, Status: http.StatusOK,
		Handler: handle.Write(db, http.StatusOK, func(ctx context.Context, tx pgx.Tx, r *http.Request, _ handle.Empty) (TournamentRegistrationDetail, error) {
			tid, rid, err := regID(ctx, tx, r)
			if err != nil {
				return TournamentRegistrationDetail{}, err
			}
			return m.CheckIn(ctx, tx, handle.Property(ctx), tid, rid)
		})})
	m.add(reg, route.Route{Method: http.MethodPost, Path: base + "/{id}/registrations/{rid}:pay", Summary: "Take the tournament fee at the desk",
		Permission: "golf.tournament_registration.manage", Request: TournamentPayInput{}, Response: TournamentRegistrationDetail{}, Status: http.StatusOK, Idempotent: true,
		Handler: handle.Write(db, http.StatusOK, func(ctx context.Context, tx pgx.Tx, r *http.Request, in TournamentPayInput) (TournamentRegistrationDetail, error) {
			tid, rid, err := regID(ctx, tx, r)
			if err != nil {
				return TournamentRegistrationDetail{}, err
			}
			return m.Pay(ctx, tx, handle.Property(ctx), tid, rid, in)
		})})
	m.add(reg, route.Route{Method: http.MethodPost, Path: base + "/{id}/registrations/{rid}:waive-fee", Summary: "Request a fee waiver (complimentary entry, approval)",
		Permission: "golf.tournament_registration.waive_fee", Request: TournamentReasonInput{}, Response: TournamentRegistrationDetail{}, Status: http.StatusOK,
		Handler: handle.Write(db, http.StatusOK, func(ctx context.Context, tx pgx.Tx, r *http.Request, in TournamentReasonInput) (TournamentRegistrationDetail, error) {
			tid, rid, err := regID(ctx, tx, r)
			if err != nil {
				return TournamentRegistrationDetail{}, err
			}
			return m.RequestFeeWaiver(ctx, tx, handle.Property(ctx), tid, rid, in.Reason)
		})})
}

// ── flighting, tee assignment, start sheet ────────────────────────────────

func (m *Module) registerDraw(reg *route.Registry) {
	db := m.DB
	m.add(reg, route.Route{Method: http.MethodGet, Path: base + "/{id}/flights", Summary: "Flights and starts of a round (Flighting, Tee Assignment)",
		Permission: "golf.tournament.view", Response: TournamentDraw{}, Query: []route.Param{{Name: "round", Type: "integer"}},
		Handler: handle.Read(db, func(ctx context.Context, tx pgx.Tx, r *http.Request) (TournamentDraw, error) {
			tid, err := handle.ID(r)
			if err != nil {
				return TournamentDraw{}, err
			}
			return m.Draw(ctx, tx, handle.Property(ctx), tid, roundParam(r))
		})})
	m.add(reg, route.Route{Method: http.MethodPost, Path: base + "/{id}/flights:generate", Summary: "Automatic flighting (handicap, division, random, standings) and starts",
		Permission: "golf.tournament_draw.manage", Request: TournamentDrawInput{}, Response: TournamentDraw{}, Status: http.StatusOK,
		Handler: handle.Write(db, http.StatusOK, func(ctx context.Context, tx pgx.Tx, r *http.Request, in TournamentDrawInput) (TournamentDraw, error) {
			tid, err := handle.ID(r)
			if err != nil {
				return TournamentDraw{}, err
			}
			return m.GenerateDraw(ctx, tx, handle.Property(ctx), tid, in)
		})})
	m.add(reg, route.Route{Method: http.MethodPost, Path: base + "/{id}/flights:move-player", Summary: "Manual flighting: move a player to another (or a new) flight",
		Permission: "golf.tournament_draw.manage", Request: TournamentMoveInput{}, Response: TournamentDraw{}, Status: http.StatusOK,
		Handler: handle.Write(db, http.StatusOK, func(ctx context.Context, tx pgx.Tx, r *http.Request, in TournamentMoveInput) (TournamentDraw, error) {
			tid, err := handle.ID(r)
			if err != nil {
				return TournamentDraw{}, err
			}
			return m.MovePlayer(ctx, tx, handle.Property(ctx), tid, in)
		})})
	m.add(reg, route.Route{Method: http.MethodPatch, Path: base + "/{id}/flights/{fid}", Summary: "Change the start or the caddies of a flight",
		Permission: "golf.tournament_draw.manage", Request: TournamentFlightPatch{}, Response: TournamentFlight{},
		Handler: handle.Write(db, http.StatusOK, func(ctx context.Context, tx pgx.Tx, r *http.Request, in TournamentFlightPatch) (TournamentFlight, error) {
			tid, err := handle.ID(r)
			if err != nil {
				return TournamentFlight{}, err
			}
			fid, err := pathID(r, "fid")
			if err != nil {
				return TournamentFlight{}, err
			}
			return m.UpdateFlight(ctx, tx, handle.Property(ctx), tid, fid, in)
		})})
	m.add(reg, route.Route{Method: http.MethodPost, Path: base + "/{id}/flights/{fid}:tee-off", Summary: "Send a flight out (tee times / late shotgun group)",
		Permission: "golf.tournament.start", Response: TournamentFlight{}, Status: http.StatusOK,
		Handler: handle.Write(db, http.StatusOK, func(ctx context.Context, tx pgx.Tx, r *http.Request, _ handle.Empty) (TournamentFlight, error) {
			tid, err := handle.ID(r)
			if err != nil {
				return TournamentFlight{}, err
			}
			fid, err := pathID(r, "fid")
			if err != nil {
				return TournamentFlight{}, err
			}
			return m.TeeOff(ctx, tx, handle.Property(ctx), tid, fid)
		})})
	m.add(reg, route.Route{Method: http.MethodPost, Path: base + "/{id}:publish-draw", Summary: "Publish the start sheet: scorecards open, players notified",
		Permission: "golf.tournament_draw.manage", Request: TournamentPublishDrawInput{}, Response: TournamentDraw{}, Status: http.StatusOK,
		Handler: handle.Write(db, http.StatusOK, func(ctx context.Context, tx pgx.Tx, r *http.Request, in TournamentPublishDrawInput) (TournamentDraw, error) {
			tid, err := handle.ID(r)
			if err != nil {
				return TournamentDraw{}, err
			}
			return m.PublishDraw(ctx, tx, handle.Property(ctx), tid, in)
		})})
	m.add(reg, route.Route{Method: http.MethodGet, Path: base + "/{id}/start-sheet", Summary: "Start sheet of a round (starting hole / tee time of every player)",
		Permission: "golf.tournament.view", Response: TournamentStartSheet{}, Query: []route.Param{{Name: "round", Type: "integer"}},
		Handler: handle.Read(db, func(ctx context.Context, tx pgx.Tx, r *http.Request) (TournamentStartSheet, error) {
			tid, err := handle.ID(r)
			if err != nil {
				return TournamentStartSheet{}, err
			}
			return m.GetStartSheet(ctx, tx, handle.Property(ctx), tid, roundParam(r))
		})})
	m.add(reg, route.Route{Method: http.MethodGet, Path: base + "/{id}/start-sheet.pdf", Summary: "Start sheet for printing (PDF)", Permission: "golf.tournament.view",
		RawContent: "application/pdf", Query: []route.Param{{Name: "round", Type: "integer"}},
		Handler: func(w http.ResponseWriter, r *http.Request) {
			ctx := r.Context()
			var s TournamentStartSheet
			err := db.WithReadTx(ctx, func(tx pgx.Tx) error {
				tid, err := handle.ID(r)
				if err != nil {
					return err
				}
				s, err = m.GetStartSheet(ctx, tx, handle.Property(ctx), tid, roundParam(r))
				return err
			})
			if err != nil {
				httpx.WriteError(w, r, err)
				return
			}
			w.Header().Set("Content-Type", "application/pdf")
			w.Header().Set("Content-Disposition", `inline; filename="start-sheet-`+s.Code+`-R`+strconv.Itoa(s.RoundNo)+`.pdf"`)
			_, _ = w.Write(StartSheetPDF(s))
		}})
}

// ── scoring (desk, caddy tablet), leaderboard ─────────────────────────────

func (m *Module) registerScoring(reg *route.Registry) {
	db := m.DB
	m.add(reg, route.Route{Method: http.MethodGet, Path: base + "/{id}/scores", Summary: "Scorecards of a round (scoring desk)", Permission: "golf.tournament_score.view",
		Response: TournamentScoreSummary{}, List: true, Query: []route.Param{{Name: "round", Type: "integer"}, {Name: "filter[status]"}},
		Handler: handle.Read(db, func(ctx context.Context, tx pgx.Tx, r *http.Request) (httpx.Page[TournamentScoreSummary], error) {
			tid, err := tournamentID(ctx, tx, r)
			if err != nil {
				return httpx.Page[TournamentScoreSummary]{}, err
			}
			return handle.Page(Scores(ctx, tx, tid, roundParam(r), httpx.ParseList(r).Filters["status"]))
		})})
	m.add(reg, route.Route{Method: http.MethodGet, Path: base + "/{id}/scores/{sid}", Summary: "Scorecard in tournament format (strokes received, net, points)",
		Permission: "golf.tournament_score.view", Response: TournamentScorecard{},
		Handler: handle.Read(db, func(ctx context.Context, tx pgx.Tx, r *http.Request) (TournamentScorecard, error) {
			tid, err := tournamentID(ctx, tx, r)
			if err != nil {
				return TournamentScorecard{}, err
			}
			sid, err := pathID(r, "sid")
			if err != nil {
				return TournamentScorecard{}, err
			}
			s, err := getScore(ctx, tx, sid)
			if err != nil || s.TournamentID != tid {
				return TournamentScorecard{}, errs.NotFound("tournament score")
			}
			return m.card(ctx, tx, s)
		})})
	m.add(reg, route.Route{Method: http.MethodPost, Path: base + "/{id}/scores", Summary: "Enter strokes per hole (scoring desk from paper cards, caddy tablet)",
		Permission: "golf.tournament_score.enter", Request: TournamentScoreInput{}, Response: TournamentScorecard{}, Status: http.StatusOK,
		Handler: handle.Write(db, http.StatusOK, func(ctx context.Context, tx pgx.Tx, r *http.Request, in TournamentScoreInput) (TournamentScorecard, error) {
			tid, err := handle.ID(r)
			if err != nil {
				return TournamentScorecard{}, err
			}
			p := handle.Property(ctx)
			who := scorerDesk
			if !can(ctx, "golf.tournament_score.validate", p) {
				who = scorerCaddy
			}
			return m.EnterScores(ctx, tx, p, tid, in, who, nil)
		})})
	scoreAction := func(path, summary, perm string, req any, fn func(ctx context.Context, tx pgx.Tx, p, tid, sid uuid.UUID, r *http.Request) (TournamentScorecard, error)) {
		m.add(reg, route.Route{Method: http.MethodPost, Path: base + "/{id}/scores/{sid}" + path, Summary: summary, Permission: perm, Request: req,
			Response: TournamentScorecard{}, Status: http.StatusOK, Handler: m.write(http.StatusOK, func(ctx context.Context, tx pgx.Tx, r *http.Request) (any, error) {
				tid, err := handle.ID(r)
				if err != nil {
					return nil, err
				}
				sid, err := pathID(r, "sid")
				if err != nil {
					return nil, err
				}
				return fn(ctx, tx, handle.Property(ctx), tid, sid, r)
			})})
	}
	scoreAction(":attest", "Attest the card (marker signature)", "golf.tournament_score.validate", TournamentAttestInput{},
		func(ctx context.Context, tx pgx.Tx, p, tid, sid uuid.UUID, r *http.Request) (TournamentScorecard, error) {
			var in TournamentAttestInput
			if err := httpx.Decode(r, &in); err != nil {
				return TournamentScorecard{}, err
			}
			return m.Attest(ctx, tx, p, tid, sid, in)
		})
	scoreAction(":validate", "Validate the card (P2 finalization; the round completes with its last card)", "golf.tournament_score.validate", nil,
		func(ctx context.Context, tx pgx.Tx, p, tid, sid uuid.UUID, r *http.Request) (TournamentScorecard, error) {
			return m.Validate(ctx, tx, p, tid, sid)
		})
	scoreAction(":correct", "Correct a validated card (reason, Score Audit History)", "golf.tournament_score.correct", TournamentCorrectionInput{},
		func(ctx context.Context, tx pgx.Tx, p, tid, sid uuid.UUID, r *http.Request) (TournamentScorecard, error) {
			var in TournamentCorrectionInput
			if err := httpx.Decode(r, &in); err != nil {
				return TournamentScorecard{}, err
			}
			return m.Correct(ctx, tx, p, tid, sid, in)
		})
	scoreAction(":set-status", "DQ / WD / NR or reinstate a player's round", "golf.tournament_score.validate", TournamentStatusInput{},
		func(ctx context.Context, tx pgx.Tx, p, tid, sid uuid.UUID, r *http.Request) (TournamentScorecard, error) {
			var in TournamentStatusInput
			if err := httpx.Decode(r, &in); err != nil {
				return TournamentScorecard{}, err
			}
			return m.SetStatus(ctx, tx, p, tid, sid, in)
		})
	m.add(reg, route.Route{Method: http.MethodGet, Path: base + "/{id}/flights/{fid}/scorecards", Summary: "Cards of a flight in tournament format (desk, the flight's caddy)",
		Permission: "golf.tournament_score.enter", Response: TournamentCaddyFlight{},
		Handler: handle.Read(db, func(ctx context.Context, tx pgx.Tx, r *http.Request) (TournamentCaddyFlight, error) {
			tid, err := handle.ID(r)
			if err != nil {
				return TournamentCaddyFlight{}, err
			}
			fid, err := pathID(r, "fid")
			if err != nil {
				return TournamentCaddyFlight{}, err
			}
			return m.FlightScorecards(ctx, tx, handle.Property(ctx), tid, fid)
		})})
	m.add(reg, route.Route{Method: http.MethodGet, Path: "/api/v1/golf/my-tournament-flights", Summary: "Caddy Tablet: today's tournament flights with the cards (offline cache)",
		Permission: "golf.tournament_score.enter", Response: TournamentCaddyFlight{}, List: true,
		Handler: handle.Read(db, func(ctx context.Context, tx pgx.Tx, r *http.Request) (httpx.Page[TournamentCaddyFlight], error) {
			return handle.Page(m.MyTournamentFlights(ctx, tx, handle.Property(ctx)))
		})})
	m.add(reg, route.Route{Method: http.MethodGet, Path: base + "/{id}/leaderboard", Summary: "Live leaderboard: gross / net / stableford per division with countback",
		Permission: "golf.tournament_leaderboard.view", Response: TournamentLeaderboard{}, Query: []route.Param{{Name: "category", Enum: []string{"gross", "net", "stableford"}},
			{Name: "division", Description: "overall or a division id"}},
		Handler: handle.Read(db, func(ctx context.Context, tx pgx.Tx, r *http.Request) (TournamentLeaderboard, error) {
			tid, err := handle.ID(r)
			if err != nil {
				return TournamentLeaderboard{}, err
			}
			q := r.URL.Query()
			return m.Leaderboard(ctx, tx, handle.Property(ctx), tid, q.Get("category"), q.Get("division"))
		})})
	m.add(reg, route.Route{Method: http.MethodGet, Path: base + "/stream", Summary: "Live tournament events: scores, draw, check-in, status (SSE)",
		Permission: "golf.tournament_leaderboard.view", RawContent: "text/event-stream", Query: []route.Param{{Name: "tournamentId"},
			{Name: "propertyId", Description: "Active property (EventSource cannot send X-Property-Id)"}},
		Handler: m.stream()})
	m.add(reg, route.Route{Method: http.MethodGet, Path: "/api/v1/golf/tournament-screen", Summary: "Leaderboard Screen feed (kiosk, read-only, rotating)",
		Permission: "golf.tournament_leaderboard.view", Response: TournamentScreenFeed{}, Query: []route.Param{{Name: "tournamentId"}},
		Handler: handle.Read(db, func(ctx context.Context, tx pgx.Tx, r *http.Request) (TournamentScreenFeed, error) {
			tid, err := handle.QueryUUID(r, "tournamentId")
			if err != nil {
				return TournamentScreenFeed{}, err
			}
			return m.Screen(ctx, tx, handle.Property(ctx), tid)
		})})
}

func (m *Module) stream() http.HandlerFunc {
	if m.Hub == nil {
		return func(w http.ResponseWriter, r *http.Request) {
			httpx.WriteError(w, r, errs.Unavailable("live updates are not available"))
		}
	}
	return m.Hub.Stream([]string{Topic}, "tournamentId")
}

// ── sponsors & prizes ─────────────────────────────────────────────────────

func (m *Module) registerAwards(reg *route.Registry) {
	db := m.DB
	m.add(reg, route.Route{Method: http.MethodPost, Path: base + "/{id}/sponsors/{sid}:invoice", Summary: "Invoice the sponsorship to the sponsor's account",
		Permission: "golf.tournament_sponsor.invoice", Request: TournamentSponsorInvoiceInput{}, Response: TournamentSponsor{}, Status: http.StatusOK, Idempotent: true,
		Handler: handle.Write(db, http.StatusOK, func(ctx context.Context, tx pgx.Tx, r *http.Request, in TournamentSponsorInvoiceInput) (TournamentSponsor, error) {
			tid, err := handle.ID(r)
			if err != nil {
				return TournamentSponsor{}, err
			}
			sid, err := pathID(r, "sid")
			if err != nil {
				return TournamentSponsor{}, err
			}
			return m.InvoiceSponsor(ctx, tx, handle.Property(ctx), tid, sid, in)
		})})
	m.add(reg, route.Route{Method: http.MethodPost, Path: base + "/{id}/prizes/{sid}:award", Summary: "Award a prize (Nearest to Pin, Longest Drive …)",
		Permission: "golf.tournament_prize.award", Request: TournamentAwardInput{}, Response: TournamentPrize{}, Status: http.StatusOK,
		Handler: handle.Write(db, http.StatusOK, func(ctx context.Context, tx pgx.Tx, r *http.Request, in TournamentAwardInput) (TournamentPrize, error) {
			tid, err := handle.ID(r)
			if err != nil {
				return TournamentPrize{}, err
			}
			pid, err := pathID(r, "sid")
			if err != nil {
				return TournamentPrize{}, err
			}
			return m.AwardPrize(ctx, tx, handle.Property(ctx), tid, pid, in)
		})})
	m.add(reg, route.Route{Method: http.MethodPost, Path: base + "/{id}/prizes/{sid}:hand-over", Summary: "Record the prize hand-over",
		Permission: "golf.tournament_prize.award", Request: TournamentHandOverInput{}, Response: TournamentPrize{}, Status: http.StatusOK,
		Handler: handle.Write(db, http.StatusOK, func(ctx context.Context, tx pgx.Tx, r *http.Request, in TournamentHandOverInput) (TournamentPrize, error) {
			tid, err := handle.ID(r)
			if err != nil {
				return TournamentPrize{}, err
			}
			pid, err := pathID(r, "sid")
			if err != nil {
				return TournamentPrize{}, err
			}
			return m.HandOverPrize(ctx, tx, handle.Property(ctx), tid, pid, in)
		})})
}

// ── Member App (FR-APP-P3-04) ─────────────────────────────────────────────

// MemberScoreInput enters the player's own strokes (Member App).
type TournamentMemberScoreInput struct {
	Round    int                     `json:"round,omitempty" doc:"Default: the current round"`
	Entries  []experience.ScoreEntry `json:"entries"`
	DeviceID string                  `json:"deviceId,omitempty"`
}

// MemberTournament is a tournament in the Member App with my registration.
type MemberTournament struct {
	PublicTournament
	MyRegistration *PublicTournamentRegistration `json:"myRegistration"`
}

func (m *Module) registerMember(reg *route.Registry) {
	db := m.DB
	me := func(rt route.Route) { crm.MeRoute(reg, "golf", "Member Portal", rt) }
	mine := func(ctx context.Context, q dbtx.Querier, property, tid, customer uuid.UUID) (*PublicTournamentRegistration, error) {
		var rid uuid.UUID
		err := q.QueryRow(ctx, `SELECT id FROM golf.tournament_registrations WHERE tournament_id = $1 AND customer_id = $2
			ORDER BY (status <> 'withdrawn') DESC, registered_at DESC LIMIT 1`, tid, customer).Scan(&rid)
		if dbtx.IsNoRows(err) {
			return nil, nil
		}
		if err != nil {
			return nil, err
		}
		p, err := m.publicRegistration(ctx, q, rid)
		return &p, err
	}
	me(route.Route{Method: http.MethodGet, Path: "/api/v1/member/golf/tournaments", Summary: "Tournaments (upcoming, live and recent) with my registration",
		Response: MemberTournament{}, List: true, Handler: handle.Read(db, func(ctx context.Context, tx pgx.Tx, r *http.Request) (httpx.Page[MemberTournament], error) {
			c, err := crm.Me(ctx, tx)
			if err != nil {
				return httpx.Page[MemberTournament]{}, err
			}
			list, err := m.PublicList(ctx, tx, c.PropertyID, true, "")
			if err != nil {
				return httpx.Page[MemberTournament]{}, err
			}
			out := make([]MemberTournament, 0, len(list))
			for _, t := range list {
				my, err := mine(ctx, tx, c.PropertyID, t.ID, c.ID)
				if err != nil {
					return httpx.Page[MemberTournament]{}, err
				}
				out = append(out, MemberTournament{PublicTournament: t, MyRegistration: my})
			}
			return handle.Page(out, nil)
		})})
	me(route.Route{Method: http.MethodGet, Path: "/api/v1/member/golf/tournaments/{id}", Summary: "Tournament with packages and my registration",
		Response: MemberTournament{}, Handler: handle.Read(db, func(ctx context.Context, tx pgx.Tx, r *http.Request) (MemberTournament, error) {
			c, err := crm.Me(ctx, tx)
			if err != nil {
				return MemberTournament{}, err
			}
			tid, err := handle.ID(r)
			if err != nil {
				return MemberTournament{}, err
			}
			t, err := m.PublicGet(ctx, tx, c.PropertyID, tid, true)
			if err != nil {
				return MemberTournament{}, err
			}
			my, err := mine(ctx, tx, c.PropertyID, tid, c.ID)
			return MemberTournament{PublicTournament: t, MyRegistration: my}, err
		})})
	me(route.Route{Method: http.MethodPost, Path: "/api/v1/member/golf/tournaments/{id}/registrations", Summary: "Register for a tournament and pay the fee",
		Request: TournamentMemberRegistrationInput{}, Response: PublicTournamentRegistration{}, Idempotent: true,
		Handler: handle.Write(db, http.StatusCreated, func(ctx context.Context, tx pgx.Tx, r *http.Request, in TournamentMemberRegistrationInput) (PublicTournamentRegistration, error) {
			c, err := crm.Me(ctx, tx)
			if err != nil {
				return PublicTournamentRegistration{}, err
			}
			tid, err := handle.ID(r)
			if err != nil {
				return PublicTournamentRegistration{}, err
			}
			if _, err := m.PublicGet(ctx, tx, c.PropertyID, tid, true); err != nil {
				return PublicTournamentRegistration{}, err
			}
			ctx = reqctx.WithProperty(ctx, c.PropertyID)
			d, err := m.RegisterMe(ctx, tx, c.PropertyID, tid, c, in)
			if err != nil {
				return PublicTournamentRegistration{}, err
			}
			return m.publicRegistration(ctx, tx, d.ID)
		})})
	me(route.Route{Method: http.MethodGet, Path: "/api/v1/member/golf/my-tournaments", Summary: "My Tournaments: registrations, payment, start and results",
		Response: MyTournamentRegistration{}, List: true, Handler: handle.Read(db, func(ctx context.Context, tx pgx.Tx, r *http.Request) (httpx.Page[MyTournamentRegistration], error) {
			c, err := crm.Me(ctx, tx)
			if err != nil {
				return httpx.Page[MyTournamentRegistration]{}, err
			}
			return handle.Page(m.MyRegistrations(ctx, tx, c.ID))
		})})
	me(route.Route{Method: http.MethodPost, Path: "/api/v1/member/golf/my-tournaments/{id}:withdraw", Summary: "Withdraw my registration (until the policy cut-off)",
		Request: TournamentReasonInput{}, Response: PublicTournamentRegistration{}, Status: http.StatusOK,
		Handler: handle.Write(db, http.StatusOK, func(ctx context.Context, tx pgx.Tx, r *http.Request, in TournamentReasonInput) (PublicTournamentRegistration, error) {
			c, err := crm.Me(ctx, tx)
			if err != nil {
				return PublicTournamentRegistration{}, err
			}
			rid, err := handle.ID(r)
			if err != nil {
				return PublicTournamentRegistration{}, err
			}
			p, err := MyRegistrationID(ctx, tx, c.ID, rid)
			if err != nil {
				return PublicTournamentRegistration{}, err
			}
			ctx = reqctx.WithProperty(ctx, p)
			return m.WithdrawSelf(ctx, tx, p, rid, in.Reason)
		})})
	me(route.Route{Method: http.MethodGet, Path: "/api/v1/member/golf/tournaments/{id}/start-sheet", Summary: "Published start sheet",
		Response: TournamentStartSheet{}, Query: []route.Param{{Name: "round", Type: "integer"}},
		Handler: handle.Read(db, func(ctx context.Context, tx pgx.Tx, r *http.Request) (TournamentStartSheet, error) {
			c, err := crm.Me(ctx, tx)
			if err != nil {
				return TournamentStartSheet{}, err
			}
			tid, err := handle.ID(r)
			if err != nil {
				return TournamentStartSheet{}, err
			}
			if _, err := m.PublicGet(ctx, tx, c.PropertyID, tid, true); err != nil {
				return TournamentStartSheet{}, err
			}
			return m.PublicStartSheet(ctx, tx, c.PropertyID, tid, roundParam(r))
		})})
	me(route.Route{Method: http.MethodGet, Path: "/api/v1/member/golf/tournaments/{id}/leaderboard", Summary: "Live leaderboard",
		Response: TournamentLeaderboard{}, Query: []route.Param{{Name: "category"}, {Name: "division"}},
		Handler: handle.Read(db, func(ctx context.Context, tx pgx.Tx, r *http.Request) (TournamentLeaderboard, error) {
			c, err := crm.Me(ctx, tx)
			if err != nil {
				return TournamentLeaderboard{}, err
			}
			tid, err := handle.ID(r)
			if err != nil {
				return TournamentLeaderboard{}, err
			}
			if _, err := m.PublicGet(ctx, tx, c.PropertyID, tid, true); err != nil {
				return TournamentLeaderboard{}, err
			}
			q := r.URL.Query()
			return m.MemberLeaderboard(ctx, tx, c.PropertyID, tid, c.ID, q.Get("category"), q.Get("division"))
		})})
	me(route.Route{Method: http.MethodGet, Path: "/api/v1/member/golf/tournaments/{id}/results", Summary: "Final results and awards",
		Response: TournamentResults{}, Handler: handle.Read(db, func(ctx context.Context, tx pgx.Tx, r *http.Request) (TournamentResults, error) {
			c, err := crm.Me(ctx, tx)
			if err != nil {
				return TournamentResults{}, err
			}
			tid, err := handle.ID(r)
			if err != nil {
				return TournamentResults{}, err
			}
			if _, err := m.PublicGet(ctx, tx, c.PropertyID, tid, true); err != nil {
				return TournamentResults{}, err
			}
			res, err := m.Results(ctx, tx, c.PropertyID, tid)
			if err != nil {
				return res, err
			}
			if res.Status != "completed" {
				return TournamentResults{}, errs.Conflict("not_final", "the results are not final yet")
			}
			return res, nil
		})})
	me(route.Route{Method: http.MethodGet, Path: "/api/v1/member/golf/tournaments/{id}/my-scorecards", Summary: "My cards of the tournament (tournament format)",
		Response: TournamentScorecard{}, List: true, Handler: handle.Read(db, func(ctx context.Context, tx pgx.Tx, r *http.Request) (httpx.Page[TournamentScorecard], error) {
			tid, err := handle.ID(r)
			if err != nil {
				return httpx.Page[TournamentScorecard]{}, err
			}
			return handle.Page(m.MyScorecards(ctx, tx, tid))
		})})
	me(route.Route{Method: http.MethodPost, Path: "/api/v1/member/golf/tournaments/{id}/scores", Summary: "Enter my own strokes (Member App scorecard)",
		Request: TournamentMemberScoreInput{}, Response: TournamentScorecard{}, Status: http.StatusOK,
		Handler: handle.Write(db, http.StatusOK, func(ctx context.Context, tx pgx.Tx, r *http.Request, in TournamentMemberScoreInput) (TournamentScorecard, error) {
			c, err := crm.Me(ctx, tx)
			if err != nil {
				return TournamentScorecard{}, err
			}
			tid, err := handle.ID(r)
			if err != nil {
				return TournamentScorecard{}, err
			}
			var rid uuid.UUID
			if err := tx.QueryRow(ctx, `SELECT id FROM golf.tournament_registrations WHERE tournament_id = $1 AND customer_id = $2
				AND status IN ('registered', 'checked_in')`, tid, c.ID).Scan(&rid); err != nil {
				if dbtx.IsNoRows(err) {
					return TournamentScorecard{}, errs.NotFound("registration")
				}
				return TournamentScorecard{}, err
			}
			ctx = reqctx.WithProperty(ctx, c.PropertyID)
			return m.EnterScores(ctx, tx, c.PropertyID, tid, TournamentScoreInput{RegistrationID: rid, Round: in.Round, Entries: in.Entries, DeviceID: in.DeviceID},
				scorerPlayer, &c.ID)
		})})
}

// ── website (FR-WEB-P3-03/06, K5) ─────────────────────────────────────────

func (m *Module) publicRead(fn func(ctx context.Context, tx pgx.Tx, pid uuid.UUID, r *http.Request) (any, error)) http.HandlerFunc {
	return publicLimiter.Wrap(func(w http.ResponseWriter, r *http.Request) {
		pid, err := uuid.Parse(r.URL.Query().Get("propertyId"))
		if err != nil {
			httpx.WriteError(w, r, errs.BadRequest("property_required", "propertyId is required"))
			return
		}
		ctx := reqctx.WithProperty(dbtx.WithScope(r.Context(), dbtx.Scope{PropertyIDs: []uuid.UUID{pid}}), pid)
		var out any
		err = m.DB.WithReadTx(ctx, func(tx pgx.Tx) error {
			var err error
			out, err = fn(ctx, tx, pid, r)
			return err
		})
		if err != nil {
			httpx.WriteError(w, r, err)
			return
		}
		httpx.JSON(w, http.StatusOK, out)
	})
}

func (m *Module) checkCaptcha(ctx context.Context, token string) error {
	if m.Integrations == nil {
		return nil
	}
	cv, err := m.Integrations.Captcha(ctx)
	if errors.Is(err, integration.ErrNotConfigured) {
		return nil
	}
	if err != nil {
		return err
	}
	ip := ""
	if meta := reqctx.GetMeta(ctx); meta != nil {
		ip = meta.IP
	}
	ok, err := cv.VerifyCaptcha(ctx, token, ip)
	if err != nil || !ok {
		return errs.Forbidden("CAPTCHA verification failed; please try again")
	}
	return nil
}

func (m *Module) registerPublic(reg *route.Registry) {
	pub := func(rt route.Route) { crm.PublicRoute(reg, "golf", rt) }
	pub(route.Route{Method: http.MethodGet, Path: "/api/v1/public/tournaments", Summary: "Public tournaments (website): schedule, fees, places left",
		Response: PublicTournament{}, List: true, Query: []route.Param{{Name: "propertyId", Required: true}, {Name: "status"}},
		Handler: m.publicRead(func(ctx context.Context, tx pgx.Tx, pid uuid.UUID, r *http.Request) (any, error) {
			return handle.Page(m.PublicList(ctx, tx, pid, false, r.URL.Query().Get("status")))
		})})
	pub(route.Route{Method: http.MethodGet, Path: "/api/v1/public/tournaments/{id}", Summary: "Public tournament with packages and sponsors",
		Response: PublicTournament{}, Query: []route.Param{{Name: "propertyId", Required: true}},
		Handler: m.publicRead(func(ctx context.Context, tx pgx.Tx, pid uuid.UUID, r *http.Request) (any, error) {
			tid, err := handle.ID(r)
			if err != nil {
				return nil, err
			}
			return m.PublicGet(ctx, tx, pid, tid, false)
		})})
	pub(route.Route{Method: http.MethodGet, Path: "/api/v1/public/tournaments/{id}/leaderboard", Summary: "Public leaderboard (opt-in; names with consent only)",
		Response: TournamentLeaderboard{}, Query: []route.Param{{Name: "propertyId", Required: true}, {Name: "category"}, {Name: "division"}},
		Handler: m.publicRead(func(ctx context.Context, tx pgx.Tx, pid uuid.UUID, r *http.Request) (any, error) {
			tid, err := handle.ID(r)
			if err != nil {
				return nil, err
			}
			q := r.URL.Query()
			return m.PublicLeaderboard(ctx, tx, pid, tid, q.Get("category"), q.Get("division"))
		})})
	pub(route.Route{Method: http.MethodPost, Path: "/api/v1/public/tournaments/{id}/registrations", Summary: "Register for a public tournament and pay online (CAPTCHA, rate limited)",
		Request: PublicTournamentRegistrationInput{}, Response: PublicTournamentRegistration{}, Status: http.StatusCreated,
		Handler: crm.PublicWrite(m.DB, http.StatusCreated, func(ctx context.Context, tx pgx.Tx, r *http.Request, pid uuid.UUID, c crm.Customer,
			in PublicTournamentRegistrationInput) (PublicTournamentRegistration, error) {
			if err := m.checkCaptcha(ctx, in.CaptchaToken); err != nil {
				return PublicTournamentRegistration{}, err
			}
			tid, err := handle.ID(r)
			if err != nil {
				return PublicTournamentRegistration{}, err
			}
			if _, err := m.PublicGet(ctx, tx, pid, tid, false); err != nil {
				return PublicTournamentRegistration{}, err
			}
			return m.RegisterPublic(ctx, tx, pid, tid, c, in)
		})})
	pub(route.Route{Method: http.MethodGet, Path: "/api/v1/public/tournament-registrations/{token}", Summary: "My registration behind its secure link",
		Response: PublicTournamentRegistration{}, Query: []route.Param{{Name: "propertyId", Required: true}},
		Handler: m.publicRead(func(ctx context.Context, tx pgx.Tx, pid uuid.UUID, r *http.Request) (any, error) {
			rid, err := RegistrationByToken(ctx, tx, pid, chi.URLParam(r, "token"))
			if err != nil {
				return nil, err
			}
			return m.publicRegistration(ctx, tx, rid)
		})})
	pub(route.Route{Method: http.MethodPost, Path: "/api/v1/public/tournament-registrations/{token}:withdraw", Summary: "Withdraw from the secure link (Tournament Policies)",
		Request: TournamentReasonInput{}, Response: PublicTournamentRegistration{}, Status: http.StatusOK, Query: []route.Param{{Name: "propertyId", Required: true}},
		Handler: publicLimiter.Wrap(func(w http.ResponseWriter, r *http.Request) {
			var in TournamentReasonInput
			if err := httpx.Decode(r, &in); err != nil {
				httpx.WriteError(w, r, err)
				return
			}
			pid, err := uuid.Parse(r.URL.Query().Get("propertyId"))
			if err != nil {
				httpx.WriteError(w, r, errs.BadRequest("property_required", "propertyId is required"))
				return
			}
			ctx := crm.PublicCtx(r.Context(), pid)
			var out PublicTournamentRegistration
			err = m.DB.WithTx(ctx, func(tx pgx.Tx) error {
				rid, err := RegistrationByToken(ctx, tx, pid, chi.URLParam(r, "token"))
				if err != nil {
					return err
				}
				reason := strings.TrimSpace(in.Reason)
				if reason == "" {
					reason = "withdrawn by the player (website)"
				}
				out, err = m.WithdrawSelf(ctx, tx, pid, rid, reason)
				return err
			})
			if err != nil {
				httpx.WriteError(w, r, err)
				return
			}
			httpx.JSON(w, http.StatusOK, out)
		})})
}
