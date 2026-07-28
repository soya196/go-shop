// Package pgstore เป็น driven adapter ที่เก็บของลง PostgreSQL
//
// ทิศทางการพึ่งพา: pgstore → domain (ชั้นนอกมองเข้าไปเห็น entity ตรงๆ = Onion)
// ในทางกลับกัน domain ไม่มีบรรทัด import "pgstore" หรือ "pgx" เลยสักที่ (= Hexagonal)
// ตรวจได้ด้วย: go run ./cmd/archlint
//
// # หน้าที่ของ package นี้มี 3 อย่างเท่านั้น
//
//  1. เรียก query ที่ sqlc สร้างไว้ให้ (internal/pgstore/gen — ห้ามแก้ด้วยมือ)
//  2. แปลง struct ของ gen ↔ entity ของ domain
//  3. แปลง error ของ pgx เป็น error ที่ domain รู้จัก (เช่น pgx.ErrNoRows → catalog.ErrNotFound)
//
// ❌ ห้ามมีกฎธุรกิจในนี้ — ถ้าเจอ if ที่ตัดสินเรื่องธุรกิจ แปลว่ามีของหลุดออกมาจาก domain
package pgstore

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/soya196/go-shop/internal/pgstore/gen"
)

// Store ถือ connection pool และเป็นโรงงานผลิต repository ทุกตัว
type Store struct {
	pool *pgxpool.Pool
}

// Open ต่อฐานข้อมูลแล้วเช็คว่าต่อติดจริง
//
// ตั้งค่า pool ไว้แบบระวังตัว: MaxConns ต่ำกว่าที่ PostgreSQL รับได้เสมอ
// เพราะแอปหลาย instance รวมกันแล้วต้องไม่เกิน max_connections ของ DB
func Open(ctx context.Context, dsn string) (*Store, error) {
	cfg, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		return nil, fmt.Errorf("pgstore: DSN ไม่ถูกต้อง: %w", err)
	}
	if cfg.MaxConns == 0 {
		cfg.MaxConns = 10
	}

	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		return nil, fmt.Errorf("pgstore: สร้าง pool ไม่ได้: %w", err)
	}
	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		return nil, fmt.Errorf("pgstore: ต่อฐานข้อมูลไม่ได้: %w", err)
	}
	return &Store{pool: pool}, nil
}

// Close ปิด pool
func (s *Store) Close() { s.pool.Close() }

// Pool เปิดให้ composition root เข้าถึงได้ (เช่นเอาไปทำ health check)
func (s *Store) Pool() *pgxpool.Pool { return s.pool }

// ═══════════════════════════════════════════════════════════════════
// transaction — หัวใจของ adapter ตัวนี้
//
// ปัญหา: repository แต่ละตัวไม่รู้จักกัน แล้วจะทำให้หลาย repo
// อยู่ใน transaction เดียวกันได้ยังไง โดยที่ domain ไม่ต้องรู้จัก pgx?
//
// คำตอบ: พา transaction ไปกับ context
//   - withinTx() เปิด tx แล้วยัดลง context
//   - repo ทุกตัวเรียก q(ctx) ซึ่งจะหยิบ tx จาก context ถ้ามี
//   - ถ้าไม่มี ก็ใช้ pool ตามปกติ (auto-commit ทีละคำสั่ง)
//
// ผล: โค้ดของ repo หน้าตาเหมือนเดิมทุกประการ ไม่ต้องมี parameter พิเศษ
// ═══════════════════════════════════════════════════════════════════

type txKey struct{}

// q คืนตัวเรียก query ที่ผูกกับ transaction ปัจจุบัน (ถ้าอยู่ใน tx) หรือ pool (ถ้าไม่อยู่)
func (s *Store) q(ctx context.Context) *gen.Queries {
	if tx, ok := ctx.Value(txKey{}).(pgx.Tx); ok {
		return gen.New(tx)
	}
	return gen.New(s.pool)
}

// Tx คืนตัวจัดการ transaction สำหรับให้ domain ใช้ผ่าน port order.TxManager
//
// สังเกตว่า pgstore ไม่ได้ import order เพื่อประกาศว่า "implement interface นี้"
// — Go ใช้ structural typing เมธอด Do ที่หน้าตาตรงกันก็พอแล้ว
func (s *Store) Tx() TxRunner { return TxRunner{s: s} }

// TxRunner ทำหน้าที่เปิด/ปิด transaction จริงบน PostgreSQL
type TxRunner struct{ s *Store }

// Do รัน fn ใน transaction เดียว — สำเร็จทั้งหมดหรือ rollback ทั้งหมด
func (t TxRunner) Do(ctx context.Context, fn func(ctx context.Context) error) error {
	return t.s.withinTx(ctx, fn)
}

// withinTx รัน fn ใน transaction เดียว
//
// ถ้า ctx อยู่ใน transaction อยู่แล้ว จะ "เข้าร่วม" อันเดิมไม่เปิดซ้อน
// (กันเคส repo.Save ที่มี tx ในตัว ถูกเรียกจาก use case ที่เปิด tx ไว้แล้ว)
func (s *Store) withinTx(ctx context.Context, fn func(context.Context) error) error {
	if _, already := ctx.Value(txKey{}).(pgx.Tx); already {
		return fn(ctx) // เข้าร่วมอันเดิม — commit/rollback เป็นหน้าที่ของคนที่เปิด
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("pgstore: เปิด transaction ไม่ได้: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }() // no-op ถ้า commit สำเร็จไปแล้ว

	if err := fn(context.WithValue(ctx, txKey{}, tx)); err != nil {
		return err
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("pgstore: commit ไม่สำเร็จ: %w", err)
	}
	return nil
}
