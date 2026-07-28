-- name: GetProduct :one
SELECT * FROM products WHERE id = $1;

-- name: GetProductBySKU :one
SELECT * FROM products WHERE sku = $1;

-- name: ListProducts :many
SELECT * FROM products ORDER BY id;

-- name: UpsertProduct :exec
INSERT INTO products (id, sku, name, price_satang, stock, reserved, active)
VALUES ($1, $2, $3, $4, $5, $6, $7)
ON CONFLICT (id) DO UPDATE SET
    sku          = EXCLUDED.sku,
    name         = EXCLUDED.name,
    price_satang = EXCLUDED.price_satang,
    stock        = EXCLUDED.stock,
    reserved     = EXCLUDED.reserved,
    active       = EXCLUDED.active;

-- ═══════════════════════════════════════════════════════════════
-- 💎 คำสั่งที่แก้ปัญหา oversell
--
-- ของเดิม (in-memory) ทำแบบ read-modify-write ใน Go:
--     p := repo.FindByID(id)      ← goroutine A และ B อ่านค่าเดียวกัน
--     p.Reserve(qty)              ← ทั้งคู่ผ่านเงื่อนไข
--     repo.Save(p)                ← เขียนทับกัน → จองเกินสต็อก
--
-- ของใหม่: เงื่อนไขอยู่ใน UPDATE เดียว → PostgreSQL ล็อกแถวให้เอง
-- ตัวที่มาทีหลังจะเห็นค่าที่อัปเดตแล้ว → แถวไม่โดนแตะ → rows affected = 0
--
-- ⚠️ กฎธุรกิจ "จองได้เมื่อของพอ" ยังอยู่ที่ catalog.Product.Reserve() เหมือนเดิม
--    ตรงนี้คือการบังคับซ้ำที่ระดับ DB ไม่ใช่การย้ายกฎออกจาก domain
-- ═══════════════════════════════════════════════════════════════

-- name: ReserveStock :execrows
UPDATE products
SET reserved = reserved + @qty::int
WHERE id = @id
  AND active = TRUE
  AND stock - reserved >= @qty::int;

-- name: ReleaseStock :execrows
UPDATE products
SET reserved = reserved - @qty::int
WHERE id = @id
  AND reserved >= @qty::int;

-- name: FulfilStock :execrows
UPDATE products
SET stock    = stock - @qty::int,
    reserved = reserved - @qty::int
WHERE id = @id
  AND reserved >= @qty::int
  AND stock >= @qty::int;
