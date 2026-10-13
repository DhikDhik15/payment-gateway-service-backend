ALTER TABLE products DROP CONSTRAINT IF EXISTS chk_products_image_url;
ALTER TABLE products DROP COLUMN IF EXISTS image_url;
