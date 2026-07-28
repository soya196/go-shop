package pgstore_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/soya196/go-shop/internal/cart"
	"github.com/soya196/go-shop/internal/catalog"
	"github.com/soya196/go-shop/internal/customer"
	"github.com/soya196/go-shop/internal/money"
	"github.com/soya196/go-shop/internal/order"
	"github.com/soya196/go-shop/internal/payment"
)

// เทสชุดนี้ตรวจว่า adapter ทำสัญญาของ port ได้ครบ
//
// 🔑 สังเกตว่าเทสพูดถึงแต่ type ของ domain (catalog.Product, cart.Cart, ...)
// ไม่มี pgx ไม่มี SQL โผล่มาเลย — ถ้าวันหนึ่งเปลี่ยนไปใช้ MySQL เทสชุดนี้ยังใช้ได้

func TestCatalogRoundTrip(t *testing.T) {
	s := openTestStore(t)
	repo := s.Catalog()
	ctx := context.Background()

	p, err := catalog.New("prd_1", "SKU-1", "เสื้อยืด", money.FromBaht(120), 40)
	if err != nil {
		t.Fatal(err)
	}
	if err := repo.Save(ctx, p); err != nil {
		t.Fatal(err)
	}

	got, err := repo.FindByID(ctx, "prd_1")
	if err != nil {
		t.Fatal(err)
	}
	if got.Name != "เสื้อยืด" || got.Price != money.FromBaht(120) || got.Stock != 40 {
		t.Fatalf("อ่านกลับมาไม่ตรงกับที่บันทึก: %+v", got)
	}

	if _, err := repo.FindBySKU(ctx, "SKU-1"); err != nil {
		t.Fatalf("หาด้วย SKU ไม่เจอ: %v", err)
	}

	// ไม่มีของ → ต้องได้ error ของ domain ไม่ใช่ pgx.ErrNoRows
	if _, err := repo.FindByID(ctx, "ไม่มีจริง"); !errors.Is(err, catalog.ErrNotFound) {
		t.Fatalf("ต้องได้ catalog.ErrNotFound แต่ได้ %v", err)
	}
}

func TestCustomerRoundTrip(t *testing.T) {
	s := openTestStore(t)
	repo := s.Customers()
	ctx := context.Background()

	c, err := customer.New("cus_1", "สนธยา", "sonthaya@example.com")
	if err != nil {
		t.Fatal(err)
	}
	if err := repo.Save(ctx, c); err != nil {
		t.Fatal(err)
	}

	got, err := repo.FindByEmail(ctx, "SONTHAYA@EXAMPLE.COM") // ต้องไม่สนตัวพิมพ์
	if err != nil {
		t.Fatalf("หาด้วยอีเมลแบบพิมพ์ใหญ่ไม่เจอ: %v", err)
	}
	if got.ID != "cus_1" {
		t.Fatalf("ได้ลูกค้าผิดคน: %+v", got)
	}
}

// TestCartLinesSurviveRoundTrip เป็นเทสที่สำคัญ เพราะ Save ของตะกร้า
// ต้องทำ 3 คำสั่งใน transaction เดียว (upsert หัว → ลบบรรทัดเก่า → ใส่ใหม่)
func TestCartLinesSurviveRoundTrip(t *testing.T) {
	s := openTestStore(t)
	repo := s.Carts()
	ctx := context.Background()

	c := cart.New("crt_1", "cus_1")
	if err := c.Add(cart.ProductInfo{ID: "prd_1", Name: "เสื้อยืด", Price: money.FromBaht(120), Sellable: true}, 3); err != nil {
		t.Fatal(err)
	}
	if err := c.Add(cart.ProductInfo{ID: "prd_2", Name: "กางเกง", Price: money.FromBaht(350), Sellable: true}, 1); err != nil {
		t.Fatal(err)
	}
	if err := repo.Save(ctx, c); err != nil {
		t.Fatal(err)
	}

	got, err := repo.FindByID(ctx, "crt_1")
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Lines) != 2 {
		t.Fatalf("ต้องมี 2 บรรทัด แต่ได้ %d", len(got.Lines))
	}
	if got.Lines[0].ProductID != "prd_1" || got.Lines[1].ProductID != "prd_2" {
		t.Fatalf("ลำดับบรรทัดเพี้ยน: %+v", got.Lines)
	}
	if got.Total() != c.Total() {
		t.Fatalf("ยอดรวมไม่ตรง: อ่านได้ %s ควรเป็น %s", got.Total(), c.Total())
	}

	// บันทึกซ้ำโดยเอาของออก 1 ตัว — บรรทัดเก่าต้องหายจริง ไม่ใช่ค้างอยู่
	if err := got.Remove("prd_1"); err != nil {
		t.Fatal(err)
	}
	if err := repo.Save(ctx, got); err != nil {
		t.Fatal(err)
	}
	again, err := repo.FindByID(ctx, "crt_1")
	if err != nil {
		t.Fatal(err)
	}
	if len(again.Lines) != 1 || again.Lines[0].ProductID != "prd_2" {
		t.Fatalf("ลบแล้วบรรทัดเก่ายังค้าง: %+v", again.Lines)
	}

	// หาด้วย customer ต้องเจอใบเดียวกัน
	byCus, err := repo.FindByCustomer(ctx, "cus_1")
	if err != nil || byCus.ID != "crt_1" {
		t.Fatalf("หาตะกร้าด้วย customer ไม่ได้: %v", err)
	}
}

