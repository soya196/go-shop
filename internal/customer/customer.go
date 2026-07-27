// Package customer เป็น domain ของ "คนที่ซื้อของกับเรา"
package customer

import (
	"context"
	"errors"
	"fmt"
	"strings"
)

var (
	ErrNotFound      = errors.New("customer: not found")
	ErrInvalidName   = errors.New("customer: name must not be empty")
	ErrInvalidEmail  = errors.New("customer: email is not valid")
	ErrDuplicate     = errors.New("customer: email already registered")
	ErrSuspended     = errors.New("customer: account is suspended")
	ErrCreditTooLow  = errors.New("customer: credit limit exceeded")
	ErrInvalidAmount = errors.New("customer: amount must be positive")
)

// Customer คือลูกค้าหนึ่งราย
type Customer struct {
	ID        string
	Name      string
	Email     string
	Suspended bool
	// OpenOrders นับออเดอร์ที่ยังไม่จบ — ใช้จำกัดจำนวนออเดอร์ค้างต่อคน
	OpenOrders int
}

// MaxOpenOrders คือกฎธุรกิจ: ลูกค้าหนึ่งคนมีออเดอร์ค้างพร้อมกันได้ไม่เกินนี้
const MaxOpenOrders = 5

// New สร้างลูกค้าใหม่พร้อม validate
func New(id, name, email string) (*Customer, error) {
	name = strings.TrimSpace(name)
	email = strings.ToLower(strings.TrimSpace(email))
	if name == "" {
		return nil, ErrInvalidName
	}
	if !validEmail(email) {
		return nil, fmt.Errorf("%w: %q", ErrInvalidEmail, email)
	}
	return &Customer{ID: id, Name: name, Email: email}, nil
}

// validEmail ตรวจแบบพอดีๆ — ไม่ต้อง regex ยาวเป็นวา
func validEmail(s string) bool {
	at := strings.IndexByte(s, '@')
	if at <= 0 || at == len(s)-1 || strings.Count(s, "@") != 1 {
		return false
	}
	domain := s[at+1:]
	dot := strings.LastIndexByte(domain, '.')
	return dot > 0 && dot < len(domain)-1 && !strings.ContainsAny(s, " \t")
}

// CanOrder บอกว่าลูกค้ารายนี้เปิดออเดอร์ใหม่ได้ไหม — กฎอยู่ที่ entity
func (c *Customer) CanOrder() error {
	if c.Suspended {
		return fmt.Errorf("%w: %s", ErrSuspended, c.Email)
	}
	if c.OpenOrders >= MaxOpenOrders {
		return fmt.Errorf("%w: มีออเดอร์ค้าง %d รายการ (สูงสุด %d)", ErrCreditTooLow, c.OpenOrders, MaxOpenOrders)
	}
	return nil
}

// OrderOpened เรียกเมื่อลูกค้าเปิดออเดอร์ใหม่สำเร็จ
func (c *Customer) OrderOpened() error {
	if err := c.CanOrder(); err != nil {
		return err
	}
	c.OpenOrders++
	return nil
}

// OrderClosed เรียกเมื่อออเดอร์จบ (ส่งถึงมือ หรือถูกยกเลิก)
func (c *Customer) OrderClosed() {
	if c.OpenOrders > 0 {
		c.OpenOrders--
	}
}

// Rename เปลี่ยนชื่อ
func (c *Customer) Rename(name string) error {
	name = strings.TrimSpace(name)
	if name == "" {
		return ErrInvalidName
	}
	c.Name = name
	return nil
}

func (c *Customer) Suspend() { c.Suspended = true }
func (c *Customer) Restore() { c.Suspended = false }

// Repository เป็น port — ประกาศที่นี่เพราะ customer เป็นผู้ใช้
type Repository interface {
	FindByID(ctx context.Context, id string) (*Customer, error)
	FindByEmail(ctx context.Context, email string) (*Customer, error)
	List(ctx context.Context) ([]*Customer, error)
	Save(ctx context.Context, c *Customer) error
}
