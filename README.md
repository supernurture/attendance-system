# attendance-system

Backend sistem absensi karyawan: autentikasi, RBAC berjenjang, absensi dengan verifikasi GPS +
selfie, daily report, pengajuan cuti dengan approval, dan report rekap untuk atasan.

Satu binary Go + PostgreSQL + object storage S3 (Cloudflare R2 di produksi, MinIO saat
development).

## Status

Dibangun bertahap dalam 7 fase — rencana lengkapnya di [`docs/plan.md`](docs/plan.md).
**Fase 0 selesai** — kerangka, dependensi, dan migrasi.
Belum ada endpoint domain; yang jalan baru `GET /health`.

## Menjalankan

```sh
cp configs/config.example.yaml configs/config.yaml
cp .env.example .env

docker compose up -d --wait  # postgres, redis, minio; --wait sampai bucket-nya jadi
make migrate-up             # terapkan migrasi
make run                    # API di :8080

curl localhost:8080/health
```

## Perintah

`make help` menampilkan semuanya. Yang sering dipakai:

| Perintah | Kegunaan |
|---|---|
| `make run` | jalankan API (`APP=migrate` untuk binary satunya) |
| `make check` | fmt-check + vet + lint + test (persis yang dijalankan CI) |
| `make test` | test dengan race detector |
| `make cover` | laporan coverage di browser |
| `make fmt` | rapikan format (`check` hanya memverifikasi) |
| `make migrate-up` / `migrate-down` / `migrate-status` | migrasi goose |
| `make oapicodegen` | generate kode server dari spec OpenAPI |

## Struktur

```
api/server/specs/       spec OpenAPI, satu file per modul
cmd/api/                HTTP server
cmd/migrate/            runner migrasi goose
internal/api/server/
  modules/<nama>/       handler -> service -> repository
  oapicodegen/          kode hasil generate (jangan diedit)
internal/config/        pemuat config (configs/config.yaml + .env + env)
internal/container/     dependensi bersama, dibuka sekali saat startup
internal/db/migrations/ migrasi SQL, di-embed ke dalam binary
internal/middleware/    request id, access log, recovery, timeout, CORS, security headers
internal/pkg/           helper generik, dipakai bersama di dalam modul ini (storage, util)
pkg/                    pembungkus yang boleh diimpor modul lain
  database, redis, logger
docs/plan.md            rencana 7 fase
```

Menambah modul: tulis `api/server/specs/<nama>.yaml`, jalankan `make oapicodegen`, implementasikan
`StrictServerInterface` yang dihasilkan di `internal/api/server/modules/<nama>/`, lalu daftarkan di
`internal/api/server/router.go`. Aturan validasi hidup di **service**, bukan handler.

## Test yang butuh service

Test `cmd/migrate` dan `internal/pkg/storage` otomatis `t.Skip` kalau Postgres/MinIO tidak
terjangkau, jadi `make check` tetap hijau tanpa menjalankan apa pun. Kalau Anda menggeser port
compose lewat `.env`, beri tahu test-nya juga:

```sh
POSTGRES_TEST_PORT=5433 make check
```

CI menyetel `POSTGRES_TEST_REQUIRED` dan `STORAGE_TEST_REQUIRED` supaya skip di sana jadi gagal.

## Catatan

- Skema dipegang goose, bukan `AutoMigrate` GORM.
- Semua timestamp `timestamptz` UTC; tanggal kerja disimpan sebagai kolom `date` terpisah.
- File tidak pernah melewati server: klien PUT/GET langsung ke object storage lewat presigned URL
  yang ditandatangani server.
