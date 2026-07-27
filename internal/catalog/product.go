// Package catalog เป็น domain ของ "สินค้าที่ร้านขาย"
//
// กฎของ package นี้ (ตามที่คลาส Part 4 สอน):
//   - entity ถือกฎธุรกิจของตัวเอง — Reserve/Release ตรวจ stock เอง ไม่ใช่ให้ service ตรวจ
//   - Repository interface ประกาศ "ที่นี่" เพราะ catalog คือผู้ใช้ (caller) ของมัน
//   - package นี้ต้องไม่รู้จัก HTTP / ฐานข้อมูล / domain อื่น เลยแม้แต่นิดเดียว
package catalog

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/soya196/go-shop/internal/money"
)

// Domain errors — ผู้เรียกใช้ errors.Is() ตรวจได้
var (
	ErrNotFound       = errors.New("catalog: product not found")
	ErrInvalidName    = errors.New("catalog: name must not be empty")
	ErrInvalidPrice   = errors.New("catalog: price must be positive")
	ErrInvalidQty     = errors.New("catalog: quantity must be positive")
	ErrOutOfStock     = errors.New("catalog: not enough stock")
	ErrInactive       = errors.New("catalog: product is not active")
	ErrDuplicateSKU   = errors.New("catalog: sku already exists")
	ErrReleaseTooMuch = errors.New("catalog: cannot release more than reserved")
)

// Product คือสินค้าหนึ่งรายการในแคตตาล็อก
//
// Stock = จำนวนที่มีอยู่จริงบนชั้น · Reserved = จำนวนที่ถูกจองไว้ในออเดอร์ที่ยังไม่จบ
// จำนวนที่ "ขายได้" = Stock - Reserved
type Product struct {
	ID       string
	SKU      string
	Name     string
	Price    money.Satang
	Stock    int
	Reserved int
	Active   bool
}

// New สร้างสินค้าใหม่พร้อม validate
//
// ตั้งชื่อ New เฉยๆ ไม่ใช่ NewProduct — เพราะผู้เรียกเห็นเป็น catalog.New()
// การเขียน catalog.NewProduct() คือ stutter ที่ Effective Go บอกให้เลี่ยง
func New(id, sku, name string, price money.Satang, stock int) (*Product, error) {
	if strings.TrimSpace(name) == "" {
		return nil, ErrInvalidName
	}
	if price <= 0 {
		return nil, ErrInvalidPrice
	}
	if stock < 0 {
		return nil, fmt.Errorf("%w: stock", ErrInvalidQty)
	}
	return &Product{
		ID:     id,
		SKU:    strings.TrimSpace(sku),
		Name:   strings.TrimSpace(name),
		Price:  price,
		Stock:  stock,
		Active: true,
	}, nil
}

// Available คือจำนวนที่ยังขายได้จริง
func (p *Product) Available() int { return p.Stock - p.Reserved }

// CanSell บอกว่าสินค้านี้ขายได้ไหม ณ ตอนนี้
func (p *Product) CanSell(qty int) bool {
	return p.Active && qty > 0 && p.Available() >= qty
}

// Reserve จองสินค้าไว้ให้ออเดอร์ — กฎธุรกิจอยู่ตรงนี้ ไม่ใช่ที่ service
func (p *Product) Reserve(qty int) error {
	if qty <= 0 {
		return ErrInvalidQty
	}
	if !p.Active {
		return fmt.Errorf("%w: %s", ErrInactive, p.Name)
	}
	if p.Available() < qty {
		return fmt.Errorf("%w: %s (ต้องการ %d มี %d)", ErrOutOfStock, p.Name, qty, p.Available())
	}
	p.Reserved += qty
	return nil
}

// Release คืนของที่จองไว้ (ออเดอร์ถูกยกเลิก)
func (p *Product) Release(qty int) error {
	if qty <= 0 {
		return ErrInvalidQty
	}
	if qty > p.Reserved {
		return fmt.Errorf("%w: reserved=%d release=%d", ErrReleaseTooMuch, p.Reserved, qty)
	}
	p.Reserved -= qty
	return nil
}

// Fulfil ตัดสต็อกจริงเมื่อของถูกส่งออกไปแล้ว
func (p *Product) Fulfil(qty int) error {
	if qty <= 0 {
		return ErrInvalidQty
	}
	if qty > p.Reserved {
		return fmt.Errorf("%w: reserved=%d fulfil=%d", ErrReleaseTooMuch, p.Reserved, qty)
	}
	p.Reserved -= qty
	p.Stock -= qty
	return nil
}

// Restock เติมของเข้าชั้น
func (p *Product) Restock(qty int) error {
	if qty <= 0 {
		return ErrInvalidQty
	}
	p.Stock += qty
	return nil
}

// Reprice เปลี่ยนราคา
func (p *Product) Reprice(price money.Satang) error {
	if price <= 0 {
		return ErrInvalidPrice
	}
	p.Price = price
	return nil
}

func (p *Product) Deactivate() { p.Active = false }
func (p *Product) Activate()   { p.Active = true }

// Repository คือ port ฝั่ง driven
//
// ประกาศไว้ที่นี่เพราะ catalog เป็น "ผู้ใช้" ของมัน (Go idiom: interface อยู่ที่ caller)
// adapter อย่าง memory/ หรือ jsonstore/ implement โดยไม่ต้อง import อะไรจากที่นี่
type Repository interface {
	FindByID(ctx context.Context, id string) (*Product, error)
	FindBySKU(ctx context.Context, sku string) (*Product, error)
	List(ctx context.Context) ([]*Product, error)
	Save(ctx context.Context, p *Product) error
}
