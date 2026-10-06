package payroll

// Statutory exports (PRD P5 FR-TAX-HR-04, FR-INT-P5-03): the monthly
// e-Bupot PPh 21 file (bukti pemotongan bulanan, BPMP), the BPJS Kesehatan
// and Ketenagakerjaan contribution files (SIPP / EDABU upload columns) and
// the annual 1721-A1 per employee, as CSV from the approved, posted and
// paid runs. They contain identity numbers: payroll export permission only;
// every download is audited.

import (
	"bytes"
	"context"
	"encoding/csv"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/shopspring/decimal"

	"oneclub/internal/hris"
	"oneclub/internal/kernel/httpx"
	"oneclub/internal/kernel/route"
	"oneclub/internal/platform/audit"
	"oneclub/internal/platform/handle"
)

func (m *Module) registerExports(reg *route.Registry) {
	tag := "HRIS Payroll"
	for _, e := range []struct{ path, summary, param string }{
		{"e-bupot", "e-Bupot PPh 21 monthly file (BPMP) of a period", "periodCode"},
		{"bpjs-kesehatan", "BPJS Kesehatan contribution file of a period", "periodCode"},
		{"bpjs-ketenagakerjaan", "BPJS Ketenagakerjaan contribution file of a period (JHT, JP, JKK, JKM)", "periodCode"},
		{"1721-a1", "Annual 1721-A1 data per employee of a tax year", "year"},
	} {
		add(reg, tag, route.Route{Method: http.MethodGet, Path: "/api/v1/hris/payroll-exports/" + e.path, Summary: e.summary, Permission: PermRunExport,
			RawContent: "text/csv", Query: []route.Param{{Name: e.param, Required: true}, {Name: "employeeId"}}, Handler: m.exportHTTP(e.path)})
	}
}

// exportHTTP serves a statutory export and audits it.
func (m *Module) exportHTTP(kind string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ctx := r.Context()
		var out []byte
		var name string
		err := m.DB.WithTx(ctx, func(tx pgx.Tx) error {
			property := handle.Property(ctx)
			var rows int
			switch kind {
			case "1721-a1":
				year, perr := strconv.Atoi(r.URL.Query().Get("year"))
				if perr != nil || year < 2000 || year > 2100 {
					return handle.Invalid("year", "invalid", "a tax year, e.g. 2026")
				}
				emp, err := handle.QueryUUID(r, "employeeId")
				if err != nil {
					return err
				}
				out, rows, err = Export1721A1(ctx, tx, property, year, emp)
				if err != nil {
					return err
				}
				name = "1721-A1-" + strconv.Itoa(year)
			default:
				month, err := parsePeriod("periodCode", r.URL.Query().Get("periodCode"))
				if err != nil {
					return err
				}
				switch kind {
				case "e-bupot":
					out, rows, err = ExportEBupot(ctx, tx, property, month)
				case "bpjs-kesehatan":
					out, rows, err = exportBPJS(ctx, tx, property, month, true)
				default:
					out, rows, err = exportBPJS(ctx, tx, property, month, false)
				}
				if err != nil {
					return err
				}
				name = kind + "-" + periodCode(month)
			}
			return audit.Record(ctx, tx, audit.Entry{Module: hris.Module, Action: audit.ActionExport, EntityType: "hris.payroll_export", EntityLabel: name,
				PropertyID: &property, Metadata: map[string]any{"kind": kind, "rows": rows}})
		})
		if err != nil {
			httpx.WriteError(w, r, err)
			return
		}
		w.Header().Set("Content-Type", "text/csv; charset=utf-8")
		w.Header().Set("Cache-Control", "private, no-store")
		w.Header().Set("Content-Disposition", `attachment; filename="`+name+`.csv"`)
		_, _ = w.Write(out)
	}
}

func writeCSV(rows [][]string) ([]byte, error) {
	var b bytes.Buffer
	w := csv.NewWriter(&b)
	if err := w.WriteAll(rows); err != nil {
		return nil, err
	}
	return b.Bytes(), w.Error()
}

func digits(s *string) string {
	if s == nil {
		return ""
	}
	return strings.Map(func(r rune) rune {
		if r >= '0' && r <= '9' {
			return r
		}
		return -1
	}, *s)
}

