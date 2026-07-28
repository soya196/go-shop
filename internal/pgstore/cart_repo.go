package pgstore

import (
	"context"
	"fmt"

	"github.com/soya196/go-shop/internal/cart"
	"github.com/soya196/go-shop/internal/money"
	"github.com/soya196/go-shop/internal/pgstore/gen"
)

// Carts implements cart.Repository
type Carts struct{ s *Store }

var _ cart.Repository = (*Carts)(nil)

func (s *Store) Carts() *Carts { return &Carts{s: s} }

func (r *Carts) FindByID(ctx context.Context, id string) (*cart.Cart, error) {
	head, err := r.s.q(ctx).GetCart(ctx, id)
	if err != nil {
		return nil, mapErr(err, cart.ErrNotFound, "หาตะกร้า")
	}
	return r.withLines(ctx, head)
}

func (r *Carts) FindByCustomer(ctx context.Context, customerID string) (*cart.Cart, error) {
	head, err := r.s.q(ctx).GetCartByCustomer(ctx, customerID)
	if err != nil {
		return nil, mapErr(err, cart.ErrNotFound, "หาตะกร้าของลูกค้า")
	}
	return r.withLines(ctx, head)
}

func (r *Carts) withLines(ctx context.Context, head gen.Cart) (*cart.Cart, error) {
	rows, err := r.s.q(ctx).ListCartLines(ctx, head.ID)
	if err != nil {
		return nil, fmt.Errorf("pgstore: อ่านรายการในตะกร้า %s: %w", head.ID, err)
	}
	lines := make([]cart.Line, 0, len(rows))
	for _, l := range rows {
		lines = append(lines, cart.Line{
			ProductID: l.ProductID,
			Name:      l.Name,
			UnitPrice: money.Satang(l.UnitPriceSatang),
			Qty:       int(l.Qty),
		})
	}
	return &cart.Cart{ID: head.ID, CustomerID: head.CustomerID, Lines: lines}, nil
}

// Save บันทึกตะกร้าทั้งใบ
//
// 🔑 ต้องอยู่ใน transaction เสมอ เพราะเป็น 3 คำสั่งที่ต้องสำเร็จหรือล้มพร้อมกัน:
// upsert หัวตะกร้า → ลบบรรทัดเก่าทั้งหมด → ใส่บรรทัดใหม่
//
// ถ้าไม่มี transaction แล้ว process ตายหลัง DELETE ลูกค้าจะเปิดมาเจอตะกร้าว่าง
//
// withinTx จะ "เข้าร่วม" transaction ที่เปิดอยู่แล้วถ้ามี ไม่เปิดซ้อน
func (r *Carts) Save(ctx context.Context, c *cart.Cart) error {
	return r.s.withinTx(ctx, func(ctx context.Context) error {
		q := r.s.q(ctx)

		if err := q.UpsertCart(ctx, gen.UpsertCartParams{ID: c.ID, CustomerID: c.CustomerID}); err != nil {
			return fmt.Errorf("pgstore: บันทึกตะกร้า %s: %w", c.ID, err)
		}
		if err := q.DeleteCartLines(ctx, c.ID); err != nil {
			return fmt.Errorf("pgstore: ล้างรายการเก่าในตะกร้า %s: %w", c.ID, err)
		}
		for i, l := range c.Lines {
			err := q.InsertCartLine(ctx, gen.InsertCartLineParams{
				CartID:          c.ID,
				Position:        int32(i),
				ProductID:       l.ProductID,
				Name:            l.Name,
				UnitPriceSatang: int64(l.UnitPrice),
				Qty:             int32(l.Qty),
			})
			if err != nil {
				return fmt.Errorf("pgstore: ใส่รายการ %s ลงตะกร้า %s: %w", l.ProductID, c.ID, err)
			}
		}
		return nil
	})
}

func (r *Carts) Delete(ctx context.Context, id string) error {
	// cart_lines มี ON DELETE CASCADE → ลบหัวแล้วบรรทัดหายตาม
	if err := r.s.q(ctx).DeleteCart(ctx, id); err != nil {
		return fmt.Errorf("pgstore: ลบตะกร้า %s: %w", id, err)
	}
	return nil
}
