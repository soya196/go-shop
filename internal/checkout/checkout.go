// Package checkout เป็น domain ของ "ขั้นตอนจ่ายตังค์" — process ที่พาดผ่านตะกร้ากับออเดอร์
//
// 🔑 ทำไมต้องมี package นี้ แทนที่จะให้ cart import order ตรงๆ:
//
// "checkout" เป็นคำที่ลูกค้าพูดจริง มันคือ business process ไม่ใช่โฟลเดอร์เทคนิค
// การมี package นี้ทำให้ cart กับ order **ไม่ต้องรู้จักกันเลย** — ทั้งคู่ยังแยกทีมได้
// ถ้าปล่อยให้ cart import order เมื่อไหร่ ทั้งสองก้อนจะติดกันถาวร
//
// นี่คือคำตอบของคำถาม "แล้ว domain ที่ต้องคุยกันจริงๆ ทำยังไง" ที่คลาสไม่ได้ลงรายละเอียด
package checkout

import (
	"context"
	"errors"
	"fmt"

	"github.com/soya196/go-shop/internal/money"
)

var (
	ErrEmptyCart = errors.New("checkout: cart is empty")
	ErrMismatch  = errors.New("checkout: cart total changed, please review")
)

// Line คือมุมมองของ checkout ต่อสินค้าหนึ่งบรรทัด
type Line struct {
	ProductID string
	Name      string
	UnitPrice money.Satang
	Qty       int
}

// Basket คือสิ่งที่ checkout ต้องรู้เกี่ยวกับตะกร้าใบหนึ่ง
type Basket struct {
	CartID     string
	CustomerID string
	Lines      []Line
}

func (b Basket) Total() money.Satang {
	var sum money.Satang
	for _, l := range b.Lines {
		sum = sum.Add(l.UnitPrice.Mul(l.Qty))
	}
	return sum
}

// Receipt คือผลลัพธ์ของการ checkout
type Receipt struct {
	OrderID string
	Total   money.Satang
	Paid    bool
}

// ── ports ที่ checkout ประกาศเอง ─────────────────────────────────────

// Baskets คือความสามารถ "อ่านตะกร้า / ล้างตะกร้า"
type Baskets interface {
	Read(ctx context.Context, cartID string) (Basket, error)
	Empty(ctx context.Context, cartID string) error
}

// Orders คือความสามารถ "เปิดออเดอร์ / จ่ายเงิน"
type Orders interface {
	Place(ctx context.Context, customerID string, lines []Line) (orderID string, total money.Satang, err error)
	Pay(ctx context.Context, orderID string) error
}

// Service คือ use case ของ checkout
type Service struct {
	baskets Baskets
	orders  Orders
}

func NewService(baskets Baskets, orders Orders) *Service {
	return &Service{baskets: baskets, orders: orders}
}

// Submit เปลี่ยนตะกร้าเป็นออเดอร์
//
// expected ใช้กันเคส "ราคาขยับระหว่างลูกค้ากดจ่าย" — ส่ง 0 ถ้าไม่อยากตรวจ
// payNow = true คือจ่ายทันที (one-click) · false คือเปิดออเดอร์ไว้จ่ายทีหลัง
func (s *Service) Submit(ctx context.Context, cartID string, expected money.Satang, payNow bool) (*Receipt, error) {
	basket, err := s.baskets.Read(ctx, cartID)
	if err != nil {
		return nil, err
	}
	if len(basket.Lines) == 0 {
		return nil, fmt.Errorf("%w: %s", ErrEmptyCart, cartID)
	}
	if expected > 0 && basket.Total() != expected {
		return nil, fmt.Errorf("%w: ตะกร้า %s แต่ลูกค้าเห็น %s", ErrMismatch, basket.Total(), expected)
	}

	orderID, total, err := s.orders.Place(ctx, basket.CustomerID, basket.Lines)
	if err != nil {
		return nil, err
	}

	receipt := &Receipt{OrderID: orderID, Total: total}

	if payNow {
		if err := s.orders.Pay(ctx, orderID); err != nil {
			// ออเดอร์เปิดแล้วแต่จ่ายไม่ผ่าน — ไม่ใช่ error ของ checkout
			// คืน receipt ให้ client ไปจ่ายซ้ำที่ /orders/{id}/pay ได้
			return receipt, fmt.Errorf("checkout: order %s created but payment failed: %w", orderID, err)
		}
		receipt.Paid = true
	}

	// ล้างตะกร้าเป็นขั้นสุดท้ายเสมอ — ถ้าล้มก่อนหน้านี้ ตะกร้าลูกค้าต้องยังอยู่
	if err := s.baskets.Empty(ctx, cartID); err != nil {
		return receipt, fmt.Errorf("checkout: order %s created but cart not cleared: %w", orderID, err)
	}
	return receipt, nil
}