// ExportEBupot is the e-Bupot 21 monthly file: one row per employee and
// tax period with the TER rate of the month (Kode Objek Pajak 21-100-01
// pegawai tetap; the December row carries the annual calculation).
func ExportEBupot(ctx context.Context, tx pgx.Tx, property uuid.UUID, month time.Time) ([]byte, int, error) {
	rows, err := tx.Query(ctx, `SELECT s.employee_no, s.full_name, min(s.npwp), min(s.nik), coalesce(min(s.ptkp_status), 'TK/0'), max(s.ter_rate),
		sum(s.taxable_gross), sum(s.pph21), sum(s.pph21_final), sum(s.severance), max(r.payment_date), bool_or(s.tax_method = 'annual')
		FROM hris.payroll_slips s JOIN hris.payroll_runs r ON r.id = s.run_id
		WHERE r.property_id = $1 AND r.period_code = $2 AND r.status IN ('approved', 'posted', 'paid')
		GROUP BY s.employee_id, s.employee_no, s.full_name ORDER BY s.employee_no`, property, periodCode(month))
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	out := [][]string{{"Masa Pajak", "Tahun Pajak", "Status", "NPWP/NIK", "Nama", "Kode Objek Pajak", "Status PTKP", "Penghasilan Bruto", "Tarif TER (%)",
		"PPh 21 Dipotong", "Fasilitas", "Tanggal Pemotongan", "Metode", "No. Karyawan"}}
	n := 0
	for rows.Next() {
		var no, name, ptkp string
		var npwp, nik *string
		var rate, gross, tax, final, sev decimal.Decimal
		var paid time.Time
		var annual bool
		if err := rows.Scan(&no, &name, &npwp, &nik, &ptkp, &rate, &gross, &tax, &final, &sev, &paid, &annual); err != nil {
			return nil, 0, err
		}
		id := digits(npwp)
		if id == "" {
			id = digits(nik)
		}
		method := "TER"
		if annual {
			method = "Tahunan (Pasal 17)"
		}
		out = append(out, []string{strconv.Itoa(int(month.Month())), strconv.Itoa(month.Year()), "Normal", id, name, "21-100-01", ptkp, money(gross),
			rate.String(), money(tax), "Tanpa Fasilitas", paid.Format("02/01/2006"), method, no})
		if sev.IsPositive() {
			out = append(out, []string{strconv.Itoa(int(month.Month())), strconv.Itoa(month.Year()), "Normal", id, name, "21-401-01", ptkp, money(sev), "",
				money(final), "Tanpa Fasilitas", paid.Format("02/01/2006"), "Final (pesangon)", no})
		}
		n++
	}
	if err := rows.Err(); err != nil {
		return nil, 0, err
	}
	b, err := writeCSV(out)
	return b, n, err
}

