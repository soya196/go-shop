package pgstore_test

import (
	"context"
	"database/sql"
	"os"
	"sync"
	"testing"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib"
	"github.com/pressly/goose/v3"

	"github.com/soya196/go-shop/internal/migrations"
	"github.com/soya196/go-shop/internal/pgstore"
)

// ═══════════════════════════════════════════════════════════════════
// ตัวช่วยสำหรับเทสที่ต้องใช้ฐานข้อมูลจริง
//
// ทำไมไม่ใช้ testcontainers-go:
//   โปรเจกต์นี้มี docker-compose อยู่แล้ว (make db-up) การรับ dependency
//   อีก ~40 module เพื่อความสะดวกที่ได้อยู่แล้วไม่คุ้ม
//   → เทสอ่าน DSN จาก env · ถ้าต่อไม่ติดก็ข้ามพร้อมบอกวิธีแก้
//
// รันเทสชุดนี้:
//   make db-up
//   go test ./internal/pgstore/...
//
// เทสจะรัน migration ให้เองอัตโนมัติ ไม่ต้อง make migrate ก่อน
// ═══════════════════════════════════════════════════════════════════

const defaultTestDSN = "postgres://shop:shop@localhost:5433/shop?sslmode=disable"

var (
	migrateOnce sync.Once
	migrateErr  error
)

func testDSN() string {
	if v := os.Getenv("TEST_DATABASE_URL"); v != "" {
		return v
	}
	return defaultTestDSN
}

// openTestStore ต่อฐานข้อมูล รัน migration แล้วล้างข้อมูลให้เอี่ยม
//
// ถ้าต่อไม่ติด → t.Skip พร้อมบอกว่าต้องทำอะไร (ไม่ใช่ t.Fatal
// เพราะคนที่ไม่มี docker ควรรันเทสส่วนอื่นได้ตามปกติ)
func openTestStore(t *testing.T) *pgstore.Store {
	t.Helper()

	dsn := testDSN()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	store, err := pgstore.Open(ctx, dsn)
	if err != nil {
		t.Skipf("ข้ามเทสที่ต้องใช้ PostgreSQL — ต่อ %s ไม่ได้\n"+
			"  วิธีเปิด: make db-up   (หรือตั้ง env TEST_DATABASE_URL)\n"+
			"  สาเหตุ: %v", dsn, err)
	}
	t.Cleanup(store.Close)

	migrateOnce.Do(func() { migrateErr = runMigrations(dsn) })
	if migrateErr != nil {
		t.Fatalf("รัน migration ไม่สำเร็จ: %v", migrateErr)
	}

	truncateAll(t, dsn)
	return store
}

func runMigrations(dsn string) error {
	db, err := sql.Open("pgx", dsn)
	if err != nil {
		return err
	}
	defer db.Close()

	goose.SetBaseFS(migrations.FS)
	goose.SetLogger(goose.NopLogger())
	if err := goose.SetDialect("postgres"); err != nil {
		return err
	}
	return goose.Up(db, ".")
}

// truncateAll ล้างทุกตารางให้เทสแต่ละตัวเริ่มจากศูนย์
//
// ใช้ TRUNCATE ไม่ใช่ DELETE เพราะเร็วกว่ามากและรีเซ็ต sequence ให้ด้วย
func truncateAll(t *testing.T, dsn string) {
	t.Helper()
	db, err := sql.Open("pgx", dsn)
	if err != nil {
		t.Fatalf("เปิด connection เพื่อล้างข้อมูลไม่ได้: %v", err)
	}
	defer db.Close()

	_, err = db.Exec(`TRUNCATE payments, order_lines, orders, cart_lines, carts, customers, products RESTART IDENTITY CASCADE`)
	if err != nil {
		t.Fatalf("ล้างข้อมูลไม่สำเร็จ: %v", err)
	}
}
