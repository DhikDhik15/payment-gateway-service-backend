-- Migration: 000021_create_orders (down)
DROP TABLE IF EXISTS order_items;
DROP TABLE IF EXISTS orders;
ALTER TABLE products DROP CONSTRAINT IF EXISTS uq_products_merchant_id_id;
