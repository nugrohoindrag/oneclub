package accounting

// FR-ACC-01 / FR-TRS-05: the OneClub chart of accounts template for clubs
// and hospitality (Indonesian club practice, English name + Bahasa
// Indonesia name), loaded per property: the accounts are instance level
// (one legal entity), the book of the property records the template and
// the cut-over date. Loading also seeds the Tax Configuration codes and the
// default posting rules generated from the Accounting Export components.

import (
	"context"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"oneclub/internal/kernel/errs"
	"oneclub/internal/kernel/id"
)

// TemplateCode is the OneClub club & hospitality chart template.
const TemplateCode = "oneclub_club_hospitality"

type tplAccount struct {
	Code, Parent, Name, NameID, Type, Subtype, CashFlow string
	Header                                              bool
	Credit                                              bool // contra asset / normal credit
}

func h(code, parent, name, nameID, typ string) tplAccount {
	return tplAccount{Code: code, Parent: parent, Name: name, NameID: nameID, Type: typ, Subtype: "header", Header: true, CashFlow: "non_cash"}
}

func a(code, parent, name, nameID, typ, subtype, cf string) tplAccount {
	return tplAccount{Code: code, Parent: parent, Name: name, NameID: nameID, Type: typ, Subtype: subtype, CashFlow: cf}
}

