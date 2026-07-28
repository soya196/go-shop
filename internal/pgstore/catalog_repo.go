package pgstore

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"

	"github.com/soya196/go-shop/internal/catalog"
	"github.com/soya196/go-shop/internal/money"
	"github.com/soya196/go-shop/internal/pgstore/gen"
)

// Catalog implements catalog.Repository
type Catalog struct{ s *Store }

var _ catalog.Repository = (*Catalog)(nil)

func (s *Store) Catalog() *Catalog { return &Catalog{s: s} }

func (r *Catalog) FindByID(ctx context.Context, id string) (*catalog.Product, error) {
	row, err := r.s.q(ctx).GetProduct(ctx, id)
	if err != nil {
		return nil, mapErr(err, catalog.ErrNotFound, "หาสินค้า")
	}
	return toProduct(row), nil
}

func (r *Catalog) FindBySKU(ctx context.Context, sku string) (*catalog.Product, error) {
	row, err := r.s.q(ctx).GetProductBySKU(ctx, sku)
	if err != nil {
		return nil, mapErr(err, catalog.ErrNotFound, "หาสินค้าจาก SKU")
	}
	return toProduct(row), nil
}

func (r *Catalog) List(ctx context.Context) ([]*catalog.Product, error) {
	rows, err := r.s.q(ctx).ListProducts(ctx)
	if err != nil {
		return nil, fmt.Errorf("pgstore: ลิสต์สินค้า: %w", err)
	}
	out := make([]*catalog.Product, 0, len(rows))
	for _, row := range rows {
		out = append(out, toProduct(row))
	}
	return out, nil
}

func (r *Catalog) Save(ctx context.Context, p *catalog.Product) error {
	err := r.s.q(ctx).UpsertProduct(ctx, gen.UpsertProductParams{
		ID:          p.ID,
		Sku:         p.SKU,
		Name:        p.Name,
		PriceSatang: int64(p.Price),
		Stock:       int32(p.Stock),
		Reserved:    int32(p.Reserved),
		Active:      p.Active,
	})
	if err != nil {
		return fmt.Errorf("pgstore: บันทึกสินค้า %s: %w", p.ID, err)
	}
	return nil
}

// ═══════════════════════════════════════════════════════════════════
// 💎 Reserve / Release / Fulfil — เวอร์ชันที่ปลอดภัยต่อการยิงพร้อมกัน
//
// เมธอด 3 ตัวนี้ไม่ได้อยู่ใน catalog.Repository — เป็นของแถมของ adapter ตัวนี้
// composition root จะเลือกใช้ก็ต่อเมื่อ store เป็น postgres
//
// ต่างจาก catalog.Service.Reserve() ที่ทำ read-modify-write ใน Go ตรงที่
// ตัวนี้ยัดเงื่อนไขลงไปใน UPDATE เดียว → PostgreSQL ล็อกแถวให้เอง
//
// ⚠️ กฎธุรกิจยังอยู่ที่ catalog.Product.Reserve() เหมือนเดิมไม่ได้ย้ายไปไหน
//    ตรงนี้คือการบังคับซ้ำอีกชั้นที่ระดับ DB (defense in depth)
// ═══════════════════════════════════════════════════════════════════

func (r *Catalog) ReserveAtomic(ctx context.Context, productID string, qty int) error {
	n, err := r.s.q(ctx).ReserveStock(ctx, gen.ReserveStockParams{Qty: int32(qty), ID: productID})
	if err != nil {
		return fmt.Errorf("pgstore: จองสินค้า %s: %w", productID, err)
	}
	return r.checkTouched(ctx, n, productID, catalog.ErrOutOfStock)
}

func (r *Catalog) ReleaseAtomic(ctx context.Context, productID string, qty int) error {
	n, err := r.s.q(ctx).ReleaseStock(ctx, gen.ReleaseStockParams{Qty: int32(qty), ID: productID})
	if err != nil {
		return fmt.Errorf("pgstore: คืนของที่จอง %s: %w", productID, err)
	}
	return r.checkTouched(ctx, n, productID, catalog.ErrOutOfStock)
}

func (r *Catalog) FulfilAtomic(ctx context.Context, productID string, qty int) error {
	n, err := r.s.q(ctx).FulfilStock(ctx, gen.FulfilStockParams{Qty: int32(qty), ID: productID})
	if err != nil {
		return fmt.Errorf("pgstore: ตัดสต็อก %s: %w", productID, err)
	}
	return r.checkTouched(ctx, n, productID, catalog.ErrOutOfStock)
}

// checkTouched แปล "ไม่มีแถวไหนถูกแตะ" ให้เป็น error ที่ domain รู้จัก
//
// rows affected = 0 มีได้ 2 สาเหตุ ต้องแยกให้ออก ไม่งั้น client จะได้ 422 ทั้งที่ควรเป็น 404:
//   - ไม่มีสินค้าตัวนี้เลย        → ErrNotFound
//   - มีอยู่ แต่เงื่อนไขไม่ผ่าน   → ErrOutOfStock (หรือ ErrInactive)
func (r *Catalog) checkTouched(ctx context.Context, rows int64, productID string, whenBlocked error) error {
	if rows > 0 {
		return nil
	}
	p, err := r.FindByID(ctx, productID)
	if err != nil {
		return err // รวมถึง catalog.ErrNotFound
	}
	if !p.Active {
		return fmt.Errorf("%w: %s", catalog.ErrInactive, p.Name)
	}
	return fmt.Errorf("%w: %s", whenBlocked, p.Name)
}

func toProduct(row gen.Product) *catalog.Product {
	return &catalog.Product{
		ID:       row.ID,
		SKU:      row.Sku,
		Name:     row.Name,
		Price:    money.Satang(row.PriceSatang),
		Stock:    int(row.Stock),
		Reserved: int(row.Reserved),
		Active:   row.Active,
	}
}

// mapErr แปลง error ของ pgx เป็น error ที่ domain รู้จัก
//
// นี่คือหน้าที่สำคัญของ adapter: domain ไม่ควรรู้จัก pgx.ErrNoRows
func mapErr(err error, notFound error, what string) error {
	if errors.Is(err, pgx.ErrNoRows) {
		return notFound
	}
	return fmt.Errorf("pgstore: %s: %w", what, err)
}
