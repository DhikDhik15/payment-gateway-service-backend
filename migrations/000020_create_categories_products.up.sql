-- Migration: 000020_create_categories_products (up)
-- Phase 7: first POS domain — categories and products, merchant-scoped.
--
-- Conventions follow the existing schema:
--   * UUID primary keys via gen_random_uuid()
--   * merchant_id FK → merchants(id) ON DELETE CASCADE
--   * TIMESTAMPTZ DEFAULT NOW()
--   * integer minor units for money (BIGINT, no floating point)
--   * uniqueness is merchant-scoped (never global)

CREATE TABLE IF NOT EXISTS categories (
    id          UUID         PRIMARY KEY DEFAULT gen_random_uuid(),
    merchant_id UUID         NOT NULL REFERENCES merchants(id) ON DELETE CASCADE,
    name        VARCHAR(100) NOT NULL,
    description VARCHAR(500),
    created_at  TIMESTAMPTZ  NOT NULL DEFAULT NOW(),
    updated_at  TIMESTAMPTZ  NOT NULL DEFAULT NOW()
);

-- Category names are unique per merchant (case-insensitive), never globally.
CREATE UNIQUE INDEX IF NOT EXISTS uq_categories_merchant_name
    ON categories (merchant_id, lower(name));

CREATE INDEX IF NOT EXISTS idx_categories_merchant
    ON categories (merchant_id);

CREATE TABLE IF NOT EXISTS products (
    id          UUID         PRIMARY KEY DEFAULT gen_random_uuid(),
    merchant_id UUID         NOT NULL REFERENCES merchants(id) ON DELETE CASCADE,
    category_id UUID         REFERENCES categories(id) ON DELETE SET NULL,
    name        VARCHAR(200) NOT NULL,
    sku         VARCHAR(50),
    description VARCHAR(500),
    price       BIGINT       NOT NULL CHECK (price >= 0),
    currency    VARCHAR(3)   NOT NULL,
    active      BOOLEAN      NOT NULL DEFAULT TRUE,
    created_at  TIMESTAMPTZ  NOT NULL DEFAULT NOW(),
    updated_at  TIMESTAMPTZ  NOT NULL DEFAULT NOW()
);

-- SKU is unique per merchant when present (NULL SKUs allowed, duplicates of
-- the empty SKU are not allowed — callers should omit sku instead of sending "").
CREATE UNIQUE INDEX IF NOT EXISTS uq_products_merchant_sku
    ON products (merchant_id, sku) WHERE sku IS NOT NULL;

CREATE INDEX IF NOT EXISTS idx_products_merchant
    ON products (merchant_id);

CREATE INDEX IF NOT EXISTS idx_products_category
    ON products (category_id);
