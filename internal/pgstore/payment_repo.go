package pgstore

import (
	"context"
	"fmt"

	"github.com/soya196/go-shop/internal/money"
	"github.com/soya196/go-shop/internal/payment"
	"github.com/soya196/go-shop/internal/pgstore/gen"
)

// Payments implements payment.Repository
type Payments struct{ s *Store }

var _ payment.Repository = (*Payments)(nil)

func (s *Store) Payments() *Payments { return &Payments{s: s} }

func (r *Payments) FindByID(ctx context.Context, id string) (*payment.Payment, error) {
	row, err := r.s.q(ctx).GetPayment(ctx, id)
	if err != nil {
		return nil, mapErr(err, payment.ErrNotFound, "หารายการชำระเงิน")
	}
	return toPayment(row), nil
}

func (r *Payments) FindByOrder(ctx context.Context, orderID string) ([]*payment.Payment, error) {
	rows, err := r.s.q(ctx).ListPaymentsByOrder(ctx, orderID)
	if err != nil {
		return nil, fmt.Errorf("pgstore: ลิสต์การชำระเงินของออเดอร์ %s: %w", orderID, err)
	}
	out := make([]*payment.Payment, 0, len(rows))
	for _, row := range rows {
		out = append(out, toPayment(row))
	}
	return out, nil
}

func (r *Payments) Save(ctx context.Context, p *payment.Payment) error {
	err := r.s.q(ctx).UpsertPayment(ctx, gen.UpsertPaymentParams{
		ID:           p.ID,
		OrderID:      p.OrderID,
		AmountSatang: int64(p.Amount),
		Status:       string(p.Status),
		Reference:    p.Reference,
		Reason:       p.Reason,
		CreatedAt:    ts(p.CreatedAt),
		SettledAt:    tsPtr(p.SettledAt),
	})
	if err != nil {
		return fmt.Errorf("pgstore: บันทึกการชำระเงิน %s: %w", p.ID, err)
	}
	return nil
}

func toPayment(row gen.Payment) *payment.Payment {
	p := &payment.Payment{
		ID:        row.ID,
		OrderID:   row.OrderID,
		Amount:    money.Satang(row.AmountSatang),
		Status:    payment.Status(row.Status),
		Reference: row.Reference,
		Reason:    row.Reason,
		CreatedAt: row.CreatedAt.Time,
	}
	if row.SettledAt.Valid {
		t := row.SettledAt.Time
		p.SettledAt = &t
	}
	return p
}
