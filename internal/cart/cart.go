// Package cart เป็น domain ของ "ตะกร้าสินค้า"
//
// 🔑 จุดที่ควรดูใน package นี้: cart ต้องใช้ข้อมูลสินค้า แต่ **ไม่ import catalog**
// มันประกาศ port ชื่อ Catalog กับ type ProductInfo ของตัวเองขึ้นมา
// แล้วให้ adapter ฝั่งนอก (internal/bridge) เป็นคนต่อท่อไปหา catalog จริง
//
// นี่คือสิ่งที่คลาสสอนไว้ว่า "มันคุยผ่าน interface — มันไม่รู้จัก book ตัวจริงด้วยซ้ำ"
// ผลลัพธ์: cart กับ catalog แยกกันคนละทีมได้ · เทส cart ได้โดยไม่ต้องมี catalog
package cart

import (
	"context"
	"errors"
	"fmt"

	"github.com/soya196/go-shop/internal/money"
)

var (
	ErrNotFound     = errors.New("cart: not found")
	ErrEmpty        = errors.New("cart: cart is empty")
	ErrInvalidQty   = errors.New("cart: quantity must be positive")
	ErrLineNotFound = errors.New("cart: product not in cart")
	ErrNotSellable  = errors.New("cart: product is not available for sale")
	ErrTooManyLines = errors.New("cart: too many distinct products in cart")
)

// MaxLines คือกฎธุรกิจ: ตะกร้าใบหนึ่งมีสินค้าต่างชนิดได้ไม่เกินนี้
const MaxLines = 50

// ProductInfo คือ "มุมมองของ cart" ต่อสินค้า — ไม่ใช่ catalog.Product
//
// cart สนใจแค่ 4 อย่างนี้ ไม่สนใจ stock/reserved/sku ที่ catalog เก็บ
type ProductInfo struct {
	ID       string
	Name     string
	Price    money.Satang
	Sellable bool
}

// Catalog คือ port ที่ cart ประกาศเอง (consumer-declared interface)
type Catalog interface {
	Lookup(ctx context.Context, productID string) (ProductInfo, error)
}

// Line คือสินค้าหนึ่งบรรทัดในตะกร้า
//
// เก็บ UnitPrice ไว้ในบรรทัด ไม่ไปดึงจาก catalog ตอนคิดเงิน
// เพราะราคาต้องนิ่ง ณ เวลาที่ลูกค้าหยิบใส่ตะกร้า
type Line struct {
	ProductID string
	Name      string
	UnitPrice money.Satang
	Qty       int
}

// Total ของบรรทัดนี้
func (l Line) Total() money.Satang { return l.UnitPrice.Mul(l.Qty) }

// Cart คือตะกร้าของลูกค้าหนึ่งใบ
type Cart struct {
	ID         string
	CustomerID string
	Lines      []Line
}

// New สร้างตะกร้าเปล่า
func New(id, customerID string) *Cart {
	return &Cart{ID: id, CustomerID: customerID, Lines: []Line{}}
}

// Add ใส่สินค้าลงตะกร้า — ถ้ามีอยู่แล้วให้บวกจำนวน
func (c *Cart) Add(p ProductInfo, qty int) error {
	if qty <= 0 {
		return ErrInvalidQty
	}
	if !p.Sellable {
		return fmt.Errorf("%w: %s", ErrNotSellable, p.Name)
	}
	if i := c.indexOf(p.ID); i >= 0 {
		c.Lines[i].Qty += qty
		c.Lines[i].UnitPrice = p.Price // ยึดราคาล่าสุดตอนหยิบเพิ่ม
		c.Lines[i].Name = p.Name
		return nil
	}
	if len(c.Lines) >= MaxLines {
		return fmt.Errorf("%w: สูงสุด %d รายการ", ErrTooManyLines, MaxLines)
	}
	c.Lines = append(c.Lines, Line{ProductID: p.ID, Name: p.Name, UnitPrice: p.Price, Qty: qty})
	return nil
}

// SetQty กำหนดจำนวนตรงๆ — ใส่ 0 = เอาออก
func (c *Cart) SetQty(productID string, qty int) error {
	if qty < 0 {
		return ErrInvalidQty
	}
	i := c.indexOf(productID)
	if i < 0 {
		return fmt.Errorf("%w: %s", ErrLineNotFound, productID)
	}
	if qty == 0 {
		return c.Remove(productID)
	}
	c.Lines[i].Qty = qty
	return nil
}

// Remove เอาสินค้าออกจากตะกร้า
func (c *Cart) Remove(productID string) error {
	i := c.indexOf(productID)
	if i < 0 {
		return fmt.Errorf("%w: %s", ErrLineNotFound, productID)
	}
	c.Lines = append(c.Lines[:i], c.Lines[i+1:]...)
	return nil
}

// Clear ล้างตะกร้า (เรียกหลัง checkout สำเร็จ)
func (c *Cart) Clear() { c.Lines = []Line{} }

// Total คือยอดรวมทั้งตะกร้า
func (c *Cart) Total() money.Satang {
	var sum money.Satang
	for _, l := range c.Lines {
		sum = sum.Add(l.Total())
	}
	return sum
}

// ItemCount คือจำนวนชิ้นรวม (ไม่ใช่จำนวนบรรทัด)
func (c *Cart) ItemCount() int {
	n := 0
	for _, l := range c.Lines {
		n += l.Qty
	}
	return n
}

// IsEmpty บอกว่าตะกร้าว่างไหม
func (c *Cart) IsEmpty() bool { return len(c.Lines) == 0 }

func (c *Cart) indexOf(productID string) int {
	for i, l := range c.Lines {
		if l.ProductID == productID {
			return i
		}
	}
	return -1
}

// Repository เป็น port ฝั่ง driven
type Repository interface {
	FindByID(ctx context.Context, id string) (*Cart, error)
	FindByCustomer(ctx context.Context, customerID string) (*Cart, error)
	Save(ctx context.Context, c *Cart) error
	Delete(ctx context.Context, id string) error
}