// Template is the chart of accounts template.
var Template = []tplAccount{
	h("1000", "", "Assets", "Aset", "asset"),
	h("1100", "1000", "Current Assets", "Aset Lancar", "asset"),
	a("1111", "1100", "Cash on Hand", "Kas Besar", "asset", "cash", "cash"),
	a("1112", "1100", "Petty Cash", "Kas Kecil", "asset", "cash", "cash"),
	a("1121", "1100", "Bank BCA – Operating", "Bank BCA Operasional", "asset", "bank", "cash"),
	a("1122", "1100", "Bank Mandiri – Collection", "Bank Mandiri Penerimaan", "asset", "bank", "cash"),
	a("1131", "1100", "Card (EDC) Clearing", "Kliring Kartu (EDC)", "asset", "clearing", "cash"),
	a("1132", "1100", "Payment Gateway Clearing", "Kliring Payment Gateway", "asset", "clearing", "cash"),
	a("1133", "1100", "Cash in Transit", "Kas dalam Perjalanan", "asset", "clearing", "cash"),
	a("1141", "1100", "Guest Ledger", "Piutang Tamu (Guest Ledger)", "asset", "receivable", "operating"),
	a("1142", "1100", "Accounts Receivable – Invoiced", "Piutang Usaha – Invoice", "asset", "receivable", "operating"),
	a("1143", "1100", "City Ledger – Unbilled", "Piutang Anggota & Korporat Belum Ditagih", "asset", "receivable", "operating"),
	a("1144", "1100", "Other Receivables", "Piutang Lain-lain", "asset", "receivable", "operating"),
	{Code: "1149", Parent: "1100", Name: "Allowance for Doubtful Accounts", NameID: "Cadangan Kerugian Piutang", Type: "asset", Subtype: "contra_asset", CashFlow: "operating", Credit: true},
	a("1151", "1100", "Inventory", "Persediaan", "asset", "inventory", "operating"),
	a("1152", "1100", "Inventory – Pro Shop", "Persediaan Pro Shop", "asset", "inventory", "operating"),
	a("1159", "1100", "Work in Process", "Barang Dalam Proses", "asset", "inventory", "operating"),
	a("1171", "1100", "VAT Input (PPN Masukan)", "PPN Masukan", "asset", "tax", "operating"),
	a("1172", "1100", "Prepaid Income Tax (PPh 23)", "PPh 23 Dibayar di Muka", "asset", "tax", "operating"),
	a("1181", "1100", "Prepaid Expenses", "Biaya Dibayar di Muka", "asset", "prepaid", "operating"),
	a("1199", "1100", "Posting Clearing", "Kliring Posting", "asset", "clearing", "operating"),
	h("1200", "1000", "Non-current Assets", "Aset Tidak Lancar", "asset"),
	a("1210", "1200", "Land", "Tanah", "asset", "fixed_asset", "investing"),
	a("1220", "1200", "Buildings & Golf Course", "Bangunan & Lapangan Golf", "asset", "fixed_asset", "investing"),
	a("1230", "1200", "Golf Carts & Vehicles", "Golf Cart & Kendaraan", "asset", "fixed_asset", "investing"),
	a("1240", "1200", "Equipment & Fixtures", "Peralatan & Perlengkapan", "asset", "fixed_asset", "investing"),
	{Code: "1290", Parent: "1200", Name: "Accumulated Depreciation", NameID: "Akumulasi Penyusutan", Type: "asset", Subtype: "accumulated_depreciation", CashFlow: "operating", Credit: true},
	h("2000", "", "Liabilities", "Kewajiban", "liability"),
	h("2100", "2000", "Current Liabilities", "Kewajiban Lancar", "liability"),
	a("2111", "2100", "Accounts Payable", "Utang Usaha", "liability", "payable", "operating"),
	a("2112", "2100", "Consignment Payable", "Utang Konsinyasi", "liability", "payable", "operating"),
	a("2113", "2100", "Goods Received Not Invoiced (GRNI)", "Barang Diterima Belum Ditagih", "liability", "accrued", "operating"),
	a("2121", "2100", "Service Charge Payable", "Utang Service Charge", "liability", "service_charge", "operating"),
	a("2131", "2100", "VAT Output (PPN Keluaran)", "PPN Keluaran", "liability", "tax", "operating"),
	a("2132", "2100", "Local Tax Payable (PBJT / PB1)", "Utang Pajak Daerah PBJT/PB1", "liability", "tax", "operating"),
	a("2133", "2100", "Withholding Tax Payable (PPh 23 / 4(2))", "Utang PPh 23 / 4(2)", "liability", "tax", "operating"),
	a("2141", "2100", "Customer Deposits", "Uang Muka / Deposit Pelanggan", "liability", "deposit", "operating"),
	a("2151", "2100", "Deferred Revenue – Vouchers", "Pendapatan Diterima di Muka – Voucher", "liability", "deferred", "operating"),
	a("2152", "2100", "Deferred Revenue – Prepaid", "Pendapatan Diterima di Muka – Prepaid", "liability", "deferred", "operating"),
	a("2153", "2100", "Deferred Revenue – Annual Fees", "Pendapatan Diterima di Muka – Iuran Tahunan", "liability", "deferred", "operating"),
	a("2154", "2100", "Deferred Revenue – Packages", "Pendapatan Diterima di Muka – Paket", "liability", "deferred", "operating"),
	a("2159", "2100", "Deferred Revenue Clearing", "Kliring Pendapatan Ditangguhkan", "liability", "clearing", "operating"),
	a("2161", "2100", "Loyalty Points Liability", "Kewajiban Poin Loyalty", "liability", "deferred", "operating"),
	a("2171", "2100", "Caddy Fee Liability", "Titipan Caddy Fee", "liability", "accrued", "operating"),
	a("2172", "2100", "Instructor Fees Payable", "Utang Honor Instruktur", "liability", "accrued", "operating"),
	a("2173", "2100", "Sales Commission Payable", "Utang Komisi Penjualan", "liability", "accrued", "operating"),
	a("2181", "2100", "Accrued Expenses", "Biaya Masih Harus Dibayar", "liability", "accrued", "operating"),
	a("2199", "2100", "Suspense", "Akun Suspense", "liability", "suspense", "operating"),
	h("3000", "", "Equity", "Ekuitas", "equity"),
	a("3100", "3000", "Share Capital", "Modal Disetor", "equity", "equity", "financing"),
	a("3200", "3000", "Retained Earnings", "Laba Ditahan", "equity", "retained_earnings", "financing"),
	a("3900", "3000", "Opening Balance Equity", "Ekuitas Saldo Awal", "equity", "equity", "financing"),
	h("4000", "", "Revenue", "Pendapatan", "revenue"),
	h("4100", "4000", "Golf Revenue", "Pendapatan Golf", "revenue"),
	a("4110", "4100", "Green Fee", "Green Fee", "revenue", "revenue", "operating"),
	a("4120", "4100", "Golf Cart (Buggy) Fee", "Golf Cart / Buggy Fee", "revenue", "revenue", "operating"),
	a("4130", "4100", "Driving Range", "Driving Range", "revenue", "revenue", "operating"),
	a("4140", "4100", "Hole-in-One Insurance", "Asuransi Hole-in-One", "revenue", "revenue", "operating"),
	a("4150", "4100", "Golf Other Revenue", "Pendapatan Golf Lainnya", "revenue", "revenue", "operating"),
	a("4160", "4100", "Tournaments & Sponsorship", "Turnamen & Sponsorship", "revenue", "revenue", "operating"),
	h("4200", "4000", "Sport Club Revenue", "Pendapatan Sport Club", "revenue"),
	a("4210", "4200", "Facility Entry", "Tiket Masuk Fasilitas", "revenue", "revenue", "operating"),
	a("4220", "4200", "Court Rental", "Sewa Lapangan", "revenue", "revenue", "operating"),
	a("4230", "4200", "Classes & Coaching", "Kelas & Pelatihan", "revenue", "revenue", "operating"),
	a("4240", "4200", "Lockers", "Loker", "revenue", "revenue", "operating"),
	h("4300", "4000", "Rooms & Venue Revenue", "Pendapatan Kamar & Venue", "revenue"),
	a("4310", "4300", "Bungalow", "Bungalow", "revenue", "revenue", "operating"),
	a("4320", "4300", "VIP Suite", "VIP Suite", "revenue", "revenue", "operating"),
	a("4330", "4300", "Meeting Rooms & Equipment", "Ruang Meeting & Peralatan", "revenue", "revenue", "operating"),
	h("4400", "4000", "Food & Beverage Revenue", "Pendapatan F&B", "revenue"),
	a("4410", "4400", "Food & Beverage", "Makanan & Minuman", "revenue", "revenue", "operating"),
	a("4420", "4400", "Banquet & Events", "Banquet & Event", "revenue", "revenue", "operating"),
	a("4430", "4400", "Venue Rental", "Sewa Venue", "revenue", "revenue", "operating"),
	h("4500", "4000", "Retail Revenue", "Pendapatan Ritel", "revenue"),
	a("4510", "4500", "Pro Shop", "Pro Shop", "revenue", "revenue", "operating"),
	h("4600", "4000", "Membership Revenue", "Pendapatan Keanggotaan", "revenue"),
	a("4610", "4600", "Membership Joining Fee", "Uang Pangkal Keanggotaan", "revenue", "revenue", "operating"),
	a("4620", "4600", "Annual Membership Fee", "Iuran Tahunan", "revenue", "revenue", "operating"),
	a("4630", "4600", "Membership Service Fees", "Biaya Layanan Keanggotaan", "revenue", "revenue", "operating"),
	a("4700", "4000", "Package Revenue", "Pendapatan Paket", "revenue", "revenue", "operating"),
	h("4800", "4000", "Other Operating Revenue", "Pendapatan Operasional Lain", "revenue"),
	a("4810", "4800", "Voucher & Points Breakage", "Breakage Voucher & Poin", "revenue", "revenue", "operating"),
	a("4820", "4800", "Cancellation & Penalty Fees", "Denda & Biaya Pembatalan", "revenue", "revenue", "operating"),
	a("4830", "4800", "Caddy Settlement Deductions", "Potongan Settlement Caddy", "revenue", "revenue", "operating"),
	a("4890", "4800", "Other Revenue", "Pendapatan Lain-lain", "revenue", "revenue", "operating"),
	h("4900", "4000", "Revenue Deductions", "Potongan Penjualan", "revenue"),
	a("4910", "4900", "Discounts & Promotions", "Diskon & Promosi", "revenue", "contra_revenue", "operating"),
	a("4920", "4900", "Sales Returns & Allowances", "Retur & Pengurangan Penjualan", "revenue", "contra_revenue", "operating"),
	h("5000", "", "Cost of Sales", "Harga Pokok Penjualan", "cogs"),
	a("5110", "5000", "Cost of Food & Beverage", "HPP Makanan & Minuman", "cogs", "cogs", "operating"),
	a("5120", "5000", "Cost of Pro Shop Sales", "HPP Pro Shop", "cogs", "cogs", "operating"),
	a("5130", "5000", "Cost of Consignment Sales", "HPP Konsinyasi", "cogs", "cogs", "operating"),
	a("5140", "5000", "Cost of Banquet", "HPP Banquet", "cogs", "cogs", "operating"),
	a("5180", "5000", "Waste & Spoilage", "Waste / Spoilage", "cogs", "cogs", "operating"),
	a("5190", "5000", "Inventory Variance", "Selisih Persediaan", "cogs", "cogs", "operating"),
	h("6000", "", "Operating Expenses", "Beban Operasional", "expense"),
	a("6110", "6000", "Salaries & Wages", "Gaji & Upah", "expense", "expense", "operating"),
	a("6120", "6000", "Instructor Fees", "Honor Instruktur", "expense", "expense", "operating"),
	a("6130", "6000", "Sales Commissions", "Komisi Penjualan", "expense", "expense", "operating"),
	a("6210", "6000", "Course Maintenance", "Perawatan Lapangan", "expense", "expense", "operating"),
	a("6220", "6000", "Golf Cart Maintenance", "Perawatan Golf Cart", "expense", "expense", "operating"),
	a("6310", "6000", "Utilities", "Listrik & Air", "expense", "expense", "operating"),
	a("6410", "6000", "Loyalty Programme", "Program Loyalty", "expense", "expense", "operating"),
	a("6510", "6000", "Depreciation", "Beban Penyusutan", "expense", "depreciation", "operating"),
	a("6610", "6000", "Bad Debt Expense", "Beban Piutang Tak Tertagih", "expense", "expense", "operating"),
	a("6620", "6000", "Bank & Payment Gateway Charges", "Biaya Bank & Payment Gateway", "expense", "expense", "operating"),
	a("6630", "6000", "Office Supplies", "Perlengkapan Kantor", "expense", "expense", "operating"),
	a("6640", "6000", "General Expenses", "Beban Umum", "expense", "expense", "operating"),
	a("6910", "6000", "Cash Over / Short", "Selisih Kas", "expense", "expense", "operating"),
	a("6990", "6000", "Rounding", "Pembulatan", "expense", "expense", "operating"),
	h("7000", "", "Other Income & Expenses", "Pendapatan & Beban Lain-lain", "expense"),
	a("7110", "7000", "Interest Income", "Pendapatan Bunga", "revenue", "other_income", "operating"),
	a("7210", "7000", "Interest Expense", "Beban Bunga", "expense", "other_expense", "operating"),
}

