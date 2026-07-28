-- +goose Up
-- +goose StatementBegin

-- ═══════════════════════════════════════════════════════════════
-- หมายเหตุการออกแบบ
--
-- 1) เงินเก็บเป็น BIGINT หน่วย "สตางค์" — ตรงกับ money.Satang (int64) ใน domain
--    ไม่ใช้ NUMERIC/FLOAT เพราะ domain ตัดสินใจไว้แล้วว่าเงินคือจำนวนเต็ม
--
-- 2) รายการสินค้าใน cart/order แยกเป็นตารางลูก ไม่ใช่ JSONB
--    เหตุผล: การบันทึกตะกร้า 1 ใบ = ลบบรรทัดเก่า + ใส่บรรทัดใหม่ + upsert หัว
--    → ต้องอยู่ใน transaction จริง ซึ่งเป็นสิ่งที่เราต้องการพิสูจน์
--
-- 3) ชื่อคอลัมน์เป็น snake_case ตามธรรมเนียม SQL
--    การแปลงเป็นชื่อ field ของ domain เป็นหน้าที่ของ adapter (internal/pgstore)
-- ═══════════════════════════════════════════════════════════════

CREATE TABLE products (
    id           TEXT    PRIMARY KEY,
    sku          TEXT    NOT NULL,
    name         TEXT    NOT NULL,
    price_satang BIGINT  NOT NULL,
    stock        INTEGER NOT NULL DEFAULT 0,
    reserved     INTEGER NOT NULL DEFAULT 0,
    active       BOOLEAN NOT NULL DEFAULT TRUE,

    -- 🛡️ กฎที่ DB บังคับซ้ำอีกชั้น (defense in depth)
    -- กฎจริงยังอยู่ที่ catalog.Product — ตรงนี้คือตาข่ายสุดท้ายกันข้อมูลเสียหาย
    CONSTRAINT products_stock_non_negative    CHECK (stock >= 0),
    CONSTRAINT products_reserved_non_negative CHECK (reserved >= 0),
    CONSTRAINT products_reserved_within_stock CHECK (reserved <= stock),
    CONSTRAINT products_price_non_negative    CHECK (price_satang >= 0)
);
CREATE UNIQUE INDEX products_sku_key ON products (sku);
CREATE INDEX products_active_idx ON products (active) WHERE active;

CREATE TABLE customers (
    id          TEXT    PRIMARY KEY,
    name        TEXT    NOT NULL,
    email       TEXT    NOT NULL,
    suspended   BOOLEAN NOT NULL DEFAULT FALSE,
    open_orders INTEGER NOT NULL DEFAULT 0,

    CONSTRAINT customers_open_orders_non_negative CHECK (open_orders >= 0)
);
CREATE UNIQUE INDEX customers_email_key ON customers (LOWER(email));

CREATE TABLE carts (
    id          TEXT PRIMARY KEY,
    customer_id TEXT NOT NULL
);
CREATE INDEX carts_customer_idx ON carts (customer_id);

CREATE TABLE cart_lines (
    cart_id           TEXT    NOT NULL REFERENCES carts (id) ON DELETE CASCADE,
    position          INTEGER NOT NULL,          -- รักษาลำดับที่ลูกค้าหยิบ
    product_id        TEXT    NOT NULL,
    name              TEXT    NOT NULL,          -- snapshot ชื่อ ณ เวลาที่หยิบ
    unit_price_satang BIGINT  NOT NULL,          -- snapshot ราคา ณ เวลาที่หยิบ
    qty               INTEGER NOT NULL,

    PRIMARY KEY (cart_id, position),
    CONSTRAINT cart_lines_qty_positive CHECK (qty > 0)
);

CREATE TABLE orders (
    id          TEXT        PRIMARY KEY,
    customer_id TEXT        NOT NULL,
    status      TEXT        NOT NULL,
    tracking    TEXT        NOT NULL DEFAULT '',
    payment_id  TEXT        NOT NULL DEFAULT '',
    created_at  TIMESTAMPTZ NOT NULL,
    updated_at  TIMESTAMPTZ NOT NULL,

    CONSTRAINT orders_status_known
        CHECK (status IN ('PLACED','PAID','PREPARING','SHIPPED','DELIVERED','CANCELLED'))
);
CREATE INDEX orders_customer_idx ON orders (customer_id);
CREATE INDEX orders_status_idx   ON orders (status);

CREATE TABLE order_lines (
    order_id          TEXT    NOT NULL REFERENCES orders (id) ON DELETE CASCADE,
    position          INTEGER NOT NULL,
    product_id        TEXT    NOT NULL,
    name              TEXT    NOT NULL,
    unit_price_satang BIGINT  NOT NULL,
    qty               INTEGER NOT NULL,

    PRIMARY KEY (order_id, position),
    CONSTRAINT order_lines_qty_positive CHECK (qty > 0)
);

CREATE TABLE payments (
    id            TEXT        PRIMARY KEY,
    order_id      TEXT        NOT NULL,
    amount_satang BIGINT      NOT NULL,
    status        TEXT        NOT NULL,
    reference     TEXT        NOT NULL DEFAULT '',
    reason        TEXT        NOT NULL DEFAULT '',
    created_at    TIMESTAMPTZ NOT NULL,
    settled_at    TIMESTAMPTZ,

    CONSTRAINT payments_status_known
        CHECK (status IN ('PENDING','SUCCEEDED','FAILED','REFUNDED'))
);
CREATE INDEX payments_order_idx ON payments (order_id);

-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DROP TABLE IF EXISTS payments;
DROP TABLE IF EXISTS order_lines;
DROP TABLE IF EXISTS orders;
DROP TABLE IF EXISTS cart_lines;
DROP TABLE IF EXISTS carts;
DROP TABLE IF EXISTS customers;
DROP TABLE IF EXISTS products;
-- +goose StatementEnd
