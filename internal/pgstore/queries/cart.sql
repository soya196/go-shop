-- name: GetCart :one
SELECT * FROM carts WHERE id = $1;

-- name: GetCartByCustomer :one
SELECT * FROM carts WHERE customer_id = $1 ORDER BY id LIMIT 1;

-- name: ListCartLines :many
SELECT * FROM cart_lines WHERE cart_id = $1 ORDER BY position;

-- name: UpsertCart :exec
INSERT INTO carts (id, customer_id)
VALUES ($1, $2)
ON CONFLICT (id) DO UPDATE SET customer_id = EXCLUDED.customer_id;

-- 🔑 บันทึกตะกร้า = ลบบรรทัดเก่าทั้งหมด แล้วใส่ใหม่
--    สองคำสั่งนี้ต้องอยู่ใน transaction เดียวกันเสมอ ไม่งั้นตะกร้าจะว่างชั่วขณะ
--    → นี่คือเหตุผลที่ต้องมี TxManager (ดู internal/pgstore/tx.go)

-- name: DeleteCartLines :exec
DELETE FROM cart_lines WHERE cart_id = $1;

-- name: InsertCartLine :exec
INSERT INTO cart_lines (cart_id, position, product_id, name, unit_price_satang, qty)
VALUES ($1, $2, $3, $4, $5, $6);

-- name: DeleteCart :exec
DELETE FROM carts WHERE id = $1;