// normalBalance returns the normal side of an account type.
func normalBalance(typ string, credit bool) string {
	if credit {
		return "credit"
	}
	switch typ {
	case "liability", "equity", "revenue":
		return "credit"
	}
	return "debit"
}

// COALoadResult reports a template load.
type COALoadResult struct {
	Created      int            `json:"created"`
	Existing     int            `json:"existing"`
	TaxCodes     int            `json:"taxCodes"`
	PostingRules int            `json:"postingRules"`
	Book         AccountingBook `json:"book"`
}

// AccountingBook is the book of a property.
type AccountingBook struct {
	PropertyID      uuid.UUID  `json:"propertyId" db:"property_id"`
	Template        string     `json:"template" db:"template"`
	Status          string     `json:"status" db:"status" enum:"setup,live"`
	CutOverDate     string     `json:"cutOverDate" db:"cut_over_date" doc:"First day posted by OneClub; opening balances are dated the day before"`
	Currency        string     `json:"currency" db:"currency"`
	ExportStoppedAt *time.Time `json:"exportStoppedAt" db:"export_stopped_at" doc:"Accounting Export cut-over sign-off (FR-TRS-04)"`
	SignoffNote     *string    `json:"signoffNote" db:"signoff_note"`
	LoadedAt        time.Time  `json:"loadedAt" db:"loaded_at"`
}

