package app

// Trial dataset: finance. At go-live Finance opens the OneClub book
// (chart of accounts template, cut-over at the first simulated day), the
// periods, the cash & bank accounts and posts the opening balances taken
// from the previous ledger (cash, bank, fixed assets, open supplier
// invoices, equity), then signs the transition off. The sandbox e-Faktur
// (Coretax) adapter is enabled for the trial (P4 gap: provisioning did not
// enable it). During the history the members get their monthly statement
// and settle their accounts, the open supplier invoices are paid; at the
// end a bank statement is reconciled and the first complete month closed.

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"

	"oneclub/internal/kernel/id"
	"oneclub/internal/platform/integration"
)

func init() {
	registerTrialSeeder(trialSeeder{Name: "finance", Order: 1, Setup: trialFinanceSetup})
}

const trialAccountant = "accountant@demo.oneclub.id"

// account returns the id of a ledger account code (cached).
func (t *Trial) account(code string) string {
	if v := t.Ref("account:" + code); v != "" {
		return v
	}
	for _, a := range t.As(trialFinance).Items("/api/v1/accounting/accounts?limit=1000") {
		t.SetRef("account:"+a.S("code"), a.S("id"))
	}
	if t.Ref("account:"+code) == "" {
		t.fail("ledger account %s not found", code)
	}
	return t.Ref("account:" + code)
}

func trialFinanceSetup(ctx context.Context, t *Trial) error {
	// sandbox e-Faktur for the trial (trialRawSQL: like the e-Meterai of the
	// demo configuration; integrations are a Platform Admin task)
	if err := t.App.DB.WithTx(ctx, func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `INSERT INTO platform.integrations (id, code, adapter, capability, name, enabled, mode)
			VALUES ($1, 'mock-efaktur', 'mock-efaktur', $2, 'Mock e-Faktur Coretax (Sandbox)', true, 'sandbox') ON CONFLICT (code) DO NOTHING`,
			id.New(), integration.CapTaxInvoice)
		return err
	}); err != nil {
		return err
	}
	fin := t.As(trialFinance)
	fin.Post("/api/v1/accounting/book:load-template", J{"cutOverDate": t.Start.Format(time.DateOnly)})
	for y := t.Start.AddDate(0, 0, -1).Year(); y <= t.Today.AddDate(0, 0, t.Ahead).Year(); y++ {
		fin.Post("/api/v1/accounting/periods:generate", J{"year": y})
	}
	for _, b := range []J{
		{"code": "BCA-OPS", "name": "BCA Operasional", "kind": "bank", "bankName": "BCA", "accountNo": "5270-123456", "accountHolder": "PT Modern Golf",
			"glAccountId": t.account("1121"), "statementFormat": "bca_csv"},
		{"code": "KAS-FO", "name": "Kas Front Office", "kind": "cash", "glAccountId": t.account("1111"), "custodian": "Front Office Cashier"},
		{"code": "KAS-KECIL", "name": "Kas Kecil", "kind": "petty_cash", "glAccountId": t.account("1112"), "floatAmount": "5000000", "custodian": "Finance"},
	} {
		fin.Post("/api/v1/accounting/bank-accounts", b)
	}
	suppliers := map[string]J{}
	for _, s := range fin.Items("/api/v1/procurement/suppliers?limit=100") {
		suppliers[s.S("code")] = s
	}
	opening := t.Start.AddDate(0, 0, -1)
	ap := func(code, doc string, amount int64, days int) J {
		s := suppliers[code]
		if s == nil {
			t.fail("supplier %s not seeded", code)
		}
		return J{"accountCode": "2111", "credit": fmt.Sprint(amount), "partnerType": "supplier", "partnerId": s.S("id"), "partnerName": s.S("name"),
			"documentNo": doc, "documentDate": opening.AddDate(0, 0, -days).Format(time.DateOnly), "dueDate": opening.AddDate(0, 0, 30-days).Format(time.DateOnly),
			"description": "Open supplier invoice at go-live"}
	}
	lines := []J{
		{"accountCode": "1111", "debit": "150000000", "description": "Cash on hand"},
		{"accountCode": "1112", "debit": "5000000", "description": "Petty cash"},
		{"accountCode": "1121", "debit": "4250000000", "description": "BCA operational account"},
		{"accountCode": "1181", "debit": "180000000", "description": "Prepaid insurance and maintenance contracts"},
		{"accountCode": "1240", "debit": "38500000000", "description": "Clubhouse, course improvements, golf carts and equipment"},
		{"accountCode": "1290", "credit": "11200000000", "description": "Accumulated depreciation"},
		ap("SUP-SINAR", "SS/INV/0614", 32500000, 12),
		ap("SUP-PRIMA", "PGS-2026-0388", 54100000, 20),
		ap("SUP-TEKNIK", "JTM-118", 8750000, 5),
		{"accountCode": "3100", "credit": "25000000000", "description": "Share capital"},
	}
	var debit, credit int64
	for _, l := range lines {
		var v int64
		if d := l.S("debit"); d != "" {
			_, _ = fmt.Sscan(d, &v)
			debit += v
		} else {
			_, _ = fmt.Sscan(l.S("credit"), &v)
			credit += v
		}
	}
	lines = append(lines, J{"accountCode": "3900", "credit": fmt.Sprint(debit - credit), "description": "Opening balance equity"})
	ob := fin.Post("/api/v1/accounting/opening-balances", J{"balanceDate": opening.Format(time.DateOnly),
		"description": "Saldo awal dari Excel Finance per " + opening.Format("2 Jan 2006"), "lines": lines})
	fin.Post("/api/v1/accounting/opening-balances/"+ob.S("id")+":post", J{})
	fin.Post("/api/v1/accounting/book:sign-off", J{"note": "Go-live on OneClub: opening balances reconciled with Excel Finance"})
	return nil
}
