# Reconciliation — Phase 7C

Reconciliation membandingkan settlement evidence dengan financial records internal. Proses ini hanya membuat audit result dan mismatch; ia tidak mengubah payment, refund, `refunded_amount`, atau `reserved_refund_amount`.

## Matching

Payment dicari deterministic dengan `(provider, provider_transaction_id)`. Match hanya jika transaction berstatus `PAID`, currency sama, dan gross amount sama dengan transaction amount. Refund dicari dengan `(provider, provider_refund_id)`. Match hanya jika refund ada, berstatus `SUCCEEDED`, currency sama, amount sama, dan provider identity cocok.

Amount-only matching tidak digunakan. Adjustment disimpan tetapi menghasilkan `UNSUPPORTED_ADJUSTMENT`.

## Result types

`PAYMENT_MATCH`, `REFUND_MATCH`, `PAYMENT_NOT_FOUND`, `REFUND_NOT_FOUND`, `PAYMENT_AMOUNT_MISMATCH`, `REFUND_AMOUNT_MISMATCH`, `PAYMENT_CURRENCY_MISMATCH`, `REFUND_CURRENCY_MISMATCH`, `UNEXPECTED_SETTLEMENT_ITEM`, `UNSUPPORTED_ADJUSTMENT`, dan `DUPLICATE_SETTLEMENT_ITEM`.

Setiap result menyimpan settlement/item, transaction atau refund jika ditemukan, expected/actual amount dan currency, difference, reason, details, dan timestamps.

## Concurrency and rerun

Reconciliation melakukan atomic claim status menggunakan PostgreSQL conditional update. Concurrent requests pada settlement yang sama menghasilkan `RECONCILIATION_ALREADY_RUNNING`; stale `RECONCILING` dapat direclaim. Result memakai unique `settlement_item_id` dan `ON CONFLICT DO UPDATE`, sedangkan result dan final settlement status disimpan dalam satu transaction. Karena itu rerun tidak membuat duplicate current result dan tidak meninggalkan settlement final yang berbeda dari result terakhir.

## API

Semua endpoint berikut memerlukan `X-Admin-Key`:

- `POST /api/v1/admin/settlements/:id/reconcile`
- `GET /api/v1/admin/settlements/:id/reconciliation`
- `GET /api/v1/admin/reconciliation/mismatches`
- `GET /api/v1/admin/reconciliation/mismatches/:id`

List endpoint menggunakan pagination dan ordering `created_at DESC, id DESC`. Mismatch report dapat difilter provider, settlement, merchant, result type, reason code, dan date range.

## Recovery

Jika matching atau persistence gagal, reconciliation run ditandai `FAILED` dan settlement juga ditandai `FAILED`. Operator dapat menjalankan endpoint reconcile kembali. Reconciliation tidak melakukan refund execution, payout, transfer, atau automatic correction.
