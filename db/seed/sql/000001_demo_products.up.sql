-- =============================================================================
-- Product Service - Demo Seed Data (DEV ONLY)
-- =============================================================================
-- Purpose: Demo product catalog for local/dev/demo environments only.
-- Applied ONLY by the `seed` subcommand — NEVER by `migrate` or the serve path,
-- so production databases are never seeded with demo products.
-- Categories are reference data and live in the schema migration (000001).
-- =============================================================================

-- =============================================================================
-- DEMO PRODUCT CATALOG
-- =============================================================================
INSERT INTO products (name, description, price, category_id) VALUES
    ('Wireless Mouse', 'Ergonomic wireless mouse with long battery life', 29.99, 1),
    ('Mechanical Keyboard', 'RGB mechanical gaming keyboard with Cherry MX switches', 79.99, 4),
    ('USB-C Hub', '7-in-1 USB-C hub with HDMI, USB 3.0, and SD card readers', 39.99, 2),
    ('Laptop Stand', 'Adjustable aluminum laptop stand for better ergonomics', 44.99, 3),
    ('Webcam HD', '1080p HD webcam with built-in microphone', 59.99, 1),
    ('Monitor 24"', '24-inch Full HD IPS monitor with ultra-thin bezels', 149.99, 1),
    ('Gaming Headset', 'Surround sound gaming headset with noise cancellation', 89.99, 3),
    ('External SSD 1TB', 'Portable 1TB SSD with USB 3.1 Gen 2 interface', 99.99, 2),
    ('Bluetooth Speaker', 'Portable Bluetooth speaker with deep bass and 12-hour playtime', 34.99, 3),
    ('Smartphone Stand', 'Adjustable smartphone stand compatible with all devices', 19.99, 3),
    ('USB Flash Drive 128GB', 'High-speed USB 3.0 flash drive with 128GB capacity', 22.99, 2),
    ('4K HDMI Cable', '2-meter ultra HD 4K HDMI cable with gold-plated connectors', 12.99, 2),
    ('Noise Cancelling Earbuds', 'True wireless earbuds with active noise cancellation', 59.99, 3)
ON CONFLICT (name) DO NOTHING;
