-- Migration: 000013_create_settlements (up)
-- Phase 7C: settlements, settlement_items, reconciliation_runs, reconciliation_results
-- Settlement is external accounting evidence — it does NOT change payment/refund financial truth.

CREATE TABLE IF NOT EXISTS settlements (
    id                UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    provider          VARCHAR(50)  NOT NULL,
    settlement_ref    VARCHAR(150) NOT NULL,
    settlement_date   DATE         NOT NULL,
    currency          VARCHAR(3)   NOT NULL,
    gross_amount      BIGINT       NOT NULL,
    fee_amount        BIGINT       NOT NULL DEFAULT 0,
    net_amount        BIGINT       NOT NULL,
    status            VARCHAR(30)  NOT NULL,
    source            VARCHAR(50)  NOT NULL,
    payload_hash      VARCHAR(64)  NOT NULL,
    raw_payload       JSONB,
    item_count        INT          NOT NULL DEFAULT 0,
    matched_count     INT          NOT NULL DEFAULT 0,
    mismatch_count    INT          NOT NULL DEFAULT 0,
    unmatched_count   INT          NOT NULL DEFAULT 0,
    imported_at       TIMESTAMPTZ  NOT NULL DEFAULT NOW(),
    reconciled_at     TIMESTAMPTZ,
    created_at        TIMESTAMPTZ  NOT NULL DEFAULT NOW(),
    updated_at        TIMESTAMPTZ  NOT NULL DEFAULT NOW(),
    CONSTRAINT chk_settlements_gross_nonneg CHECK (gross_amount >= 0),
    CONSTRAINT chk_settlements_fee_nonneg CHECK (fee_amount >= 0),
    CONSTRAINT chk_settlements_net_nonneg CHECK (net_amount >= 0),
    CONSTRAINT chk_settlements_currency CHECK (char_length(currency) = 3),
    CONSTRAINT chk_settlements_status CHECK (
        status IN ('IMPORTED', 'RECONCILING', 'RECONCILED', 'PARTIAL', 'MISMATCH', 'FAILED')
    ),
    CONSTRAINT chk_settlements_counts_nonneg CHECK (
        item_count >= 0 AND matched_count >= 0 AND mismatch_count >= 0 AND unmatched_count >= 0
    )
);

CREATE UNIQUE INDEX IF NOT EXISTS uq_settlements_provider_ref
    ON settlements (provider, settlement_ref);

CREATE INDEX IF NOT EXISTS idx_settlements_status_date
    ON settlements (status, settlement_date DESC);

CREATE INDEX IF NOT EXISTS idx_settlements_created_at
    ON settlements (created_at DESC, id DESC);

CREATE TABLE IF NOT EXISTS settlement_items (
    id                      UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    settlement_id           UUID         NOT NULL REFERENCES settlements (id) ON DELETE CASCADE,
    provider                VARCHAR(50)  NOT NULL,
    item_ref                VARCHAR(200) NOT NULL,
    item_type               VARCHAR(30)  NOT NULL,
    provider_transaction_id VARCHAR(150),
    provider_refund_id      VARCHAR(150),
    order_id                VARCHAR(100),
    currency                VARCHAR(3)   NOT NULL,
    gross_amount            BIGINT       NOT NULL,
    fee_amount              BIGINT       NOT NULL DEFAULT 0,
    net_amount              BIGINT       NOT NULL,
    settled_at              TIMESTAMPTZ,
    raw_payload             JSONB,
    created_at              TIMESTAMPTZ  NOT NULL DEFAULT NOW(),
    CONSTRAINT chk_settlement_items_gross_nonneg CHECK (gross_amount >= 0),
    CONSTRAINT chk_settlement_items_fee_nonneg CHECK (fee_amount >= 0),
    CONSTRAINT chk_settlement_items_net_nonneg CHECK (net_amount >= 0),
    CONSTRAINT chk_settlement_items_currency CHECK (char_length(currency) = 3),
    CONSTRAINT chk_settlement_items_type CHECK (
        item_type IN ('PAYMENT', 'REFUND', 'ADJUSTMENT')
    )
);

CREATE UNIQUE INDEX IF NOT EXISTS uq_settlement_items_settlement_ref
    ON settlement_items (settlement_id, item_ref);

CREATE INDEX IF NOT EXISTS idx_settlement_items_settlement_created
    ON settlement_items (settlement_id, created_at DESC, id DESC);

CREATE INDEX IF NOT EXISTS idx_settlement_items_provider_tx
    ON settlement_items (provider, provider_transaction_id)
    WHERE provider_transaction_id IS NOT NULL;

CREATE INDEX IF NOT EXISTS idx_settlement_items_provider_refund
    ON settlement_items (provider, provider_refund_id)
    WHERE provider_refund_id IS NOT NULL;

-- A provider identity is evidence for one settlement line only. Prevent the
-- same payment/refund from being imported into two settlement batches.
CREATE UNIQUE INDEX IF NOT EXISTS uq_settlement_items_provider_tx
    ON settlement_items (provider, provider_transaction_id)
    WHERE provider_transaction_id IS NOT NULL;

