-- Migration: 000024_product_image_local_refs (down)
-- Restores the 000023 URL-only CHECK. This FAILS if any row still holds a
-- local /api/v1/product-images/... reference — unlink or migrate those rows
-- (set image_url NULL or to an external HTTPS URL) before rolling back.

ALTER TABLE products DROP CONSTRAINT IF EXISTS chk_products_image_url;

ALTER TABLE products
    ADD CONSTRAINT chk_products_image_url
    CHECK (image_url IS NULL OR (
        image_url LIKE 'https://%' AND
        position('@' IN image_url) = 0 AND
        image_url NOT LIKE '% %' AND
        char_length(image_url) <= 2048
    ));
