package app

// Demo access (Staff App /demo): the presenter of a demo picks a business
// (Golf, Sport Club, Bungalow, VIP Suite, Wedding / MICE / Banquet) and a
// demo user, and lands on that user's login page with the e-mail filled in.
// The page lists the active demo accounts of `oneclub seed-demo` (name and
// e-mail only — never a password); it answers 404 on a production instance
// or an instance without the demo seed, so it never shows on a club's
// live system.

import (
	"net/http"

	"github.com/jackc/pgx/v5"

	"oneclub/internal/kernel/config"
	"oneclub/internal/kernel/dbtx"
	"oneclub/internal/kernel/errs"
	"oneclub/internal/kernel/httpx"
	"oneclub/internal/kernel/route"
	"oneclub/internal/platform/handle"
)

// DemoEmailDomain is the e-mail domain of the seeded demo accounts.
const DemoEmailDomain = "@demo.oneclub.id"

// DemoAccount is an active demo user offered on the demo access page.
type DemoAccount struct {
	Email string `json:"email" db:"email"`
	Name  string `json:"name" db:"full_name"`
}

// DemoAccess lists the demo accounts of the instance.
type DemoAccess struct {
	Accounts []DemoAccount `json:"accounts"`
}

func registerDemoAccess(reg *route.Registry, cfg *config.Config, db *dbtx.DB) {
	reg.Add(route.Route{Method: http.MethodGet, Path: "/api/v1/public/demo-access", Module: "platform", Tag: "Public", Auth: route.AuthPublic,
		Summary:  "Demo accounts for the demo access page (404 on production or without the demo seed)",
		Response: DemoAccess{},
		Handler: func(w http.ResponseWriter, r *http.Request) {
			if cfg == nil || cfg.Env == "production" || db == nil {
				httpx.WriteError(w, r, errs.NotFound("demo access"))
				return
			}
			ctx := dbtx.System(r.Context())
			var out DemoAccess
			err := db.WithTx(ctx, func(tx pgx.Tx) error {
				var err error
				out.Accounts, err = handle.List[DemoAccount](tx.Query(ctx, `SELECT email, full_name FROM platform.users
					WHERE email LIKE '%' || $1 AND status = 'active' ORDER BY email`, DemoEmailDomain))
				return err
			})
			if err == nil && len(out.Accounts) == 0 {
				err = errs.NotFound("demo access")
			}
			if err != nil {
				httpx.WriteError(w, r, err)
				return
			}
			httpx.JSON(w, http.StatusOK, out)
		}})
}