CREATE UNIQUE INDEX IF NOT EXISTS uq_settlement_items_provider_refund
    ON settlement_items (provider, provider_refund_id)
    WHERE provider_refund_id IS NOT NULL;

CREATE TABLE IF NOT EXISTS reconciliation_runs (
    id               UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    settlement_id    UUID         NOT NULL REFERENCES settlements (id) ON DELETE CASCADE,
    status           VARCHAR(30)  NOT NULL,
    total_items      INT          NOT NULL DEFAULT 0,
    matched_items    INT          NOT NULL DEFAULT 0,
    mismatch_items   INT          NOT NULL DEFAULT 0,
    unmatched_items  INT          NOT NULL DEFAULT 0,
    payment_matches  INT          NOT NULL DEFAULT 0,
    refund_matches   INT          NOT NULL DEFAULT 0,
    amount_mismatches INT         NOT NULL DEFAULT 0,
    currency_mismatches INT       NOT NULL DEFAULT 0,
    not_found_count  INT          NOT NULL DEFAULT 0,
    unsupported_count INT         NOT NULL DEFAULT 0,
    error_message    TEXT,
    started_at       TIMESTAMPTZ  NOT NULL DEFAULT NOW(),
    finished_at      TIMESTAMPTZ,
    created_at       TIMESTAMPTZ  NOT NULL DEFAULT NOW(),
    CONSTRAINT chk_reconciliation_runs_status CHECK (
        status IN ('RUNNING', 'COMPLETED', 'FAILED')
    )
);

CREATE INDEX IF NOT EXISTS idx_reconciliation_runs_settlement
    ON reconciliation_runs (settlement_id, created_at DESC);

CREATE TABLE IF NOT EXISTS reconciliation_results (
    id                     UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    settlement_id          UUID         NOT NULL REFERENCES settlements (id) ON DELETE CASCADE,
    settlement_item_id     UUID         NOT NULL REFERENCES settlement_items (id) ON DELETE CASCADE,
    reconciliation_run_id  UUID         NOT NULL REFERENCES reconciliation_runs (id) ON DELETE CASCADE,
    transaction_id         UUID         REFERENCES transactions (id),
    refund_id              UUID         REFERENCES refunds (id),
    merchant_id            UUID         REFERENCES merchants (id),
    result_type            VARCHAR(50)  NOT NULL,
    expected_amount        BIGINT,
    actual_amount          BIGINT,
    expected_currency      VARCHAR(3),
    actual_currency        VARCHAR(3),
    difference_amount      BIGINT,
    status                 VARCHAR(30)  NOT NULL,
    reason_code            VARCHAR(50),
    details                JSONB,
    created_at             TIMESTAMPTZ  NOT NULL DEFAULT NOW(),
    updated_at             TIMESTAMPTZ  NOT NULL DEFAULT NOW(),
    CONSTRAINT chk_reconciliation_results_status CHECK (
        status IN ('MATCHED', 'MISMATCH', 'UNMATCHED')
    ),
    CONSTRAINT chk_reconciliation_results_type CHECK (
        result_type IN (
            'PAYMENT_MATCH',
            'REFUND_MATCH',
            'PAYMENT_NOT_FOUND',
            'REFUND_NOT_FOUND',
            'PAYMENT_AMOUNT_MISMATCH',
            'REFUND_AMOUNT_MISMATCH',
            'PAYMENT_CURRENCY_MISMATCH',
            'REFUND_CURRENCY_MISMATCH',
            'UNEXPECTED_SETTLEMENT_ITEM',
            'UNSUPPORTED_ADJUSTMENT',
            'DUPLICATE_SETTLEMENT_ITEM'
        )
    )
);

-- One current result per settlement item — rerun upserts safely.
CREATE UNIQUE INDEX IF NOT EXISTS uq_reconciliation_results_item
    ON reconciliation_results (settlement_item_id);

CREATE INDEX IF NOT EXISTS idx_reconciliation_results_settlement
    ON reconciliation_results (settlement_id, created_at DESC, id DESC);

CREATE INDEX IF NOT EXISTS idx_reconciliation_results_transaction
    ON reconciliation_results (transaction_id)
    WHERE transaction_id IS NOT NULL;

CREATE INDEX IF NOT EXISTS idx_reconciliation_results_refund
    ON reconciliation_results (refund_id)
    WHERE refund_id IS NOT NULL;

CREATE INDEX IF NOT EXISTS idx_reconciliation_results_status_created
    ON reconciliation_results (status, created_at DESC);

CREATE INDEX IF NOT EXISTS idx_reconciliation_results_reason_created
    ON reconciliation_results (reason_code, created_at DESC)
    WHERE reason_code IS NOT NULL;

CREATE INDEX IF NOT EXISTS idx_reconciliation_results_merchant
    ON reconciliation_results (merchant_id, created_at DESC)
    WHERE merchant_id IS NOT NULL;
