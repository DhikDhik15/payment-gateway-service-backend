-- Phase 10C: URL-only product image reference (no uploads, no object storage).
-- NULL means no image. The CHECK is a database-level backstop for the
-- application validation: absolute HTTPS URL, no embedded credentials,
-- bounded length. The backend never fetches the URL.
ALTER TABLE products
    ADD COLUMN image_url TEXT;

ALTER TABLE products
    ADD CONSTRAINT chk_products_image_url
    CHECK (image_url IS NULL OR (
        image_url LIKE 'https://%' AND
        position('@' IN image_url) = 0 AND
        image_url NOT LIKE '% %' AND
        char_length(image_url) <= 2048
    ));