const bookSelect = `SELECT property_id, template, status, to_char(cut_over_date, 'YYYY-MM-DD') AS cut_over_date, currency, export_stopped_at, signoff_note,
	loaded_at FROM accounting.books`

// GetBook returns the book of a property.
func GetBook(ctx context.Context, q interface {
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
}, property uuid.UUID) (AccountingBook, error) {
	rows, err := q.Query(ctx, bookSelect+` WHERE property_id = $1`, property)
	if err != nil {
		return AccountingBook{}, err
	}
	b, err := pgx.CollectExactlyOneRow(rows, pgx.RowToStructByNameLax[AccountingBook])
	if err != nil {
		if err == pgx.ErrNoRows {
			return AccountingBook{}, errs.NotFound("book (load the chart of accounts template first)")
		}
		return AccountingBook{}, err
	}
	return b, nil
}

// LoadTemplate creates the template accounts that do not exist yet, opens
// the book of the property with its cut-over date and seeds the tax codes
// and default posting rules (idempotent).
func LoadTemplate(ctx context.Context, tx pgx.Tx, property uuid.UUID, cutOver time.Time) (COALoadResult, error) {
	res, err := SeedChart(ctx, tx)
	if err != nil {
		return res, err
	}
	uid := actorPtr(ctx)
	cur := currency(ctx, tx)
	if _, err := tx.Exec(ctx, `INSERT INTO accounting.books (property_id, template, cut_over_date, currency, loaded_by) VALUES ($1,$2,$3,$4,$5)
		ON CONFLICT (property_id) DO NOTHING`, property, TemplateCode, dateOnly(cutOver), cur, uid); err != nil {
		return res, err
	}
	res.Book, err = GetBook(ctx, tx, property)
	return res, err
}

