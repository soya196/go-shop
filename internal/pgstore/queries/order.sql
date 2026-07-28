-- name: GetOrder :one
SELECT * FROM orders WHERE id = $1;

-- name: ListOrdersByCustomer :many
SELECT * FROM orders WHERE customer_id = $1 ORDER BY created_at DESC, id;

-- name: ListOrders :many
-- sqlc.narg ทำให้ส่ง NULL มาแปลว่า "เอาทุกสถานะ" — ไม่ต้องเขียน query สองตัว
SELECT * FROM orders
WHERE (sqlc.narg('status')::text IS NULL OR status = sqlc.narg('status')::text)
ORDER BY created_at DESC, id;

-- name: ListOrderLines :many
SELECT * FROM order_lines WHERE order_id = $1 ORDER BY position;

-- name: ListOrderLinesFor :many
-- ดึงบรรทัดของหลายออเดอร์ในคำสั่งเดียว — กัน N+1 ตอน List
SELECT * FROM order_lines WHERE order_id = ANY(@order_ids::text[]) ORDER BY order_id, position;

-- name: UpsertOrder :exec
INSERT INTO orders (id, customer_id, status, tracking, payment_id, created_at, updated_at)
VALUES ($1, $2, $3, $4, $5, $6, $7)
ON CONFLICT (id) DO UPDATE SET
    customer_id = EXCLUDED.customer_id,
    status      = EXCLUDED.status,
    tracking    = EXCLUDED.tracking,
    payment_id  = EXCLUDED.payment_id,
    updated_at  = EXCLUDED.updated_at;

-- name: DeleteOrderLines :exec
DELETE FROM order_lines WHERE order_id = $1;

-- name: InsertOrderLine :exec
INSERT INTO order_lines (order_id, position, product_id, name, unit_price_satang, qty)
VALUES ($1, $2, $3, $4, $5, $6);
