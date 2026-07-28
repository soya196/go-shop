-- name: GetCustomer :one
SELECT * FROM customers WHERE id = $1;

-- name: GetCustomerByEmail :one
SELECT * FROM customers WHERE LOWER(email) = LOWER($1);

-- name: ListCustomers :many
SELECT * FROM customers ORDER BY id;

-- name: UpsertCustomer :exec
INSERT INTO customers (id, name, email, suspended, open_orders)
VALUES ($1, $2, $3, $4, $5)
ON CONFLICT (id) DO UPDATE SET
    name        = EXCLUDED.name,
    email       = EXCLUDED.email,
    suspended   = EXCLUDED.suspended,
    open_orders = EXCLUDED.open_orders;
