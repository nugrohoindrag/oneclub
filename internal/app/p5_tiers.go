package app

// Member tier classes & classification (PRD P5 EP-18, product owner request
// "on member add tier class and with classification and CRUD"): the tier
// master routes, manual classification overrides (approval document,
// expiry job), the tier badge lookups, the Members by Tier / Tier Movement
// reports and the tier F&B discount at the POS. Called from p5_crm.go.
//
// CRM sits above commercial (Technical Doc §4.2): commercial/pos reads the
// tier benefit of a customer through the hook wired here.

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/shopspring/decimal"

	"oneclub/internal/commercial/pos"
	"oneclub/internal/crm/loyalty"
	"oneclub/internal/kernel/clock"
	"oneclub/internal/kernel/dbtx"
	"oneclub/internal/kernel/id"
	"oneclub/internal/kernel/route"
	"oneclub/internal/platform/calendar"
	"oneclub/internal/platform/catalog"
	"oneclub/internal/platform/numbering"
	"oneclub/internal/platform/provision"
)

func p5TiersContributions() []catalog.Contribution {
	return []catalog.Contribution{{Permissions: loyalty.TierPermissions(), RolePermissions: loyalty.TierRolePermissions()}}
}

func p5TiersDocumentTypes() []provision.DocumentType {
	return []provision.DocumentType{loyalty.TierOverrideDocumentType}
}

// buildP5Tiers wires the routes, the override approval, the expiry job and
// the POS tier discount hook.
func (a *App) buildP5Tiers(reg *route.Registry) {
	lm := a.Engage.Loyalty
	lm.RegisterTiers(reg)
	a.Approvals.RegisterDocumentType(loyalty.TierOverrideDocumentType, lm.TierOverrideDecision)
	lm.RegisterTierJobs(a.Registrar, a.Instance.Location)
	pos.SetTierBenefit(posTierBenefit)
}

// posTierBenefit is the tier F&B discount of a customer for the POS.
func posTierBenefit(ctx context.Context, q dbtx.Querier, customer uuid.UUID) (*pos.TierBenefit, error) {
	b, err := loyalty.BenefitsOf(ctx, q, customer)
	if err != nil || b.TierID == nil || b.TierCode == nil || b.TierName == nil {
		return nil, err
	}
	pct, err := decimal.NewFromString(b.FnbDiscountPercent)
	if err != nil {
		return nil, nil
	}
	return &pos.TierBenefit{Code: *b.TierCode, Name: *b.TierName, FnbDiscountPercent: pct}, nil
}

