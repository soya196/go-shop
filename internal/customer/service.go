package customer

import (
	"context"
	"errors"
	"fmt"
	"strings"
)

// IDGenerator เป็น port เล็กๆ ที่ service ต้องใช้
type IDGenerator interface {
	NewID() string
}

// Service คือชั้น use case ของ customer
type Service struct {
	repo Repository
	ids  IDGenerator
}

func NewService(repo Repository, ids IDGenerator) *Service {
	return &Service{repo: repo, ids: ids}
}

// Register สมัครลูกค้าใหม่
func (s *Service) Register(ctx context.Context, name, email string) (*Customer, error) {
	norm := strings.ToLower(strings.TrimSpace(email))
	if existing, err := s.repo.FindByEmail(ctx, norm); err == nil && existing != nil {
		return nil, fmt.Errorf("%w: %s", ErrDuplicate, norm)
	} else if err != nil && !errors.Is(err, ErrNotFound) {
		return nil, err
	}

	c, err := New(s.ids.NewID(), name, email)
	if err != nil {
		return nil, err
	}
	if err := s.repo.Save(ctx, c); err != nil {
		return nil, err
	}
	return c, nil
}

func (s *Service) Get(ctx context.Context, id string) (*Customer, error) {
	return s.repo.FindByID(ctx, id)
}

func (s *Service) List(ctx context.Context) ([]*Customer, error) {
	return s.repo.List(ctx)
}

// EnsureCanOrder ให้ domain อื่นถามผ่าน bridge ว่า "ลูกค้าคนนี้สั่งของได้ไหม"
func (s *Service) EnsureCanOrder(ctx context.Context, id string) error {
	c, err := s.repo.FindByID(ctx, id)
	if err != nil {
		return err
	}
	return c.CanOrder()
}

// OrderOpened / OrderClosed ให้ order domain แจ้งกลับผ่าน bridge
func (s *Service) OrderOpened(ctx context.Context, id string) error {
	return s.mutate(ctx, id, (*Customer).OrderOpened)
}

func (s *Service) OrderClosed(ctx context.Context, id string) error {
	return s.mutate(ctx, id, func(c *Customer) error { c.OrderClosed(); return nil })
}

func (s *Service) Suspend(ctx context.Context, id string) error {
	return s.mutate(ctx, id, func(c *Customer) error { c.Suspend(); return nil })
}

func (s *Service) Restore(ctx context.Context, id string) error {
	return s.mutate(ctx, id, func(c *Customer) error { c.Restore(); return nil })
}

func (s *Service) mutate(ctx context.Context, id string, apply func(*Customer) error) error {
	c, err := s.repo.FindByID(ctx, id)
	if err != nil {
		return err
	}
	if err := apply(c); err != nil {
		return err
	}
	return s.repo.Save(ctx, c)
}
