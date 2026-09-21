# Settlements — Phase 7C

Settlement adalah **external accounting evidence** dari provider. Ia tidak menggantikan `transactions.status=PAID`, tidak mengubah payment amount/status, dan tidak mengubah refund counters.

## Import

`POST /api/v1/admin/settlements/import` menerima `provider`, `settlement_ref`, dan payload provider. Phase 7C menyediakan `MOCK` importer; format provider live yang belum tervalidasi tidak diarang.

Import melakukan parsing, validasi, lalu menyimpan header dan seluruh item dalam satu PostgreSQL transaction. Network/provider call tidak dilakukan di dalam transaction. Payload dibatasi 1 MiB, disimpan setelah redaction secret-like fields, dan tidak pernah ditampilkan sebagai default response.

Identity import adalah `(provider, settlement_ref)`. Payload JSON di-canonicalize lalu di-hash SHA-256:

- identity dan hash sama: replay idempotent, tanpa row baru;
- identity sama tetapi hash berbeda: `SETTLEMENT_IMPORT_CONFLICT`;
- item provider transaction/refund identity juga unik lintas settlement.

## Status

```text
IMPORTED -> RECONCILING -> RECONCILED
                              PARTIAL
                              MISMATCH
                              FAILED
```

Settlement boleh direconcile ulang dari status final. `RECONCILING` yang stale dapat diambil kembali berdasarkan `SETTLEMENT_RECON_STALE_AFTER`.

## Model

`settlements` menyimpan batch-level metadata dan `settlement_items` menyimpan payment, refund, atau adjustment. Fee/net disimpan sebagai evidence; Phase 7C tidak menganggap fee/net sebagai internal financial truth.

## Security

Endpoint admin menggunakan `X-Admin-Key`, bukan merchant `X-API-Key`. Shared key ini adalah ops abstraction minimal, bukan RBAC production: tidak ada role, rotation, MFA, atau operator identity. Jika `ADMIN_API_KEY` kosong, endpoint mengembalikan `ADMIN_NOT_CONFIGURED`.

Raw payload tidak ditulis ke log. Provider credentials tidak boleh masuk payload; known secret-like keys disimpan sebagai `[REDACTED]`.

## Operational workflow

```text
provider report -> import -> validate -> atomic persist -> reconcile -> review mismatch -> rerun
```

Live Midtrans settlement import belum diverifikasi karena contract report dan credential settlement tidak tersedia. Gunakan mock importer untuk development dan test.
