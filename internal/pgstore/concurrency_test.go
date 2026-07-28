package pgstore_test

import (
	"context"
	"sync"
	"testing"

	"github.com/soya196/go-shop/internal/catalog"
	"github.com/soya196/go-shop/internal/money"
)

// ═══════════════════════════════════════════════════════════════════
// 💎 เทสที่เป็นเหตุผลหลักของการย้ายมา PostgreSQL
//
// หนี้ก้อนใหญ่สุดของ go-shop เวอร์ชัน in-memory คือ oversell:
//
//	p := repo.FindByID(id)   ← goroutine A และ B อ่านค่าเดียวกัน (stock 10, reserved 0)
//	p.Reserve(1)             ← ทั้งคู่ผ่านเงื่อนไข "ของพอ"
//	repo.Save(p)             ← เขียนทับกัน → reserved = 1 ทั้งที่ควรเป็น 2
//
// เวอร์ชัน PostgreSQL ยัดเงื่อนไขลงใน UPDATE เดียว → DB ล็อกแถวให้
// เทสนี้ยิง goroutine พร้อมกันเยอะๆ แล้วนับว่าสำเร็จกี่ตัว
// ═══════════════════════════════════════════════════════════════════

func TestReserveAtomicNeverOversells(t *testing.T) {
	s := openTestStore(t)
	repo := s.Catalog()
	ctx := context.Background()

	const (
		stock   = 10  // ของมีอยู่ 10 ชิ้น
		callers = 100 // คน 100 คนแย่งกันจอง คนละ 1 ชิ้น
	)

	p, err := catalog.New("prd_hot", "SKU-HOT", "ของมันต้องมี", money.FromBaht(99), stock)
	if err != nil {
		t.Fatal(err)
	}
	if err := repo.Save(ctx, p); err != nil {
		t.Fatal(err)
	}

	var (
		wg      sync.WaitGroup
		mu      sync.Mutex
		okCount int
		errs    = map[string]int{}
	)
	start := make(chan struct{}) // ปล่อยพร้อมกันทีเดียว ให้ชนกันจริงๆ

	for i := 0; i < callers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			err := repo.ReserveAtomic(ctx, "prd_hot", 1)

			mu.Lock()
			defer mu.Unlock()
			if err == nil {
				okCount++
			} else {
				errs[err.Error()]++
			}
		}()
	}
	close(start)
	wg.Wait()

	if okCount != stock {
		t.Fatalf("🚨 oversell! จองสำเร็จ %d ครั้ง ทั้งที่มีของแค่ %d ชิ้น\nerror ที่เจอ: %v",
			okCount, stock, errs)
	}

	// ยืนยันกับข้อมูลจริงในตาราง ไม่ใช่เชื่อแค่ตัวนับ
	got, err := repo.FindByID(ctx, "prd_hot")
	if err != nil {
		t.Fatal(err)
	}
	if got.Reserved != stock || got.Available() != 0 {
		t.Fatalf("สถานะสต็อกเพี้ยน: reserved=%d available=%d (ควรเป็น %d / 0)",
			got.Reserved, got.Available(), stock)
	}
	t.Logf("✅ ยิงพร้อมกัน %d ครั้ง สำเร็จ %d (= สต็อกพอดี) · ที่เหลือถูกปฏิเสธด้วย ErrOutOfStock",
		callers, okCount)
}

// TestReleaseAtomicRoundTrip ตรวจว่าคืนของที่จองแล้วสต็อกกลับมาถูก
func TestReleaseAtomicRoundTrip(t *testing.T) {
	s := openTestStore(t)
	repo := s.Catalog()
	ctx := context.Background()

	p, _ := catalog.New("prd_r", "SKU-R", "ของทดสอบ", money.FromBaht(50), 5)
	if err := repo.Save(ctx, p); err != nil {
		t.Fatal(err)
	}

	if err := repo.ReserveAtomic(ctx, "prd_r", 3); err != nil {
		t.Fatal(err)
	}
	if err := repo.ReleaseAtomic(ctx, "prd_r", 2); err != nil {
		t.Fatal(err)
	}
	got, _ := repo.FindByID(ctx, "prd_r")
	if got.Reserved != 1 || got.Available() != 4 {
		t.Fatalf("หลังจอง 3 คืน 2 ควรเหลือ reserved=1 available=4 แต่ได้ %d / %d",
			got.Reserved, got.Available())
	}

	// ตัดสต็อกจริงตอนส่งของ
	if err := repo.FulfilAtomic(ctx, "prd_r", 1); err != nil {
		t.Fatal(err)
	}
	after, _ := repo.FindByID(ctx, "prd_r")
	if after.Stock != 4 || after.Reserved != 0 {
		t.Fatalf("หลัง Fulfil ควรเหลือ stock=4 reserved=0 แต่ได้ %d / %d", after.Stock, after.Reserved)
	}
}

// TestReserveOnMissingProductSaysNotFound
// rows affected = 0 มีได้ 2 สาเหตุ — ต้องแยกให้ออกไม่งั้น client ได้ status ผิด
func TestReserveOnMissingProductSaysNotFound(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()

	err := s.Catalog().ReserveAtomic(ctx, "ไม่มีจริง", 1)
	if err == nil {
		t.Fatal("จองสินค้าที่ไม่มีอยู่ต้อง error")
	}
	if !isNotFound(err) {
		t.Fatalf("ต้องได้ catalog.ErrNotFound (→ 404) แต่ได้: %v", err)
	}
}

func isNotFound(err error) bool {
	for e := err; e != nil; {
		if e == catalog.ErrNotFound {
			return true
		}
		u, ok := e.(interface{ Unwrap() error })
		if !ok {
			return false
		}
		e = u.Unwrap()
	}
	return false
}
