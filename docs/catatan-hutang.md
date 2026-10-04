# Catatan Hutang — Integrasi P2 di atas P1

Status per 4 Oktober 2026 malam. Branch `feat/p2-on-p1`, semua commit masih lokal dan belum di-push.

**Hutang #1–#6 selesai.** Yang tersisa hanya keputusan dan langkah rilis di bagian "Sisa". Rincian perubahan di file P1 untuk direview Dian ada di `docs/p2-contract-review.md`; peta requirement → kode → test ada di `docs/p2-traceability.md`. File ini dibuang setelah PR di-merge.

## Hasil verifikasi terakhir

- `go build ./... && go vet ./...` hijau.
- `go test ./test/e2e/` (P0 + P1 + P2, **coverage wajib aktif**): hijau, 521/521 route mutasi teruji dan teraudit.
- `go test ./internal/...` (termasuk provision) hijau.
- `pnpm -r typecheck` dan `pnpm -r build` hijau.
- Path API frontend dan test cocok dengan OpenAPI (validator di bawah).
- File P1 yang berubah sesuai daftar di `docs/p2-contract-review.md`:

```bash
git diff --diff-filter=MD --stat a48e6a3 -- internal/golf internal/billing internal/crm internal/membership internal/commercial/*.go
```

## Sisa

1. **Urutan merge dengan P1.** P1 belum di `main`: ada di `origin/feat/p1-golf-core-mvp` (masih `a48e6a3`) dan `origin/staging`. Branch ini berisi `origin/main` + commit P1 + commit P2 (0 commit tertinggal dari `main`). Pilihan:
   - PR P1 di-merge dulu (merge commit), lalu PR P2 hanya berisi commit P2; atau
   - PR P2 langsung ke `main` dan ikut membawa commit P1.

   Bila P1 di-merge dengan squash, branch ini perlu di-rebase ke atas `main` yang baru sebelum PR.
2. **Push dan PR ke `main`** setelah urutan di atas disepakati.
3. **Jalankan spec Playwright** `web/e2e/p2.spec.ts`. Spec sudah memakai path baru tetapi belum pernah dijalankan; butuh API dan preview yang berjalan (`web/playwright.config.ts`), atau dijalankan di CI.

## Diputuskan (4 Oktober 2026)

- #6.7: `POST /billing/customer-accounts` untuk akun yang sudah ada tanpa perubahan tetap mengembalikan 201 tanpa entri audit (perilaku P1 dipertahankan).
- Customer 360: tombol "View all business lines" di halaman Customer 360 P1 membuka Customer 360 lintas lini P2 (`crm/customers/:id`).
- Rating caddy dari member app: belum ada tombol (`member/golf/my-flights` P1 tidak mengembalikan id assignment); rating lewat link feedback setelah ronde.

## Aturan (berlaku sampai PR di-merge)

Sumber: Tech Doc §4.2, §7.5, §12.3 dan PRD P2 §5.4.

- File milik P1 (golf, billing, crm, membership, commercial/pricing; frontend P1) tidak diubah, kecuali requirement PRD yang hanya bisa dipenuhi di sana atau penyesuaian P1. Keduanya dicatat di `docs/p2-contract-review.md`.
- Kebutuhan P2 ditaruh di sub-package P2 atau file kontrak aditif (`p2_*.go`, `lines.go`, `pricing_p2.go`, `sales_api.go`; frontend `p2.tsx`, `lib-p2.ts`).
- Tidak ada query ke schema modul lain; pakai API publik, read model `reporting.*`, atau domain event.
- Migration expand-only; `internal/app`, catalog, navigasi dan router frontend hanya diubah secara aditif.

## Catatan teknis

- **Menjalankan test:** `ONECLUB_TEST_ADMIN_URL="postgres://postgres:postgres@localhost:5432/postgres?sslmode=disable" go test -count=1 ./test/e2e/`. Untuk iterasi satu test, tambahkan `ONECLUB_REQUIRE_FULL_COVERAGE=false -run TestX`.
- **PostgreSQL 18 lokal** jalan sebagai service Windows di `localhost:5432` (`postgres/postgres`).
- **Test golf P2** memakai course buatan test (`setupGolfCourse` + tee sheet template P1) dan hari main sendiri (`clubDay` 18/22/26/30). Waktu ronde dimundurkan lewat SQL (`backdate`) karena pembagian fee caddy dan pace dihitung dari timestamp.
- **Validator path API** (dibuat ulang bila perlu; scratchpad tidak permanen):
  - test Go: cocokkan setiap `"METHOD", "/api/v1/..."` di `test/e2e` dengan path `go run ./cmd/oneclub openapi`;
  - frontend: kumpulkan literal `'/api/v1/...'` dan template string di `web/apps/**` (ganti `${...}` dengan placeholder, buang `${qs(...)}`), lalu cocokkan dengan path OpenAPI. Typecheck saja tidak cukup karena path berupa string.
- **Dead code:** `go run golang.org/x/tools/cmd/deadcode@latest -test ./...`. Sisa temuannya hanya fungsi yang sudah tidak terpakai sejak P0/P1 (bukan wewenang P2).
- **Perbedaan semantik P1 yang sering menjebak:** uang berformat 4 desimal (bandingkan dengan `dec`); status pembayaran `completed` (bukan `paid`); 1 caddy per pemain (Caddy Policy); event SSE bernama sesuai topik (`golf.pace`, `commercial.kds`), jenis event ada di data.