// exportBPJS is the BPJS contribution file of a period.
func exportBPJS(ctx context.Context, tx pgx.Tx, property uuid.UUID, month time.Time, kesehatan bool) ([]byte, int, error) {
	rows, err := tx.Query(ctx, `SELECT s.employee_no, s.full_name, min(s.nik), min(e.bpjs_kesehatan_no), min(e.bpjs_ketenagakerjaan_no),
		max(l.quantity) FILTER (WHERE l.programme = 'kesehatan'), max(l.quantity) FILTER (WHERE l.programme = 'jht'),
		max(l.quantity) FILTER (WHERE l.programme = 'jp'),
		coalesce(sum(l.amount) FILTER (WHERE l.code = 'BPJS_KESEHATAN_ER'), 0), coalesce(sum(l.amount) FILTER (WHERE l.code = 'BPJS_KESEHATAN_EE'), 0),
		coalesce(sum(l.amount) FILTER (WHERE l.code = 'BPJS_JHT_ER'), 0), coalesce(sum(l.amount) FILTER (WHERE l.code = 'BPJS_JHT_EE'), 0),
		coalesce(sum(l.amount) FILTER (WHERE l.code = 'BPJS_JP_ER'), 0), coalesce(sum(l.amount) FILTER (WHERE l.code = 'BPJS_JP_EE'), 0),
		coalesce(sum(l.amount) FILTER (WHERE l.code = 'BPJS_JKK_ER'), 0), coalesce(sum(l.amount) FILTER (WHERE l.code = 'BPJS_JKM_ER'), 0)
		FROM hris.payroll_lines l JOIN hris.payroll_slips s ON s.id = l.slip_id JOIN hris.payroll_runs r ON r.id = l.run_id
		JOIN hris.employees e ON e.id = s.employee_id
		WHERE r.property_id = $1 AND r.period_code = $2 AND r.status IN ('approved', 'posted', 'paid') AND l.kind IN ('bpjs_employee', 'bpjs_employer')
		GROUP BY s.employee_id, s.employee_no, s.full_name ORDER BY s.employee_no`, property, periodCode(month))
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	var out [][]string
	if kesehatan {
		out = [][]string{{"No. Peserta BPJS Kesehatan", "NIK", "Nama", "Upah", "Iuran Pemberi Kerja (4%)", "Iuran Pekerja (1%)", "Total Iuran", "Periode",
			"No. Karyawan"}}
	} else {
		out = [][]string{{"No. KPJ", "NIK", "Nama", "Upah", "JHT Pemberi Kerja", "JHT Tenaga Kerja", "JP Pemberi Kerja", "JP Tenaga Kerja", "JKK", "JKM",
			"Total Iuran", "Periode", "No. Karyawan"}}
	}
	n := 0
	for rows.Next() {
		var no, name string
		var nik, kes, tk *string
		var baseKes, baseJHT, baseJP *decimal.Decimal
		var v [8]decimal.Decimal
		if err := rows.Scan(&no, &name, &nik, &kes, &tk, &baseKes, &baseJHT, &baseJP, &v[0], &v[1], &v[2], &v[3], &v[4], &v[5], &v[6], &v[7]); err != nil {
			return nil, 0, err
		}
		base := func(d *decimal.Decimal) string {
			if d == nil {
				return ""
			}
			return money(*d)
		}
		if kesehatan {
			if v[0].IsZero() && v[1].IsZero() {
				continue
			}
			out = append(out, []string{digits(kes), digits(nik), name, base(baseKes), money(v[0]), money(v[1]), money(v[0].Add(v[1])), periodCode(month), no})
		} else {
			total := v[2].Add(v[3]).Add(v[4]).Add(v[5]).Add(v[6]).Add(v[7])
			if total.IsZero() {
				continue
			}
			out = append(out, []string{digits(tk), digits(nik), name, base(baseJHT), money(v[2]), money(v[3]), money(v[4]), money(v[5]), money(v[6]),
				money(v[7]), money(total), periodCode(month), no})
		}
		n++
	}
	if err := rows.Err(); err != nil {
		return nil, 0, err
	}
	b, err := writeCSV(out)
	return b, n, err
}

// Form1721A1 is the annual withholding statement of an employee.
type Form1721A1 struct {
	EmployeeID                                        uuid.UUID
	EmployeeNo, Name, TaxID, PTKP                     string
	FirstMonth, LastMonth                             int
	Salary, Allowances, Overtime, Insurance, Bonus    decimal.Decimal
	Gross, Occupational, Contributions, Net           decimal.Decimal
	PriorNet, YearNet, PTKPAmount, Taxable, AnnualTax decimal.Decimal
	PriorTax, Withheld                                decimal.Decimal
}

