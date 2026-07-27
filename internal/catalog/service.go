package catalog

import (
	"context"
	"errors"
	"fmt"

	"github.com/soya196/go-shop/internal/money"
)

// IDGenerator เป็น port เล็กๆ ที่ service ต้องใช้
//
// แยกออกมาเป็น interface เพราะ "สุ่ม id" คือ side effect — ทำให้ test ไม่ deterministic
// ผู้ใช้ (service) เป็นคนบอกว่าต้องการอะไร adapter เป็นคนหาให้
type IDGenerator interface {
	NewID() string
}

// Service คือชั้น use case ของ catalog
//
// สังเกตว่ามันไม่มี http.Request, ไม่มี sql.DB, ไม่มี logger ของ framework ใดๆ
type Service struct {
	repo Repository
	ids  IDGenerator
}

func NewService(repo Repository, ids IDGenerator) *Service {
	return &Service{repo: repo, ids: ids}
}

// AddProduct เพิ่มสินค้าใหม่เข้าแคตตาล็อก
func (s *Service) AddProduct(ctx context.Context, sku, name string, price money.Satang, stock int) (*Product, error) {
	if existing, err := s.repo.FindBySKU(ctx, sku); err == nil && existing != nil {
		return nil, fmt.Errorf("%w: %s", ErrDuplicateSKU, sku)
	} else if err != nil && !errors.Is(err, ErrNotFound) {
		return nil, err
	}

	p, err := New(s.ids.NewID(), sku, name, price, stock)
	if err != nil {
		return nil, err
	}
	if err := s.repo.Save(ctx, p); err != nil {
		return nil, err
	}
	return p, nil
}

// Get อ่านสินค้าตาม id
func (s *Service) Get(ctx context.Context, id string) (*Product, error) {
	return s.repo.FindByID(ctx, id)
}

// Browse คืนสินค้าที่ยังขายอยู่ (ลูกค้าเห็นเฉพาะที่ Active)
func (s *Service) Browse(ctx context.Context) ([]*Product, error) {
	all, err := s.repo.List(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]*Product, 0, len(all))
	for _, p := range all {
		if p.Active {
			out = append(out, p)
		}
	}
	return out, nil
}

// ListAll คืนทุกอย่างรวมของที่ปิดขาย (สำหรับหลังร้าน)
func (s *Service) ListAll(ctx context.Context) ([]*Product, error) {
	return s.repo.List(ctx)
}

// Reserve จองของให้ออเดอร์ — use case ที่ domain อื่นเรียกผ่าน bridge
func (s *Service) Reserve(ctx context.Context, productID string, qty int) error {
	return s.mutate(ctx, productID, func(p *Product) error { return p.Reserve(qty) })
}

// Release คืนของที่จองไว้
func (s *Service) Release(ctx context.Context, productID string, qty int) error {
	return s.mutate(ctx, productID, func(p *Product) error { return p.Release(qty) })
}

// Fulfil ตัดสต็อกจริงตอนส่งของ
func (s *Service) Fulfil(ctx context.Context, productID string, qty int) error {
	return s.mutate(ctx, productID, func(p *Product) error { return p.Fulfil(qty) })
}

// Restock เติมของ
func (s *Service) Restock(ctx context.Context, productID string, qty int) error {
	return s.mutate(ctx, productID, func(p *Product) error { return p.Restock(qty) })
}

// Reprice เปลี่ยนราคา
func (s *Service) Reprice(ctx context.Context, productID string, price money.Satang) error {
	return s.mutate(ctx, productID, func(p *Product) error { return p.Reprice(price) })
}

// Deactivate ปิดการขายสินค้า
func (s *Service) Deactivate(ctx context.Context, productID string) error {
	return s.mutate(ctx, productID, func(p *Product) error { p.Deactivate(); return nil })
}

// mutate = read-modify-write ที่ใช้ซ้ำทุก use case
//
// กฎธุรกิจอยู่ใน closure ที่เรียก method ของ entity — service แค่จัดลำดับ ไม่ตัดสินใจเอง
func (s *Service) mutate(ctx context.Context, productID string, apply func(*Product) error) error {
	p, err := s.repo.FindByID(ctx, productID)
	if err != nil {
		return err
	}
	if err := apply(p); err != nil {
		return err
	}
	return s.repo.Save(ctx, p)
}
