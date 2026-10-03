package reporting

import (
	"context"
	"strconv"
	"strings"

	"github.com/jackc/pgx/v5"
)

func collect(rows pgx.Rows) ([]map[string]any, error) {
	defer rows.Close()
	fds := rows.FieldDescriptions()
	var out []map[string]any
	for rows.Next() {
		vals, err := rows.Values()
		if err != nil {
			return nil, err
		}
		m := make(map[string]any, len(vals))
		for i, fd := range fds {
			m[fd.Name] = vals[i]
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

func limitClause(limit int) string {
	if limit <= 0 {
		return ""
	}
	return " LIMIT " + strconv.Itoa(limit)
}

// UserAccessReport is FR-REP-05: user, role, property, last login.
var UserAccessReport = &Report{
	Code: "platform.user_access", Name: "User Access Report", Module: "platform", Permission: "reporting.user_access.view",
	Description: "Who has which role at which property, MFA status and last login.",
	Columns: []Column{
		{Key: "fullName", Label: "User", Type: "string"}, {Key: "email", Label: "E-mail", Type: "string"},
		{Key: "userStatus", Label: "Status", Type: "string"}, {Key: "roleName", Label: "Role", Type: "string"},
		{Key: "propertyName", Label: "Property", Type: "string"}, {Key: "mfaEnabled", Label: "MFA", Type: "boolean"},
		{Key: "lastLoginAt", Label: "Last Login", Type: "datetime"},
	},
	Params: []Param{{Key: "status", Label: "Status", Type: "enum", Enum: []string{"active", "inactive"}}, {Key: "q", Label: "Search", Type: "string"}},
	Query: func(ctx context.Context, tx pgx.Tx, p map[string]string, limit int) ([]map[string]any, error) {
		where := []string{"(property_id IS NULL OR platform.rls_allowed(property_id))"}
		args := []any{}
		if v := p["status"]; v != "" {
			args = append(args, v)
			where = append(where, "user_status = $"+strconv.Itoa(len(args)))
		}
		if v := p["q"]; v != "" {
			args = append(args, "%"+v+"%")
			where = append(where, "(full_name ILIKE $"+strconv.Itoa(len(args))+" OR email ILIKE $"+strconv.Itoa(len(args))+")")
		}
		rows, err := tx.Query(ctx, `SELECT full_name AS "fullName", email, user_status AS "userStatus", role_name AS "roleName",
			property_name AS "propertyName", mfa_enabled AS "mfaEnabled", last_login_at AS "lastLoginAt"
			FROM reporting.user_access WHERE `+strings.Join(where, " AND ")+` ORDER BY full_name, role_name`+limitClause(limit), args...)
		if err != nil {
			return nil, err
		}
		return collect(rows)
	},
}

// VenueDirectoryReport is the vertical slice report (PRD §8): venues and
// their activation status, read from the replica.
var VenueDirectoryReport = &Report{
	Code: "platform.venue_directory", Name: "Venue Directory Report", Module: "platform", Permission: "reporting.venue_directory.view",
	Description: "Venues per property with activation status.",
	Columns: []Column{
		{Key: "propertyName", Label: "Property", Type: "string"}, {Key: "code", Label: "Code", Type: "string"},
		{Key: "name", Label: "Venue", Type: "string"}, {Key: "venueType", Label: "Venue Type", Type: "string"},
		{Key: "status", Label: "Status", Type: "string"}, {Key: "updatedAt", Label: "Updated", Type: "datetime"},
	},
	Params: []Param{{Key: "status", Label: "Status", Type: "enum", Enum: []string{"pending", "active", "inactive", "rejected"}}},
	Query: func(ctx context.Context, tx pgx.Tx, p map[string]string, limit int) ([]map[string]any, error) {
		args := []any{}
		where := "true"
		if v := p["status"]; v != "" {
			args = append(args, v)
			where = "status = $1"
		}
		rows, err := tx.Query(ctx, `SELECT venue_id::text AS "venueId", property_name AS "propertyName", code, name, venue_type AS "venueType",
			status, updated_at AS "updatedAt" FROM reporting.venue_directory WHERE `+where+` ORDER BY property_name, name`+limitClause(limit), args...)
		if err != nil {
			return nil, err
		}
		return collect(rows)
	},
}
