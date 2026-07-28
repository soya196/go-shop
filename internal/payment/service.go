package payment

import (
	"context"
	"fmt"

	"github.com/soya196/go-shop/internal/money"
)

// IDGenerator เป็น port เล็กๆ ที่ service ต้องใช้
type IDGenerator interface {
	NewID() string
}

// Service คือชั้น use case ของ payment
type Service struct {
	repo  Repository
	gw    Gateway
	ids   IDGenerator
	clock Clock
}

func NewService(repo Repository, gw Gateway, ids IDGenerator, clock Clock) *Service {
	return &Service{repo: repo, gw: gw, ids: ids, clock: clock}
}

// Collect เก็บเงินสำหรับออเดอร์หนึ่งใบ
//
// ลำดับสำคัญ: บันทึก PENDING ก่อนยิง gateway เสมอ
// ถ้าเครื่องดับหลังยิงแต่ก่อนบันทึกผล เราจะยังมีร่องรอยว่าเคยพยายามเก็บเงิน
func (s *Service) Collect(ctx context.Context, orderID string, amount money.Satang) (*Payment, error) {
	p, err := New(s.ids.NewID(), orderID, amount, s.clock.Now())
	if err != nil {
		return nil, err
	}
	if err := s.repo.Save(ctx, p); err != nil {
		return nil, err
	}

	charge, err := s.gw.Charge(ctx, p.ID, amount)
	if err != nil {
		if failErr := p.Fail(err.Error(), s.clock.Now()); failErr != nil {
			return nil, failErr
		}
		if saveErr := s.repo.Save(ctx, p); saveErr != nil {
			return nil, saveErr
		}
		return p, fmt.Errorf("%w: %w", ErrDeclined, err)
	}

	if !charge.Approved {
		if failErr := p.Fail(charge.Reason, s.clock.Now()); failErr != nil {
			return nil, failErr
		}
		if saveErr := s.repo.Save(ctx, p); saveErr != nil {
			return nil, saveErr
		}
		return p, fmt.Errorf("%w: %s", ErrDeclined, charge.Reason)
	}

	if err := p.Succeed(charge.Reference, s.clock.Now()); err != nil {
		return nil, err
	}
	if err := s.repo.Save(ctx, p); err != nil {
		return nil, err
	}
	return p, nil
}

// Refund คืนเงิน
func (s *Service) Refund(ctx context.Context, paymentID string) (*Payment, error) {
	p, err := s.repo.FindByID(ctx, paymentID)
	if err != nil {
		return nil, err
	}
	if err := p.Refund(s.clock.Now()); err != nil {
		return nil, err
	}
	if err := s.repo.Save(ctx, p); err != nil {
		return nil, err
	}
	return p, nil
}

// Get อ่านรายการชำระเงิน
func (s *Service) Get(ctx context.Context, paymentID string) (*Payment, error) {
	return s.repo.FindByID(ctx, paymentID)
}

// ForOrder คืนรายการชำระเงินทั้งหมดของออเดอร์
func (s *Service) ForOrder(ctx context.Context, orderID string) ([]*Payment, error) {
	return s.repo.FindByOrder(ctx, orderID)
}

// RefundOrder คืนเงินทุกรายการที่สำเร็จของออเดอร์ (ใช้ตอนยกเลิกออเดอร์ที่จ่ายแล้ว)
func (s *Service) RefundOrder(ctx context.Context, orderID string) error {
	list, err := s.repo.FindByOrder(ctx, orderID)
	if err != nil {
		return err
	}
	for _, p := range list {
		if p.Status != Succeeded {
			continue
		}
		if err := p.Refund(s.clock.Now()); err != nil {
			return err
		}
		if err := s.repo.Save(ctx, p); err != nil {
			return err
		}
	}
	return nil
}
