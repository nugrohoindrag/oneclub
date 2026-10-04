package membership

// Data migration from Rhapsody (PRD P1 EP-18, FR-MIG-04/05): members,
// memberships (principal and family) and cards are upserted by their legacy
// reference so a dry run can be repeated without duplicates.

import (
	"context"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"oneclub/internal/crm"
	"oneclub/internal/kernel/dbtx"
	"oneclub/internal/kernel/errs"
	"oneclub/internal/kernel/id"
	"oneclub/internal/kernel/secret"
)

// MemberImport is one migrated membership.
type MemberImport struct {
	MemberNo          string
	CustomerID        uuid.UUID
	TypeCode          string
	StartsOn          time.Time
	EndsOn            *time.Time
	Status            string // active | expired | inactive
	CardNumber        string // Rhapsody card number (kept as legacy number)
	PrincipalMemberNo string // family members: the principal's member number
	Relationship      string // spouse | child | …
	CorporateID       *uuid.UUID
	LegacyRef         string
}

// ImportMember upserts the member, its membership and card. Principals must
// be imported before their family members.
func ImportMember(ctx context.Context, tx pgx.Tx, property uuid.UUID, in MemberImport) (uuid.UUID, error) {
	in.MemberNo = strings.TrimSpace(in.MemberNo)
	if in.MemberNo == "" {
		return uuid.Nil, errs.Validation("member_no_required", "member number is required")
	}
	switch in.Status {
	case "active", "expired", "inactive":
	default:
		return uuid.Nil, errs.Validation("invalid_status", "status must be active, expired or inactive")
	}
	var typeID uuid.UUID
	var typeName string
	if err := tx.QueryRow(ctx, `SELECT id, name FROM membership.types WHERE property_id = $1 AND upper(code) = upper($2) AND archived_at IS NULL`,
		property, in.TypeCode).Scan(&typeID, &typeName); err != nil {
		if dbtx.IsNoRows(err) {
			return uuid.Nil, errs.Validation("unknown_type", "membership type "+in.TypeCode+" is not configured")
		}
		return uuid.Nil, err
	}
	c, err := crm.GetCustomer(ctx, tx, in.CustomerID)
	if err != nil {
		return uuid.Nil, err
	}
	memberStatus := in.Status
	var mid uuid.UUID
	err = tx.QueryRow(ctx, `INSERT INTO membership.members (id, property_id, code, name, email, phone, membership_type, customer_id, joined_on, status, legacy_ref)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9::date,$10,$11)
		ON CONFLICT (property_id, code) DO UPDATE SET name = EXCLUDED.name, email = EXCLUDED.email, phone = EXCLUDED.phone,
		  membership_type = EXCLUDED.membership_type, customer_id = EXCLUDED.customer_id, status = EXCLUDED.status, legacy_ref = EXCLUDED.legacy_ref
		RETURNING id`, id.New(), property, in.MemberNo, c.Name, nullStr(c.Email), nullStr(c.Phone), typeName, c.ID,
		in.StartsOn.Format("2006-01-02"), memberStatus, nullStr(in.LegacyRef)).Scan(&mid)
	if err != nil {
		return uuid.Nil, err
	}
	role, rel := "principal", (*string)(nil)
	var principal *uuid.UUID
	if pn := strings.TrimSpace(in.PrincipalMemberNo); pn != "" && !strings.EqualFold(pn, in.MemberNo) {
		var p uuid.UUID
		if err := tx.QueryRow(ctx, `SELECT ms.id FROM membership.memberships ms JOIN membership.members m ON m.id = ms.member_id
			WHERE m.property_id = $1 AND m.code = $2 AND ms.role = 'principal' ORDER BY ms.starts_on DESC LIMIT 1`, property, pn).Scan(&p); err != nil {
			if dbtx.IsNoRows(err) {
				return uuid.Nil, errs.Validation("principal_not_found", "principal member "+pn+" is not imported yet")
			}
			return uuid.Nil, err
		}
		principal, role = &p, "family"
		r := strings.ToLower(strings.TrimSpace(in.Relationship))
		if r == "" {
			r = "other"
		}
		rel = &r
		if in.CorporateID != nil {
			role = "nominee"
		}
	}
	var ends *string
	if in.EndsOn != nil {
		e := in.EndsOn.Format("2006-01-02")
		ends = &e
	}
	ref := in.LegacyRef
	if ref == "" {
		ref = in.MemberNo
	}
	var msID uuid.UUID
	err = tx.QueryRow(ctx, `SELECT id FROM membership.memberships WHERE member_id = $1 AND legacy_ref = $2`, mid, ref).Scan(&msID)
	switch {
	case dbtx.IsNoRows(err):
		msID = id.New()
		var activated *time.Time
		if in.Status == "active" {
			now := time.Now()
			activated = &now
		}
		if _, err := tx.Exec(ctx, `INSERT INTO membership.memberships (id, property_id, member_id, type_id, principal_id, role, relationship, corporate_account_id,
			starts_on, ends_on, status, activated_at, legacy_ref) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9::date,$10::date,$11,$12,$13)`,
			msID, property, mid, typeID, principal, role, rel, in.CorporateID, in.StartsOn.Format("2006-01-02"), ends, in.Status, activated, ref); err != nil {
			return uuid.Nil, err
		}
		if err := history(ctx, tx, property, mid, &msID, "migrated", "", in.Status, map[string]any{"source": "rhapsody", "memberNo": in.MemberNo}); err != nil {
			return uuid.Nil, err
		}
	case err != nil:
		return uuid.Nil, err
	default:
		if _, err := tx.Exec(ctx, `UPDATE membership.memberships SET type_id = $2, principal_id = $3, role = $4, relationship = $5, corporate_account_id = $6,
			starts_on = $7::date, ends_on = $8::date, status = $9 WHERE id = $1`, msID, typeID, principal, role, rel, in.CorporateID,
			in.StartsOn.Format("2006-01-02"), ends, in.Status); err != nil {
			return uuid.Nil, err
		}
	}
	if cn := strings.TrimSpace(in.CardNumber); cn != "" {
		if _, err := tx.Exec(ctx, `INSERT INTO membership.cards (id, property_id, member_id, membership_id, card_number, legacy_number, card_type, qr_token, valid_until, status)
			SELECT $1,$2,$3,$4,$5,$5,'physical',$6,$7::date,$8 WHERE NOT EXISTS
			  (SELECT 1 FROM membership.cards WHERE property_id = $2 AND (card_number = $5 OR legacy_number = $5))`,
			id.New(), property, mid, msID, cn, "mc_"+secret.RandomToken(24), ends, map[bool]string{true: "active", false: "inactive"}[in.Status == "active"]); err != nil {
			return uuid.Nil, err
		}
	}
	return mid, nil
}