func TestOrderRoundTripAndListing(t *testing.T) {
	s := openTestStore(t)
	repo := s.Orders()
	ctx := context.Background()
	now := time.Date(2026, 7, 28, 10, 0, 0, 0, time.UTC)

	lines := []order.Line{{ProductID: "prd_1", Name: "เสื้อยืด", UnitPrice: money.FromBaht(120), Qty: 2}}
	o, err := order.New("ord_1", "cus_1", lines, now)
	if err != nil {
		t.Fatal(err)
	}
	if err := repo.Save(ctx, o); err != nil {
		t.Fatal(err)
	}

	got, err := repo.FindByID(ctx, "ord_1")
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != order.Placed || got.Total() != money.FromBaht(240) {
		t.Fatalf("อ่านออเดอร์กลับมาไม่ตรง: %+v", got)
	}
	if !got.CreatedAt.Equal(now) {
		t.Fatalf("เวลาเพี้ยน: ได้ %v ควรเป็น %v", got.CreatedAt, now)
	}

	// เปลี่ยนสถานะแล้วบันทึกทับ
	if err := got.MarkPaid("pay_1", now.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	if err := repo.Save(ctx, got); err != nil {
		t.Fatal(err)
	}

	paid, _ := repo.FindByID(ctx, "ord_1")
	if paid.Status != order.Paid || paid.PaymentID != "pay_1" {
		t.Fatalf("บันทึกสถานะใหม่ไม่ติด: %+v", paid)
	}
	if len(paid.Lines) != 1 {
		t.Fatalf("บันทึกทับแล้วบรรทัดหาย: %+v", paid.Lines)
	}

	// กรองตามสถานะ
	byStatus, err := repo.List(ctx, order.Paid)
	if err != nil || len(byStatus) != 1 {
		t.Fatalf("กรองด้วยสถานะ PAID ไม่ได้: %v (%d ใบ)", err, len(byStatus))
	}
	all, err := repo.List(ctx, "")
	if err != nil || len(all) != 1 {
		t.Fatalf("ลิสต์ทั้งหมดไม่ได้: %v (%d ใบ)", err, len(all))
	}
	byCus, err := repo.FindByCustomer(ctx, "cus_1")
	if err != nil || len(byCus) != 1 || len(byCus[0].Lines) != 1 {
		t.Fatalf("ลิสต์ตามลูกค้าไม่ได้ หรือบรรทัดไม่ติดมา: %v", err)
	}
}

func TestPaymentRoundTrip(t *testing.T) {
	s := openTestStore(t)
	repo := s.Payments()
	ctx := context.Background()
	now := time.Date(2026, 7, 28, 10, 0, 0, 0, time.UTC)

	p, err := payment.New("pay_1", "ord_1", money.FromBaht(240), now)
	if err != nil {
		t.Fatal(err)
	}
	if err := repo.Save(ctx, p); err != nil {
		t.Fatal(err)
	}

	got, err := repo.FindByID(ctx, "pay_1")
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != payment.Pending || got.SettledAt != nil {
		t.Fatalf("รายการใหม่ต้องเป็น PENDING และยังไม่มี settled_at: %+v", got)
	}

	// จ่ายสำเร็จ → settled_at ต้องมีค่า (คอลัมน์นี้ NULL ได้)
	if err := got.Succeed("ref-123", now.Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	if err := repo.Save(ctx, got); err != nil {
		t.Fatal(err)
	}

	done, _ := repo.FindByID(ctx, "pay_1")
	if done.Status != payment.Succeeded || done.SettledAt == nil {
		t.Fatalf("บันทึกผลสำเร็จไม่ติด: %+v", done)
	}
	if !done.SettledAt.Equal(now.Add(time.Second)) {
		t.Fatalf("settled_at เพี้ยน: %v", done.SettledAt)
	}

	list, err := repo.FindByOrder(ctx, "ord_1")
	if err != nil || len(list) != 1 {
		t.Fatalf("ลิสต์การชำระเงินของออเดอร์ไม่ได้: %v", err)
	}
}
