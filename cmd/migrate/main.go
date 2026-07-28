// Command migrate รัน database migration ด้วย goose
//
//	go run ./cmd/migrate up          # อัปเดตให้เป็นเวอร์ชันล่าสุด
//	go run ./cmd/migrate status      # ดูว่ารันไปถึงไหนแล้ว
//	go run ./cmd/migrate down        # ถอย 1 ขั้น
//	go run ./cmd/migrate reset       # ถอยทั้งหมด (dev เท่านั้น)
//
// อ่าน DSN จาก -dsn หรือ env DATABASE_URL
package main

import (
	"context"
	"database/sql"
	"flag"
	"fmt"
	"os"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib" // driver "pgx" สำหรับ database/sql
	"github.com/pressly/goose/v3"

	"github.com/soya196/go-shop/internal/migrations"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "❌", err)
		os.Exit(1)
	}
}

func run() error {
	dsn := flag.String("dsn", env("DATABASE_URL", ""), "PostgreSQL DSN (หรือตั้ง env DATABASE_URL)")
	timeout := flag.Duration("timeout", 30*time.Second, "timeout ของทั้งคำสั่ง")
	flag.Parse()

	cmd := flag.Arg(0)
	if cmd == "" {
		cmd = "up"
	}
	if *dsn == "" {
		return fmt.Errorf("ต้องระบุ -dsn หรือ env DATABASE_URL\n" +
			"ตัวอย่าง: postgres://shop:shop@localhost:5433/shop?sslmode=disable")
	}

	ctx, cancel := context.WithTimeout(context.Background(), *timeout)
	defer cancel()

	// ใช้ database/sql ตรงนี้เพราะ goose ต้องการ *sql.DB
	// ส่วนตัวแอปจริงใช้ pgxpool (เร็วกว่า) — คนละ connection กัน ไม่ปนกัน
	db, err := sql.Open("pgx", *dsn)
	if err != nil {
		return fmt.Errorf("เปิด connection ไม่ได้: %w", err)
	}
	defer db.Close()

	if err := db.PingContext(ctx); err != nil {
		return fmt.Errorf("ต่อฐานข้อมูลไม่ได้: %w\nรัน `docker compose up -d db` แล้วหรือยัง?", err)
	}

	goose.SetBaseFS(migrations.FS)
	if err := goose.SetDialect("postgres"); err != nil {
		return err
	}

	if err := goose.RunContext(ctx, cmd, db, "."); err != nil {
		return fmt.Errorf("goose %s: %w", cmd, err)
	}
	return nil
}

func env(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}
