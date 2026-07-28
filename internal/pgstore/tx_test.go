package pgstore_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/soya196/go-shop/internal/catalog"
	"github.com/soya196/go-shop/internal/money"
	"github.com/soya196/go-shop/internal/order"
	"github.com/soya196/go-shop/internal/pgstore"
)

// ═══════════════════════════════════════════════════════════════════
// 💎 เทสที่พิสูจน์คุณค่าของ TxManager
//
// สถานการณ์: order.Service.Place ทำงานถึงขั้นสุดท้ายแล้วพัง
//   ตรวจสิทธิ์ ✅ → จองของ ✅ → สร้าง entity ✅ → บันทึกออเดอร์ ✅ → นับออเดอร์ค้าง ❌
//
// สิ่งที่ compensating action (releaseAll) ทำได้: คืนของที่จอง
// สิ่งที่มัน **ทำไม่ได้**: ลบออเดอร์ที่บันทึกไปแล้ว
//   → ได้ "ออเดอร์ผี" ค้างในระบบ ที่ลูกค้าไม่รู้ว่ามี และไม่มีใครตามเก็บ
//
// เทสนี้รันโค้ด domain ตัวเดียวกันเป๊ะ 2 รอบ ต่างกันแค่ TxManager ที่เสียบเข้าไป
// ═══════════════════════════════════════════════════════════════════

var errBoom = errors.New("จำลองพัง: ระบบลูกค้าไม่ตอบ")

// shoppersThatFailAtTheEnd ผ่านด่านแรก แต่พังตอนขั้นสุดท้าย
type shoppersThatFailAtTheEnd struct{}

func (shoppersThatFailAtTheEnd) EnsureCanOrder(context.Context, string) error { return nil }
func (shoppersThatFailAtTheEnd) OrderOpened(context.Context, string) error    { return errBoom }
func (shoppersThatFailAtTheEnd) OrderClosed(context.Context, string) error    { return nil }

// pgStock ต่อ order.Stock เข้ากับคำสั่ง atomic ของ pgstore
type pgStock struct{ c *pgstore.Catalog }

func (s pgStock) Reserve(ctx context.Context, id string, qty int) error {
	return s.c.ReserveAtomic(ctx, id, qty)
}
func (s pgStock) Release(ctx context.Context, id string, qty int) error {
	return s.c.ReleaseAtomic(ctx, id, qty)
}
func (s pgStock) Fulfil(ctx context.Context, id string, qty int) error {
	return s.c.FulfilAtomic(ctx, id, qty)
}

type noWallet struct{}

func (noWallet) Collect(context.Context, string, money.Satang) (string, error) { return "", nil }
func (noWallet) RefundOrder(context.Context, string) error                     { return nil }

type fixedID struct{ v string }

func (f fixedID) NewID() string { return f.v }

type fixedClock struct{ t time.Time }

func (f fixedClock) Now() time.Time { return f.t }

// noTx เลียนแบบ store ที่ไม่มี transaction (memory / json)
type noTx struct{}

func (noTx) Do(ctx context.Context, fn func(context.Context) error) error { return fn(ctx) }

// buildOrderService ประกอบ order.Service โดยใช้ pgstore เป็นของจริง
// แล้วเสียบ TxManager ที่ส่งเข้ามา — นี่คือตัวแปรเดียวที่ต่างกันระหว่าง 2 เทส
func buildOrderService(s *pgstore.Store, id string, tx order.TxManager) *order.Service {
	return order.NewService(
		s.Orders(),
		pgStock{c: s.Catalog()},
		shoppersThatFailAtTheEnd{},
		noWallet{},
		fixedID{v: id},
		fixedClock{t: time.Date(2026, 7, 28, 12, 0, 0, 0, time.UTC)},
		tx,
	)
}

func seedHotProduct(t *testing.T, s *pgstore.Store, id string, stock int) {
	t.Helper()
	p, err := catalog.New(id, "SKU-"+id, "สินค้าทดสอบ", money.FromBaht(100), stock)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Catalog().Save(context.Background(), p); err != nil {
		t.Fatal(err)
	}
}

// ✅ ด้วย transaction จริง: พังแล้วต้องไม่เหลืออะไรเลย
func TestRealTransactionLeavesNothingBehind(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	seedHotProduct(t, s, "prd_tx", 10)

	svc := buildOrderService(s, "ord_ghost_tx", s.Tx()) // ← transaction จริง
	lines := []order.Line{{ProductID: "prd_tx", Name: "สินค้าทดสอบ", UnitPrice: money.FromBaht(100), Qty: 3}}

	_, err := svc.Place(ctx, "cus_1", lines)
	if !errors.Is(err, errBoom) {
		t.Fatalf("ควรพังด้วย errBoom แต่ได้: %v", err)
	}

	// 1) ต้องไม่มีออเดอร์ผีค้างอยู่
	if _, err := s.Orders().FindByID(ctx, "ord_ghost_tx"); !errors.Is(err, order.ErrNotFound) {
		t.Fatalf("🚨 มีออเดอร์ผีค้างในระบบ — transaction ไม่ได้ rollback (err=%v)", err)
	}

	// 2) ของที่จองต้องคืนหมด
	p, err := s.Catalog().FindByID(ctx, "prd_tx")
	if err != nil {
		t.Fatal(err)
	}
	if p.Reserved != 0 || p.Available() != 10 {
		t.Fatalf("ของยังค้างจอง: reserved=%d available=%d (ควรเป็น 0 / 10)", p.Reserved, p.Available())
	}
	t.Log("✅ transaction จริง: ไม่มีออเดอร์ผี · ของคืนครบ")
}

// ⚠️ ไม่มี transaction: ของคืนได้ (compensating action) แต่ออเดอร์ผีค้าง
//
// เทสนี้ "ยืนยันข้อจำกัด" ไม่ใช่ยืนยันความถูกต้อง — เขียนไว้ให้เห็นชัดว่า
// เลือก store ผิดแล้วเจออะไร และทำไม postgres ถึงไม่ใช่แค่ของประดับ
func TestWithoutTransactionLeavesGhostOrder(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	seedHotProduct(t, s, "prd_notx", 10)

	svc := buildOrderService(s, "ord_ghost_notx", noTx{}) // ← ไม่มี transaction
	lines := []order.Line{{ProductID: "prd_notx", Name: "สินค้าทดสอบ", UnitPrice: money.FromBaht(100), Qty: 3}}

	_, err := svc.Place(ctx, "cus_1", lines)
	if !errors.Is(err, errBoom) {
		t.Fatalf("ควรพังด้วย errBoom แต่ได้: %v", err)
	}

	// compensating action ทำงาน → ของคืนได้
	p, _ := s.Catalog().FindByID(ctx, "prd_notx")
	if p.Reserved != 0 {
		t.Fatalf("compensating action ไม่ทำงาน: reserved=%d", p.Reserved)
	}

	// แต่ออเดอร์ที่บันทึกไปแล้วยังอยู่ — releaseAll ลบให้ไม่ได้
	ghost, err := s.Orders().FindByID(ctx, "ord_ghost_notx")
	if err != nil {
		t.Fatalf("คาดว่าจะเจอออเดอร์ผี (ข้อจำกัดของ store ที่ไม่มี transaction) แต่ไม่เจอ: %v", err)
	}
	t.Logf("⚠️ ตามคาด: ของคืนครบ (reserved=0) แต่เหลือออเดอร์ผี %s สถานะ %s ที่ไม่มีใครเป็นเจ้าของ",
		ghost.ID, ghost.Status)
}