// SeedChart creates the template accounts, tax codes and default posting
// rules that do not exist yet (instance level, idempotent).
func SeedChart(ctx context.Context, tx pgx.Tx) (COALoadResult, error) {
	var res COALoadResult
	ids := map[string]uuid.UUID{}
	rows, err := tx.Query(ctx, `SELECT code, id FROM accounting.accounts`)
	if err != nil {
		return res, err
	}
	for rows.Next() {
		var code string
		var aid uuid.UUID
		if err := rows.Scan(&code, &aid); err != nil {
			rows.Close()
			return res, err
		}
		ids[code] = aid
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return res, err
	}
	uid := actorPtr(ctx)
	for _, t := range Template {
		if _, ok := ids[t.Code]; ok {
			res.Existing++
			continue
		}
		aid := id.New()
		var parent *uuid.UUID
		if t.Parent != "" {
			if p, ok := ids[t.Parent]; ok {
				parent = &p
			}
		}
		if _, err := tx.Exec(ctx, `INSERT INTO accounting.accounts (id, code, name, name_id, account_type, subtype, parent_id, is_posting, normal_balance,
			cash_flow, created_by, updated_by) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$11) ON CONFLICT (code) DO NOTHING`,
			aid, t.Code, t.Name, t.NameID, t.Type, t.Subtype, parent, !t.Header, normalBalance(t.Type, t.Credit), t.CashFlow, uid); err != nil {
			return res, err
		}
		ids[t.Code] = aid
		res.Created++
	}
	n, err := seedTaxCodes(ctx, tx, ids)
	if err != nil {
		return res, err
	}
	res.TaxCodes = n
	res.PostingRules, err = GenerateDefaultRules(ctx, tx)
	return res, err
}

// default Tax Configuration (FR-REV-04, PRD P4 §16 #7): PPN 11%, PBJT
// (PB1) 10%, service charge 10%, PPN input, PPh 23 2%, PPh 4(2) 10%.
var defaultTaxCodes = []struct {
	Code, Name, Kind, Rate, Account, TxCode string
	Rules                                   []string
}{
	{"PPN", "PPN Keluaran 11%", "output_vat", "11", "2131", "01", []string{"PPN", "VAT", "P2VAT", "PPN11"}},
	{"PB1", "PBJT / PB1 10%", "local_tax", "10", "2132", "", []string{"PB1", "PBJT", "P2PB1"}},
	{"SVC", "Service Charge 10%", "service_charge", "10", "2121", "", []string{"SVC", "SERVICE", "P2SVC", "SC"}},
	{"PPN-IN", "PPN Masukan 11%", "input_vat", "11", "1171", "", []string{}},
	{"PPH23", "PPh 23 (jasa) 2%", "withholding", "2", "2133", "", []string{}},
	{"PPH42", "PPh 4(2) (sewa) 10%", "withholding", "10", "2133", "", []string{}},
}

func seedTaxCodes(ctx context.Context, tx pgx.Tx, ids map[string]uuid.UUID) (int, error) {
	n := 0
	for _, t := range defaultTaxCodes {
		acct, ok := ids[t.Account]
		if !ok {
			continue
		}
		tag, err := tx.Exec(ctx, `INSERT INTO accounting.tax_codes (id, code, name, kind, rate_percent, account_id, rule_codes, transaction_code)
			VALUES ($1,$2,$3,$4,$5::numeric,$6,$7,$8) ON CONFLICT (code) DO NOTHING`, id.New(), t.Code, t.Name, t.Kind, t.Rate, acct, t.Rules, nz(t.TxCode))
		if err != nil {
			return n, err
		}
		n += int(tag.RowsAffected())
	}
	return n, nil
}
