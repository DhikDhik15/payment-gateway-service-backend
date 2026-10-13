-- Migration: 000024_product_image_local_refs (up)
-- Product image uploads: allow backend-stored local image references in
-- products.image_url alongside external HTTPS URLs.
--
-- Two reference forms are valid after this migration:
--   1. external absolute HTTPS URLs (unchanged from 000023), and
--   2. local references issued by POST /api/v1/dashboard/product-images:
--        /api/v1/product-images/{merchant_uuid}/{file_uuid}.{jpg|jpeg|png|webp}
--
-- The local branch is a strict regular expression (no spaces, no userinfo,
-- no traversal segments, server-generated UUID grammar) mirroring the
-- application validation in model.ParseLocalProductImageRef.
--
-- Prerequisite: migration 000023 must be applied first (it owns the
-- products.image_url column). This migration is NOT run automatically —
-- apply with the migrate runner after review.

ALTER TABLE products DROP CONSTRAINT IF EXISTS chk_products_image_url;

ALTER TABLE products
    ADD CONSTRAINT chk_products_image_url
    CHECK (image_url IS NULL OR (
        image_url LIKE 'https://%' AND
        position('@' IN image_url) = 0 AND
        image_url NOT LIKE '% %' AND
        char_length(image_url) <= 2048
    ) OR (
        image_url ~ '^/api/v1/product-images/[0-9a-fA-F-]{36}/[0-9a-fA-F-]{36}\.(jpg|jpeg|png|webp)$' AND
        char_length(image_url) <= 2048
    ));
