package pgstore

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5/pgtype"

	"github.com/soya196/go-shop/internal/money"
	"github.com/soya196/go-shop/internal/order"
	"github.com/soya196/go-shop/internal/pgstore/gen"
)

// Orders implements order.Repository
type Orders struct{ s *Store }

var _ order.Repository = (*Orders)(nil)

func (s *Store) Orders() *Orders { return &Orders{s: s} }

func (r *Orders) FindByID(ctx context.Context, id string) (*order.Order, error) {
	head, err := r.s.q(ctx).GetOrder(ctx, id)
	if err != nil {
		return nil, mapErr(err, order.ErrNotFound, "หาออเดอร์")
	}
	lines, err := r.s.q(ctx).ListOrderLines(ctx, id)
	if err != nil {
		return nil, fmt.Errorf("pgstore: อ่านรายการในออเดอร์ %s: %w", id, err)
	}
	return toOrder(head, lines), nil
}

func (r *Orders) FindByCustomer(ctx context.Context, customerID string) ([]*order.Order, error) {
	heads, err := r.s.q(ctx).ListOrdersByCustomer(ctx, customerID)
	if err != nil {
		return nil, fmt.Errorf("pgstore: ลิสต์ออเดอร์ของลูกค้า %s: %w", customerID, err)
	}
	return r.attachLines(ctx, heads)
}

func (r *Orders) List(ctx context.Context, status order.Status) ([]*order.Order, error) {
	// status ว่าง = เอาทุกสถานะ → ส่ง NULL ให้ query (ดู sqlc.narg ใน order.sql)
	var filter *string
	if status != "" {
		s := string(status)
		filter = &s
	}
	heads, err := r.s.q(ctx).ListOrders(ctx, filter)
	if err != nil {
		return nil, fmt.Errorf("pgstore: ลิสต์ออเดอร์: %w", err)
	}
	return r.attachLines(ctx, heads)
}

// attachLines ดึงบรรทัดของหลายออเดอร์ใน query เดียว
//
// 🔑 ถ้าวนเรียก ListOrderLines ทีละออเดอร์ = ปัญหา N+1 คลาสสิก
// ออเดอร์ 100 ใบ = 101 query · ที่นี่ใช้ WHERE order_id = ANY(...) → 2 query จบ
func (r *Orders) attachLines(ctx context.Context, heads []gen.Order) ([]*order.Order, error) {
	if len(heads) == 0 {
		return []*order.Order{}, nil
	}
	ids := make([]string, 0, len(heads))
	for _, h := range heads {
		ids = append(ids, h.ID)
	}
	rows, err := r.s.q(ctx).ListOrderLinesFor(ctx, ids)
	if err != nil {
		return nil, fmt.Errorf("pgstore: อ่านรายการของหลายออเดอร์: %w", err)
	}
	byOrder := make(map[string][]gen.OrderLine, len(heads))
	for _, l := range rows {
		byOrder[l.OrderID] = append(byOrder[l.OrderID], l)
	}
	out := make([]*order.Order, 0, len(heads))
	for _, h := range heads {
		out = append(out, toOrder(h, byOrder[h.ID]))
	}
	return out, nil
}

// Save บันทึกออเดอร์ทั้งใบ — เหตุผลเดียวกับ Carts.Save (ดูคอมเมนต์ที่นั่น)
func (r *Orders) Save(ctx context.Context, o *order.Order) error {
	return r.s.withinTx(ctx, func(ctx context.Context) error {
		q := r.s.q(ctx)

		err := q.UpsertOrder(ctx, gen.UpsertOrderParams{
			ID:         o.ID,
			CustomerID: o.CustomerID,
			Status:     string(o.Status),
			Tracking:   o.Tracking,
			PaymentID:  o.PaymentID,
			CreatedAt:  ts(o.CreatedAt),
			UpdatedAt:  ts(o.UpdatedAt),
		})
		if err != nil {
			return fmt.Errorf("pgstore: บันทึกออเดอร์ %s: %w", o.ID, err)
		}
		if err := q.DeleteOrderLines(ctx, o.ID); err != nil {
			return fmt.Errorf("pgstore: ล้างรายการเก่าในออเดอร์ %s: %w", o.ID, err)
		}
		for i, l := range o.Lines {
			err := q.InsertOrderLine(ctx, gen.InsertOrderLineParams{
				OrderID:         o.ID,
				Position:        int32(i),
				ProductID:       l.ProductID,
				Name:            l.Name,
				UnitPriceSatang: int64(l.UnitPrice),
				Qty:             int32(l.Qty),
			})
			if err != nil {
				return fmt.Errorf("pgstore: ใส่รายการ %s ลงออเดอร์ %s: %w", l.ProductID, o.ID, err)
			}
		}
		return nil
	})
}

func toOrder(head gen.Order, rows []gen.OrderLine) *order.Order {
	lines := make([]order.Line, 0, len(rows))
	for _, l := range rows {
		lines = append(lines, order.Line{
			ProductID: l.ProductID,
			Name:      l.Name,
			UnitPrice: money.Satang(l.UnitPriceSatang),
			Qty:       int(l.Qty),
		})
	}
	return &order.Order{
		ID:         head.ID,
		CustomerID: head.CustomerID,
		Lines:      lines,
		Status:     order.Status(head.Status),
		Tracking:   head.Tracking,
		PaymentID:  head.PaymentID,
		CreatedAt:  head.CreatedAt.Time,
		UpdatedAt:  head.UpdatedAt.Time,
	}
}

// ts แปลง time.Time → pgtype.Timestamptz (ชนิดที่ pgx ใช้แทน sql.NullTime)
func ts(t time.Time) pgtype.Timestamptz {
	return pgtype.Timestamptz{Time: t, Valid: !t.IsZero()}
}

// tsPtr สำหรับคอลัมน์ที่เป็น NULL ได้ (เช่น payments.settled_at)
func tsPtr(t *time.Time) pgtype.Timestamptz {
	if t == nil {
		return pgtype.Timestamptz{}
	}
	return ts(*t)
}