// Export1721A1 is the 1721-A1 data per employee of a tax year (numbered as
// the form: 1 salary … 20 PPh 21 withheld), from the runs of the year and
// the imported opening YTD.
func Export1721A1(ctx context.Context, tx pgx.Tx, property uuid.UUID, year int, employee *uuid.UUID) ([]byte, int, error) {
	rates, _, err := statutoryRatesAt(ctx, tx, property, time.Date(year, 12, 31, 0, 0, 0, 0, time.UTC))
	if err != nil {
		return nil, 0, err
	}
	rows, err := tx.Query(ctx, `SELECT s.employee_id, min(s.employee_no), min(s.full_name), coalesce(min(s.npwp), min(s.nik), ''), coalesce(min(s.ptkp_status), 'TK/0'),
		min(right(r.period_code, 2)::int), max(right(r.period_code, 2)::int),
		coalesce(sum(l.amount) FILTER (WHERE l.kind = 'earning' AND l.category = 'basic'), 0),
		coalesce(sum(l.amount) FILTER (WHERE l.kind = 'earning' AND l.category IN ('allowance', 'service_charge', 'commission', 'other', 'rounding')
		  AND l.taxable), 0) - coalesce(sum(l.amount) FILTER (WHERE l.kind = 'deduction' AND l.pre_tax), 0),
		coalesce(sum(l.amount) FILTER (WHERE l.kind = 'earning' AND l.category = 'overtime'), 0),
		coalesce(sum(l.amount) FILTER (WHERE l.kind = 'bpjs_employer' AND l.programme = ANY ($4)), 0),
		coalesce(sum(l.amount) FILTER (WHERE l.kind = 'earning' AND l.category IN ('bonus', 'thr', 'leave_encashment')), 0),
		coalesce(sum(l.amount) FILTER (WHERE l.kind = 'bpjs_employee' AND l.programme = ANY ($5)), 0),
		coalesce(sum(l.amount) FILTER (WHERE l.kind = 'tax' AND l.code = 'PPH21'), 0)
		FROM hris.payroll_lines l JOIN hris.payroll_slips s ON s.id = l.slip_id JOIN hris.payroll_runs r ON r.id = l.run_id
		WHERE r.property_id = $1 AND left(r.period_code, 4) = $2 AND r.status IN ('approved', 'posted', 'paid') AND ($3::uuid IS NULL OR s.employee_id = $3)
		GROUP BY s.employee_id ORDER BY 2`, property, strconv.Itoa(year), employee, rates.TaxableEmployerContributions, rates.DeductibleEmployeeContributions)
	if err != nil {
		return nil, 0, err
	}
	var forms []Form1721A1
	for rows.Next() {
		var f Form1721A1
		if err := rows.Scan(&f.EmployeeID, &f.EmployeeNo, &f.Name, &f.TaxID, &f.PTKP, &f.FirstMonth, &f.LastMonth, &f.Salary, &f.Allowances, &f.Overtime,
			&f.Insurance, &f.Bonus, &f.Contributions, &f.Withheld); err != nil {
			rows.Close()
			return nil, 0, err
		}
		forms = append(forms, f)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, 0, err
	}
	out := [][]string{{"No. Karyawan", "Nama", "NPWP/NIK", "Status PTKP", "Masa Perolehan Awal", "Masa Perolehan Akhir", "1 Gaji/Pensiun", "2 Tunjangan PPh",
		"3 Tunjangan Lainnya, Uang Lembur", "4 Honorarium", "5 Premi Asuransi oleh Pemberi Kerja", "6 Natura", "7 Tantiem, Bonus, Gratifikasi, THR",
		"8 Jumlah Penghasilan Bruto", "9 Biaya Jabatan", "10 Iuran Pensiun/JHT/JP", "11 Jumlah Pengurangan", "12 Penghasilan Neto",
		"13 Neto Masa Sebelumnya", "14 Neto Setahun", "15 PTKP", "16 PKP Setahun", "17 PPh 21 atas PKP", "18 PPh 21 Masa Sebelumnya", "19 PPh 21 Terutang",
		"20 PPh 21 Telah Dipotong"}}
	for _, f := range forms {
		var priorGross, priorDed, priorTax decimal.Decimal
		var priorMonths int
		err := tx.QueryRow(ctx, `SELECT gross, deductible, pph21, months_worked FROM hris.payroll_ytd WHERE employee_id = $1 AND tax_year = $2`, f.EmployeeID, year).
			Scan(&priorGross, &priorDed, &priorTax, &priorMonths)
		if err != nil && !strings.Contains(err.Error(), "no rows") {
			return nil, 0, err
		}
		f.Overtime = f.Overtime.Add(f.Allowances)
		f.Gross = f.Salary.Add(f.Overtime).Add(f.Insurance).Add(f.Bonus)
		months := f.LastMonth - f.FirstMonth + 1 + priorMonths
		a := rates.Annual(f.PTKP, f.Gross.Add(priorGross), f.Contributions.Add(priorDed), months, f.TaxID != "")
		// the occupational cost and contributions of the OneClub months
		f.Occupational = a.OccupationalCost
		f.Net = f.Gross.Sub(f.Occupational).Sub(f.Contributions)
		f.PriorNet = priorGross.Sub(priorDed)
		f.YearNet, f.PTKPAmount, f.Taxable, f.AnnualTax, f.PriorTax = a.Net, a.PTKP, a.TaxableIncome, a.AnnualTax, priorTax
		out = append(out, []string{f.EmployeeNo, f.Name, digits(&f.TaxID), f.PTKP, strconv.Itoa(f.FirstMonth), strconv.Itoa(f.LastMonth), money(f.Salary), "0",
			money(f.Overtime), "0", money(f.Insurance), "0", money(f.Bonus), money(f.Gross), money(f.Occupational), money(f.Contributions),
			money(f.Occupational.Add(f.Contributions)), money(f.Net), money(f.PriorNet), money(f.YearNet), money(f.PTKPAmount), money(f.Taxable),
			money(f.AnnualTax), money(f.PriorTax), money(f.AnnualTax.Sub(f.PriorTax)), money(f.Withheld)})
	}
	b, err := writeCSV(out)
	return b, len(forms), err
}
