package app

// Trial dataset: receivables and the month-end close. Members receive their
// monthly statement on the 1st and settle their account by transfer in the
// following weeks; companies pay their invoices when due (a few stay
// overdue for the AR aging); the e-Faktur of the invoices with PPN are
// uploaded every afternoon (mock-efaktur sandbox). Early in the month the
// accountant imports the bank statement of the operating account (BCA) of
// the previous month, auto-matches it to the books and books the bank
// charges; on the 25th the finance manager soft-closes and closes the
// previous month (late enough for the banquet events quoted in that month:
// re-pricing an event voids its package charges, which a closed period
// refuses).

import (
	"context"
	"fmt"
	"hash/fnv"
	"strings"
	"time"

	"github.com/shopspring/decimal"
)

func init() {
	registerTrialSeeder(trialSeeder{Name: "closing", Order: 85, Day: trialClosingDay})
}

func trialClosingDay(_ context.Context, t *Trial, day time.Time) error {
	ds := day.Format(time.DateOnly)
	fin := t.As(trialFinance)
	prevEnd := time.Date(day.Year(), day.Month(), 1, 0, 0, 0, 0, time.UTC).AddDate(0, 0, -1)
	prevStart := time.Date(prevEnd.Year(), prevEnd.Month(), 1, 0, 0, 0, 0, time.UTC)
	inHistory := !prevEnd.Before(t.Start)
	if day.Day() == 1 && inHistory {
		t.At(day, "03:00")
		fin.Post("/api/v1/billing/member-statements:generate", J{"period": prevEnd.Format("2006-01")})
	}
	// companies pay their invoices when due (one in ten is late)
	t.At(day, "10:00")
	for _, st := range []string{"issued", "partially_paid", "overdue"} {
		for _, inv := range fin.Items("/api/v1/billing/invoices?limit=200&filter[status]=" + st) {
			due := inv.S("dueDate")
			if due == "" || due > ds || t.Rand("late:"+inv.S("id")).IntN(10) == 0 && due > day.AddDate(0, 0, -20).Format(time.DateOnly) {
				continue
			}
			fin.Post("/api/v1/billing/invoices/"+inv.S("id")+":pay", J{"methodType": "bank_transfer", "reference": "TRF-" + inv.S("number")})
		}
	}
	// members settle their account balance once a month (8th–20th)
	if d := day.Day(); d >= 8 && d <= 20 {
		cashier := t.As(trialCashier)
		for _, a := range cashier.Items("/api/v1/billing/customer-accounts?limit=500&filter[accountType]=member") {
			h := fnv.New32a()
			_, _ = h.Write([]byte(a.S("id") + day.Format("2006-01")))
			bal, err := decimal.NewFromString(a.S("balance"))
			if err != nil || !bal.IsPositive() || int(h.Sum32()%13)+8 != d {
				continue
			}
			cashier.Post("/api/v1/billing/payments", J{"accountId": a.S("id"), "amount": bal.String(), "methodType": "bank_transfer",
				"reference": "TRF-" + a.S("number"), "payerName": a.S("number")})
		}
	}
	// e-Faktur upload of the output tax invoices (mock-efaktur)
	t.At(day, "16:00")
	for _, ti := range fin.Items("/api/v1/accounting/tax-invoices?limit=200&filter[direction]=output&filter[status]=draft") {
		fin.Post("/api/v1/accounting/tax-invoices/"+ti.S("id")+":upload", J{})
	}
	if day.Day() == 3 && inHistory {
		t.At(day, "09:30")
		trialBankReconciliation(t, maxTime(prevStart, t.Start), prevEnd)
	}
	if day.Day() == 25 && inHistory {
		t.At(day, "15:00")
		trialClosePeriod(t, prevStart)
	}
	return nil
}

func maxTime(a, b time.Time) time.Time {
	if a.After(b) {
		return a
	}
	return b
}

// trialBankReconciliation imports the BCA statement of a month (every
// movement of the books plus the monthly administration fee), auto-matches
// it, books the fee and completes the reconciliation.
func trialBankReconciliation(t *Trial, from, to time.Time) {
	acc := t.As(trialAccountant)
	bank := ""
	for _, b := range acc.Items("/api/v1/accounting/bank-accounts?limit=50") {
		if b.S("code") == "BCA-OPS" {
			bank = b.S("id")
		}
	}
	gl := acc.Get("/api/v1/accounting/general-ledger?accountId=" + t.account("1121") + "&from=" + from.Format(time.DateOnly) + "&to=" + to.Format(time.DateOnly))
	clean := strings.NewReplacer(",", " ", "\"", "", "\n", " ")
	var b strings.Builder
	b.WriteString("Rekening Koran BCA " + to.Format("2006-01") + "\ndate,description,reference,amount,balance\n")
	closing, err := decimal.NewFromString(gl.S("closingBalance"))
	t.check(err)
	fee := decimal.NewFromInt(15000)
	for _, l := range gl.A("lines") {
		amt := dec0(l.S("debit")).Sub(dec0(l.S("credit")))
		if amt.IsZero() {
			continue
		}
		desc := strings.ToUpper(clean.Replace(l.S("description")))
		if l.S("journalType") == "opening" {
			desc = "SALDO AWAL"
		}
		fmt.Fprintf(&b, "%s,%s,%s,%s,\n", l.S("journalDate"), desc, l.S("journalNumber"), amt.String())
	}
	stmt := closing.Sub(fee)
	fmt.Fprintf(&b, "%s,BIAYA ADMINISTRASI,,-%s,%s\n", to.Format(time.DateOnly), fee.String(), stmt.String())
	acc.Post("/api/v1/accounting/bank-transactions:import", J{"bankAccountId": bank, "content": b.String(), "format": "generic_csv",
		"filename": "bca-" + to.Format("2006-01") + ".csv"})
	rec := acc.Post("/api/v1/accounting/bank-reconciliations", J{"bankAccountId": bank, "statementDate": to.Format(time.DateOnly), "statementBalance": stmt.String()})
	rid := rec.S("id")
	acc.Post("/api/v1/accounting/bank-reconciliations/"+rid+":auto-match", J{})
	for _, x := range acc.Get("/api/v1/accounting/bank-reconciliations/" + rid).A("unmatchedTransactions") {
		if x.S("description") == "BIAYA ADMINISTRASI" {
			acc.Post("/api/v1/accounting/bank-reconciliations/"+rid+":resolve", J{"bankTransactionId": x.S("id"), "accountId": t.account("6620"),
				"description": "Bank administration fee " + to.Format("2006-01")})
		}
	}
	acc.Post("/api/v1/accounting/bank-reconciliations/"+rid+":complete", J{})
}

func dec0(s string) decimal.Decimal {
	d, err := decimal.NewFromString(s)
	if err != nil {
		return decimal.Zero
	}
	return d
}

// trialClosePeriod soft-closes and closes the financial period of a month.
func trialClosePeriod(t *Trial, month time.Time) {
	fin := t.As(trialFinance)
	for _, p := range fin.Items(fmt.Sprintf("/api/v1/accounting/periods?year=%d&limit=50", month.Year())) {
		if p.S("startDate") > month.Format(time.DateOnly) || p.S("endDate") < month.Format(time.DateOnly) || p.S("status") == "closed" {
			continue
		}
		if p.S("status") == "open" {
			fin.Post("/api/v1/accounting/periods/"+p.S("id")+":soft-close", J{})
		}
		fin.Post("/api/v1/accounting/periods/"+p.S("id")+":close", J{})
	}
}
