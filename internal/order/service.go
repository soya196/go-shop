package order

import (
	"context"
	"errors"
	"fmt"
)

// Service คือชั้น use case ของ order
//
// dependency ทั้ง 6 ตัวเป็น interface ที่ package นี้ประกาศเอง — ไม่มี struct จากที่อื่นเลย
type Service struct {
	repo     Repository
	stock    Stock
	shoppers Shoppers
	wallet   Wallet
	ids      IDGenerator
	clock    Clock
}

func NewService(repo Repository, stock Stock, shoppers Shoppers, wallet Wallet, ids IDGenerator, clock Clock) *Service {
	return &Service{repo: repo, stock: stock, shoppers: shoppers, wallet: wallet, ids: ids, clock: clock}
}

// Place เปิดออเดอร์ใหม่
//
// ขั้นตอน: ตรวจสิทธิ์ลูกค้า → จองของทุกบรรทัด (ถ้าพลาดกลางทางต้องคืนของที่จองไปแล้ว) →
// สร้าง entity → บันทึก → นับออเดอร์ค้างให้ลูกค้า
func (s *Service) Place(ctx context.Context, customerID string, lines []Line) (*Order, error) {
	if err := s.shoppers.EnsureCanOrder(ctx, customerID); err != nil {
		return nil, err
	}

	reserved := make([]Line, 0, len(lines))
	for _, l := range lines {
		if err := s.stock.Reserve(ctx, l.ProductID, l.Qty); err != nil {
			s.releaseAll(ctx, reserved) // ⚠️ compensating action — กันของค้างจอง
			return nil, err
		}
		reserved = append(reserved, l)
	}

	o, err := New(s.ids.NewID(), customerID, lines, s.clock.Now())
	if err != nil {
		s.releaseAll(ctx, reserved)
		return nil, err
	}
	if err := s.repo.Save(ctx, o); err != nil {
		s.releaseAll(ctx, reserved)
		return nil, err
	}
	if err := s.shoppers.OrderOpened(ctx, customerID); err != nil {
		s.releaseAll(ctx, reserved)
		return nil, err
	}
	return o, nil
}

// Pay เก็บเงินแล้วเลื่อนสถานะเป็น PAID
func (s *Service) Pay(ctx context.Context, orderID string) (*Order, error) {
	o, err := s.repo.FindByID(ctx, orderID)
	if err != nil {
		return nil, err
	}
	if o.Status != Placed {
		return nil, fmt.Errorf("%w: %s → %s", ErrBadTransition, o.Status, Paid)
	}

	paymentID, err := s.wallet.Collect(ctx, o.ID, o.Total())
	if err != nil {
		return nil, err
	}
	if err := o.MarkPaid(paymentID, s.clock.Now()); err != nil {
		return nil, err
	}
	if err := s.repo.Save(ctx, o); err != nil {
		return nil, err
	}
	return o, nil
}

// StartPreparing เริ่มจัดของ
func (s *Service) StartPreparing(ctx context.Context, orderID string) (*Order, error) {
	return s.apply(ctx, orderID, func(o *Order) error { return o.StartPreparing(s.clock.Now()) })
}

// Ship ส่งของ — ตัดสต็อกจริงตรงนี้ (ของออกจากคลังแล้ว)
func (s *Service) Ship(ctx context.Context, orderID, tracking string) (*Order, error) {
	o, err := s.repo.FindByID(ctx, orderID)
	if err != nil {
		return nil, err
	}
	if err := o.Ship(tracking, s.clock.Now()); err != nil {
		return nil, err
	}
	for _, l := range o.Lines {
		if err := s.stock.Fulfil(ctx, l.ProductID, l.Qty); err != nil {
			return nil, err
		}
	}
	if err := s.repo.Save(ctx, o); err != nil {
		return nil, err
	}
	return o, nil
}

// Deliver ส่งถึงมือ — ออเดอร์จบ ปลดออเดอร์ค้างของลูกค้า
func (s *Service) Deliver(ctx context.Context, orderID string) (*Order, error) {
	o, err := s.repo.FindByID(ctx, orderID)
	if err != nil {
		return nil, err
	}
	if err := o.Deliver(s.clock.Now()); err != nil {
		return nil, err
	}
	if err := s.repo.Save(ctx, o); err != nil {
		return nil, err
	}
	if err := s.shoppers.OrderClosed(ctx, o.CustomerID); err != nil {
		return nil, err
	}
	return o, nil
}

// Cancel ยกเลิกออเดอร์ — คืนของที่จอง คืนเงินถ้าจ่ายแล้ว ปลดออเดอร์ค้าง
func (s *Service) Cancel(ctx context.Context, orderID string) (*Order, error) {
	o, err := s.repo.FindByID(ctx, orderID)
	if err != nil {
		return nil, err
	}
	wasPaid := o.WasPaid()

	if err := o.Cancel(s.clock.Now()); err != nil {
		return nil, err
	}
	s.releaseAll(ctx, o.Lines)

	if wasPaid {
		if err := s.wallet.RefundOrder(ctx, o.ID); err != nil {
			return nil, err
		}
	}
	if err := s.repo.Save(ctx, o); err != nil {
		return nil, err
	}
	if err := s.shoppers.OrderClosed(ctx, o.CustomerID); err != nil {
		return nil, err
	}
	return o, nil
}

// Get อ่านออเดอร์
func (s *Service) Get(ctx context.Context, orderID string) (*Order, error) {
	return s.repo.FindByID(ctx, orderID)
}

// ForCustomer คืนออเดอร์ทั้งหมดของลูกค้า
func (s *Service) ForCustomer(ctx context.Context, customerID string) ([]*Order, error) {
	return s.repo.FindByCustomer(ctx, customerID)
}

// List คืนออเดอร์ตามสถานะ (ส่ง "" = ทั้งหมด)
func (s *Service) List(ctx context.Context, status Status) ([]*Order, error) {
	return s.repo.List(ctx, status)
}

func (s *Service) apply(ctx context.Context, orderID string, fn func(*Order) error) (*Order, error) {
	o, err := s.repo.FindByID(ctx, orderID)
	if err != nil {
		return nil, err
	}
	if err := fn(o); err != nil {
		return nil, err
	}
	if err := s.repo.Save(ctx, o); err != nil {
		return nil, err
	}
	return o, nil
}

// releaseAll คืนของที่จองไว้ทั้งหมดแบบ best-effort
//
// ตั้งใจกลืน error: ถ้าคืนของไม่สำเร็จเราไม่อยากบัง error ต้นทางที่สำคัญกว่า
// ระบบจริงควรส่งเข้าคิว retry / แจ้งเตือน — ที่นี่จงใจให้เห็นว่าเป็นจุดที่ต้องคิด
func (s *Service) releaseAll(ctx context.Context, lines []Line) {
	for _, l := range lines {
		if err := s.stock.Release(ctx, l.ProductID, l.Qty); err != nil && !errors.Is(err, context.Canceled) {
			continue
		}
	}
}
