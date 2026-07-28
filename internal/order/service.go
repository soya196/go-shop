package order

import (
	"context"
	"errors"
	"fmt"
)

// Service คือชั้น use case ของ order
//
// dependency ทั้ง 7 ตัวเป็น interface ที่ package นี้ประกาศเอง — ไม่มี struct จากที่อื่นเลย
type Service struct {
	repo     Repository
	stock    Stock
	shoppers Shoppers
	wallet   Wallet
	ids      IDGenerator
	clock    Clock
	tx       TxManager
}

func NewService(repo Repository, stock Stock, shoppers Shoppers, wallet Wallet, ids IDGenerator, clock Clock, tx TxManager) *Service {
	return &Service{repo: repo, stock: stock, shoppers: shoppers, wallet: wallet, ids: ids, clock: clock, tx: tx}
}

// Place เปิดออเดอร์ใหม่
//
// ขั้นตอน: ตรวจสิทธิ์ลูกค้า → จองของทุกบรรทัด → สร้าง entity → บันทึก → นับออเดอร์ค้าง
// ทั้งหมดนี้ต้องสำเร็จหรือล้ม "พร้อมกัน" — ไม่งั้นจะเหลือของค้างจองที่ไม่มีออเดอร์เป็นเจ้าของ
//
// 🔑 ป้องกัน 2 ชั้น:
//  1. s.tx.Do — ถ้า adapter ทำ transaction ได้จริง (postgres) ทุกอย่างถูก rollback ให้
//  2. releaseAll ใน defer — ตาข่ายสำหรับ adapter ที่ไม่มี transaction (memory/json)
//
// ชั้นที่ 2 ไม่ได้ซ้ำซ้อนเปล่าๆ: บน postgres มันจะถูก rollback ไปด้วย จึงไม่มีผลข้างเคียง
// แต่บน memory มันคือสิ่งเดียวที่กันของค้าง
func (s *Service) Place(ctx context.Context, customerID string, lines []Line) (*Order, error) {
	var placed *Order

	err := s.tx.Do(ctx, func(ctx context.Context) error {
		reserved := make([]Line, 0, len(lines))
		committed := false
		defer func() {
			if !committed {
				s.releaseAll(ctx, reserved)
			}
		}()

		if err := s.shoppers.EnsureCanOrder(ctx, customerID); err != nil {
			return err
		}
		for _, l := range lines {
			if err := s.stock.Reserve(ctx, l.ProductID, l.Qty); err != nil {
				return err
			}
			reserved = append(reserved, l)
		}

		o, err := New(s.ids.NewID(), customerID, lines, s.clock.Now())
		if err != nil {
			return err
		}
		if err := s.repo.Save(ctx, o); err != nil {
			return err
		}
		if err := s.shoppers.OrderOpened(ctx, customerID); err != nil {
			return err
		}

		committed = true
		placed = o
		return nil
	})
	if err != nil {
		return nil, err
	}
	return placed, nil
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
//
// ตัดสต็อกหลายบรรทัด + บันทึกสถานะ ต้องอยู่ด้วยกัน
// ไม่งั้นอาจตัดสต็อกไปแล้วครึ่งหนึ่ง แต่ออเดอร์ยังเป็น PREPARING → กด ship ซ้ำได้ ตัดซ้ำ
func (s *Service) Ship(ctx context.Context, orderID, tracking string) (*Order, error) {
	var shipped *Order

	err := s.tx.Do(ctx, func(ctx context.Context) error {
		o, err := s.repo.FindByID(ctx, orderID)
		if err != nil {
			return err
		}
		if err := o.Ship(tracking, s.clock.Now()); err != nil {
			return err
		}
		for _, l := range o.Lines {
			if err := s.stock.Fulfil(ctx, l.ProductID, l.Qty); err != nil {
				return err
			}
		}
		if err := s.repo.Save(ctx, o); err != nil {
			return err
		}
		shipped = o
		return nil
	})
	if err != nil {
		return nil, err
	}
	return shipped, nil
}

// Deliver ส่งถึงมือ — ออเดอร์จบ ปลดออเดอร์ค้างของลูกค้า
func (s *Service) Deliver(ctx context.Context, orderID string) (*Order, error) {
	var delivered *Order

	// บันทึกสถานะ + ลดตัวนับออเดอร์ค้างของลูกค้า ต้องไปด้วยกัน
	// ไม่งั้นลูกค้าจะติดโควตา MaxOpenOrders ทั้งที่ของส่งถึงมือแล้ว
	err := s.tx.Do(ctx, func(ctx context.Context) error {
		o, err := s.repo.FindByID(ctx, orderID)
		if err != nil {
			return err
		}
		if err := o.Deliver(s.clock.Now()); err != nil {
			return err
		}
		if err := s.repo.Save(ctx, o); err != nil {
			return err
		}
		if err := s.shoppers.OrderClosed(ctx, o.CustomerID); err != nil {
			return err
		}
		delivered = o
		return nil
	})
	if err != nil {
		return nil, err
	}
	return delivered, nil
}

// Cancel ยกเลิกออเดอร์ — คืนของที่จอง คืนเงินถ้าจ่ายแล้ว ปลดออเดอร์ค้าง
//
// 4 อย่างนี้ต้องไปด้วยกัน ถ้าครึ่งทางแล้วพัง จะได้ออเดอร์ที่ "ยกเลิกแล้วแต่ของยังค้างจอง"
// หรือแย่กว่านั้นคือ "คืนเงินแล้วแต่สถานะยังไม่ CANCELLED" → ลูกค้ายกเลิกซ้ำได้เงินอีกรอบ
func (s *Service) Cancel(ctx context.Context, orderID string) (*Order, error) {
	var cancelled *Order

	err := s.tx.Do(ctx, func(ctx context.Context) error {
		o, err := s.repo.FindByID(ctx, orderID)
		if err != nil {
			return err
		}
		wasPaid := o.WasPaid()

		if err := o.Cancel(s.clock.Now()); err != nil {
			return err
		}
		s.releaseAll(ctx, o.Lines)

		if wasPaid {
			if err := s.wallet.RefundOrder(ctx, o.ID); err != nil {
				return err
			}
		}
		if err := s.repo.Save(ctx, o); err != nil {
			return err
		}
		if err := s.shoppers.OrderClosed(ctx, o.CustomerID); err != nil {
			return err
		}
		cancelled = o
		return nil
	})
	if err != nil {
		return nil, err
	}
	return cancelled, nil
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
