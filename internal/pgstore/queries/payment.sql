-- name: GetPayment :one
SELECT * FROM payments WHERE id = $1;

-- name: ListPaymentsByOrder :many
SELECT * FROM payments WHERE order_id = $1 ORDER BY created_at, id;

-- name: UpsertPayment :exec
INSERT INTO payments (id, order_id, amount_satang, status, reference, reason, created_at, settled_at)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8)
ON CONFLICT (id) DO UPDATE SET
    order_id      = EXCLUDED.order_id,
    amount_satang = EXCLUDED.amount_satang,
    status        = EXCLUDED.status,
    reference     = EXCLUDED.reference,
    reason        = EXCLUDED.reason,
    settled_at    = EXCLUDED.settled_at;