// demoP5Tiers seeds the tier classes (badges of PRD P5 §16 #14: Silver,
// Gold, Platinum) and a few demo members in each tier with their spend of
// the last months, one of them classified manually (idempotent).
func demoP5Tiers(ctx context.Context, tx pgx.Tx, property uuid.UUID) error {
	for code, badge := range map[string][2]string{"SILVER": {"#9CA3AF", "military_tech"}, "GOLD": {"#C9A227", "star"}, "PLATINUM": {"#4B5563", "diamond"}} {
		if _, err := tx.Exec(ctx, `UPDATE crm.loyalty_tiers SET color = $3, icon = $4 WHERE property_id = $1 AND code = $2 AND icon = 'workspace_premium'`,
			property, code, badge[0], badge[1]); err != nil {
			return err
		}
	}
	tiers := map[string]uuid.UUID{}
	rows, err := tx.Query(ctx, `SELECT code, id FROM crm.loyalty_tiers WHERE property_id = $1 AND code IN ('SILVER', 'GOLD', 'PLATINUM')`, property)
	if err != nil {
		return err
	}
	for rows.Next() {
		var c string
		var tid uuid.UUID
		if err := rows.Scan(&c, &tid); err != nil {
			rows.Close()
			return err
		}
		tiers[c] = tid
	}
	rows.Close()
	if len(tiers) < 3 {
		return nil
	}
	now := clock.Now().In(calendar.Location(ctx, tx))
	today := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, time.UTC)
	// Demo members of their own (the portal demo member D0001 stays out of
	// the tiers): customer, member, active membership of the D0001 type.
	var typeID, packageID *uuid.UUID
	if err := tx.QueryRow(ctx, `SELECT ms.type_id, ms.package_id FROM membership.memberships ms JOIN membership.members m ON m.id = ms.member_id
		WHERE m.property_id = $1 AND m.code = $2 ORDER BY ms.starts_on DESC LIMIT 1`, property, DemoMemberCode).Scan(&typeID, &packageID); err != nil {
		if dbtx.IsNoRows(err) {
			return nil
		}
		return err
	}
	plan := []struct {
		name  string
		tier  string
		spend int64
	}{{"Ratna Wijaya", "PLATINUM", 82_500_000}, {"Bambang Susilo", "PLATINUM", 76_000_000}, {"Dewi Kartika", "GOLD", 31_000_000},
		{"Agus Pratama", "GOLD", 27_500_000}, {"Sinta Maharani", "GOLD", 25_400_000}, {"Yusuf Hakim", "SILVER", 8_200_000},
		{"Lestari Putri", "SILVER", 3_100_000}, {"Joko Santoso", "SILVER", 0}}
	customers := make([]uuid.UUID, 0, len(plan))
	for i, p := range plan {
		code := fmt.Sprintf("DT%03d", i+1)
		cust := id.New()
		if err := tx.QueryRow(ctx, `INSERT INTO crm.customers (id, property_id, code, name, email, phone, consent_at, consent_channel)
			VALUES ($1,$2,$3,$4,$5,$6,now(),'back_office') ON CONFLICT (property_id, code) DO UPDATE SET name = EXCLUDED.name RETURNING id`, cust, property,
			"CUS-TIER-"+code, p.name, "tier."+strings.ToLower(code)+"@demo.oneclub.id", fmt.Sprintf("+62811200%04d", i+1)).Scan(&cust); err != nil {
			return err
		}
		member := id.New()
		if err := tx.QueryRow(ctx, `INSERT INTO membership.members (id, property_id, code, name, email, status, customer_id, joined_on)
			VALUES ($1,$2,$3,$4,$5,'active',$6,$7) ON CONFLICT (property_id, code) DO UPDATE SET name = EXCLUDED.name RETURNING id`, member, property, code,
			p.name, "tier."+strings.ToLower(code)+"@demo.oneclub.id", cust, today.AddDate(-2, 0, 0)).Scan(&member); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `INSERT INTO membership.memberships (id, property_id, member_id, type_id, package_id, role, starts_on, ends_on, status, activated_at)
			SELECT $1,$2,$3,$4,$5,'principal',$6,$7,'active',now() WHERE NOT EXISTS (SELECT 1 FROM membership.memberships WHERE member_id = $3)`,
			id.New(), property, member, typeID, packageID, today.AddDate(0, -2, 0), today.AddDate(0, 10, 0)); err != nil {
			return err
		}
		customers = append(customers, cust)
	}
	for i, cust := range customers {
		p := plan[i]
		var exists bool
		if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM crm.loyalty_accounts WHERE customer_id = $1)`, cust).Scan(&exists); err != nil {
			return err
		}
		if exists {
			continue
		}
		num, err := numbering.Next(ctx, tx, property, "LOY", now)
		if err != nil {
			return err
		}
		aid := id.New()
		points := p.spend / 10000
		if _, err := tx.Exec(ctx, `INSERT INTO crm.loyalty_accounts (id, property_id, number, customer_id, tier_id, enrolled_via, balance, lifetime_points,
			tier_since, tier_evaluated_at) VALUES ($1,$2,$3,$4,$5,'migration',$6,$6,$7,now())`, aid, property, num, cust, tiers[p.tier], points, today); err != nil {
			return err
		}
		if points > 0 {
			lid := id.New()
			if _, err := tx.Exec(ctx, `INSERT INTO crm.loyalty_ledger (id, property_id, account_id, kind, points, balance_after, source_type, amount, currency,
				idempotency_key, expires_on, description, occurred_at) VALUES ($1,$2,$3,'earned',$4,$4,'migration',$5::numeric,(SELECT currency FROM platform.instance),'demo-tier-spend',$6,
				'Spend of the last months (demo)', now() - interval '30 days')`, lid, property, aid, points, fmt.Sprint(p.spend), today.AddDate(1, 0, -30)); err != nil {
				return err
			}
			if _, err := tx.Exec(ctx, `INSERT INTO crm.loyalty_lots (ledger_id, property_id, account_id, points, remaining, expires_on) VALUES ($1,$2,$3,$4,$4,$5)`,
				lid, property, aid, points, today.AddDate(1, 0, -30)); err != nil {
				return err
			}
		}
		if _, err := tx.Exec(ctx, `INSERT INTO crm.loyalty_tier_history (id, property_id, account_id, to_tier_id, reason, points_basis, spend_basis, source)
			VALUES ($1,$2,$3,$4,'opening classification (demo)',$5,$6::numeric,'auto')`, id.New(), property, aid, tiers[p.tier], points, fmt.Sprint(p.spend)); err != nil {
			return err
		}
		if i == 5 { // manual classification: a Silver spender kept in Gold for six months
			onum, err := numbering.Next(ctx, tx, property, "TOV", now)
			if err != nil {
				return err
			}
			oid := id.New()
			if _, err := tx.Exec(ctx, `INSERT INTO crm.loyalty_tier_overrides (id, property_id, number, account_id, customer_id, tier_id, previous_tier_id, reason,
				valid_until, status, applied_at) VALUES ($1,$2,$3,$4,$5,$6,$7,'Board member courtesy (demo)',$8,'active',now())`, oid, property, onum, aid, cust,
				tiers["GOLD"], tiers["SILVER"], today.AddDate(0, 6, 0)); err != nil {
				return err
			}
			if _, err := tx.Exec(ctx, `UPDATE crm.loyalty_accounts SET tier_id = $2, tier_locked = true, tier_source = 'manual', tier_override_id = $3 WHERE id = $1`,
				aid, tiers["GOLD"], oid); err != nil {
				return err
			}
			if _, err := tx.Exec(ctx, `INSERT INTO crm.loyalty_tier_history (id, property_id, account_id, from_tier_id, to_tier_id, reason, source, override_id)
				VALUES ($1,$2,$3,$4,$5,'manual override: Board member courtesy (demo)','manual',$6)`, id.New(), property, aid, tiers["SILVER"], tiers["GOLD"],
				oid); err != nil {
				return err
			}
		}
	}
	return nil
}
