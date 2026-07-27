package cart

import (
	"context"
	"errors"
)

// IDGenerator เป็น port เล็กๆ ที่ service ต้องใช้
type IDGenerator interface {
	NewID() string
}

// Service คือชั้น use case ของ cart
//
// dependency ทั้งหมดเป็น interface ที่ package นี้ประกาศเอง
type Service struct {
	repo    Repository
	catalog Catalog
	ids     IDGenerator
}

func NewService(repo Repository, cat Catalog, ids IDGenerator) *Service {
	return &Service{repo: repo, catalog: cat, ids: ids}
}

// OpenFor คืนตะกร้าที่ใช้งานอยู่ของลูกค้า ถ้ายังไม่มีก็สร้างให้
func (s *Service) OpenFor(ctx context.Context, customerID string) (*Cart, error) {
	c, err := s.repo.FindByCustomer(ctx, customerID)
	if err == nil {
		return c, nil
	}
	if !errors.Is(err, ErrNotFound) {
		return nil, err
	}
	c = New(s.ids.NewID(), customerID)
	if err := s.repo.Save(ctx, c); err != nil {
		return nil, err
	}
	return c, nil
}

// Get อ่านตะกร้าตาม id
func (s *Service) Get(ctx context.Context, cartID string) (*Cart, error) {
	return s.repo.FindByID(ctx, cartID)
}

// AddItem หยิบสินค้าใส่ตะกร้า
//
// สังเกตลำดับ: ถามราคาจาก port → ให้ entity ตัดสินใจ → บันทึก
// service ไม่ได้ตัดสินกฎเอง มันแค่จัดลำดับงาน
func (s *Service) AddItem(ctx context.Context, cartID, productID string, qty int) (*Cart, error) {
	c, err := s.repo.FindByID(ctx, cartID)
	if err != nil {
		return nil, err
	}
	info, err := s.catalog.Lookup(ctx, productID)
	if err != nil {
		return nil, err
	}
	if err := c.Add(info, qty); err != nil {
		return nil, err
	}
	if err := s.repo.Save(ctx, c); err != nil {
		return nil, err
	}
	return c, nil
}

// SetQty ปรับจำนวน (0 = เอาออก)
func (s *Service) SetQty(ctx context.Context, cartID, productID string, qty int) (*Cart, error) {
	return s.apply(ctx, cartID, func(c *Cart) error { return c.SetQty(productID, qty) })
}

// RemoveItem เอาสินค้าออก
func (s *Service) RemoveItem(ctx context.Context, cartID, productID string) (*Cart, error) {
	return s.apply(ctx, cartID, func(c *Cart) error { return c.Remove(productID) })
}

// Clear ล้างตะกร้า
func (s *Service) Clear(ctx context.Context, cartID string) error {
	_, err := s.apply(ctx, cartID, func(c *Cart) error { c.Clear(); return nil })
	return err
}

func (s *Service) apply(ctx context.Context, cartID string, fn func(*Cart) error) (*Cart, error) {
	c, err := s.repo.FindByID(ctx, cartID)
	if err != nil {
		return nil, err
	}
	if err := fn(c); err != nil {
		return nil, err
	}
	if err := s.repo.Save(ctx, c); err != nil {
		return nil, err
	}
	return c, nil
}
