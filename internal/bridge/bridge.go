// Package bridge เป็น adapter ที่ต่อ port ของ domain หนึ่ง เข้ากับ service ของอีก domain หนึ่ง
//
// 🔑 นี่คือคำตอบของคำถามที่คลาสไม่ได้ลงรายละเอียด:
// "domain ที่ต้องคุยกันจริงๆ (order ต้องรู้ stock) ทำยังไงไม่ให้ผูกกันถาวร?"
//
// คำตอบ: ไม่มี domain ไหน import domain อื่น — แต่ละตัวประกาศ **port ของตัวเอง**
// แล้วมี package เดียวในระบบ (คือ package นี้) ที่รู้จักทั้งสองฝั่งและต่อท่อให้
//
// ผลที่ได้:
//   - อยากแยก order ออกไปเป็น service ต่างหาก → ลบ bridge ตัวนั้นทิ้ง เขียน HTTP client แทน
//     โดย internal/order ไม่ต้องแก้เลยแม้แต่บรรทัดเดียว
//   - อ่าน package นี้ไฟล์เดียว = เห็น "แผนที่การพึ่งพา" ของทั้งระบบ
//
// bridge ทุกตัวในไฟล์นี้บางมากโดยตั้งใจ — ถ้ามันเริ่มมี if/for แปลว่ามีกฎธุรกิจหลุดมา
// กฎธุรกิจต้องอยู่ใน domain ไม่ใช่ที่นี่
package bridge

import (
	"context"

	"github.com/soya196/go-shop/internal/cart"
	"github.com/soya196/go-shop/internal/catalog"
	"github.com/soya196/go-shop/internal/checkout"
	"github.com/soya196/go-shop/internal/customer"
	"github.com/soya196/go-shop/internal/money"
	"github.com/soya196/go-shop/internal/order"
	"github.com/soya196/go-shop/internal/payment"
)

// ── cart.Catalog ← catalog.Service ────────────────────────────────
// cart อยากรู้ "ชื่อ ราคา ขายได้ไหม" — catalog รู้มากกว่านั้นเยอะ แต่ยัดให้แค่ที่ cart ขอ

type CartCatalog struct{ Catalog *catalog.Service }

var _ cart.Catalog = CartCatalog{}

func (b CartCatalog) Lookup(ctx context.Context, productID string) (cart.ProductInfo, error) {
	p, err := b.Catalog.Get(ctx, productID)
	if err != nil {
		return cart.ProductInfo{}, err
	}
	return cart.ProductInfo{
		ID:       p.ID,
		Name:     p.Name,
		Price:    p.Price,
		Sellable: p.CanSell(1),
	}, nil
}

// ── order.Stock ← catalog.Service ─────────────────────────────────

type OrderStock struct{ Catalog *catalog.Service }

var _ order.Stock = OrderStock{}

func (b OrderStock) Reserve(ctx context.Context, productID string, qty int) error {
	return b.Catalog.Reserve(ctx, productID, qty)
}

func (b OrderStock) Release(ctx context.Context, productID string, qty int) error {
	return b.Catalog.Release(ctx, productID, qty)
}

func (b OrderStock) Fulfil(ctx context.Context, productID string, qty int) error {
	return b.Catalog.Fulfil(ctx, productID, qty)
}

// ── order.Shoppers ← customer.Service ─────────────────────────────

type OrderShoppers struct{ Customers *customer.Service }

var _ order.Shoppers = OrderShoppers{}

func (b OrderShoppers) EnsureCanOrder(ctx context.Context, customerID string) error {
	return b.Customers.EnsureCanOrder(ctx, customerID)
}

func (b OrderShoppers) OrderOpened(ctx context.Context, customerID string) error {
	return b.Customers.OrderOpened(ctx, customerID)
}

func (b OrderShoppers) OrderClosed(ctx context.Context, customerID string) error {
	return b.Customers.OrderClosed(ctx, customerID)
}

// ── order.Wallet ← payment.Service ────────────────────────────────

type OrderWallet struct{ Payments *payment.Service }

var _ order.Wallet = OrderWallet{}

func (b OrderWallet) Collect(ctx context.Context, orderID string, amount money.Satang) (string, error) {
	p, err := b.Payments.Collect(ctx, orderID, amount)
	if err != nil {
		return "", err
	}
	return p.ID, nil
}

func (b OrderWallet) RefundOrder(ctx context.Context, orderID string) error {
	return b.Payments.RefundOrder(ctx, orderID)
}

// ── checkout.Baskets ← cart.Service ───────────────────────────────

type CheckoutBaskets struct{ Carts *cart.Service }

var _ checkout.Baskets = CheckoutBaskets{}

func (b CheckoutBaskets) Read(ctx context.Context, cartID string) (checkout.Basket, error) {
	c, err := b.Carts.Get(ctx, cartID)
	if err != nil {
		return checkout.Basket{}, err
	}
	lines := make([]checkout.Line, 0, len(c.Lines))
	for _, l := range c.Lines {
		lines = append(lines, checkout.Line{
			ProductID: l.ProductID,
			Name:      l.Name,
			UnitPrice: l.UnitPrice,
			Qty:       l.Qty,
		})
	}
	return checkout.Basket{CartID: c.ID, CustomerID: c.CustomerID, Lines: lines}, nil
}

func (b CheckoutBaskets) Empty(ctx context.Context, cartID string) error {
	return b.Carts.Clear(ctx, cartID)
}

// ── checkout.Orders ← order.Service ───────────────────────────────

type CheckoutOrders struct{ Orders *order.Service }

var _ checkout.Orders = CheckoutOrders{}

func (b CheckoutOrders) Place(ctx context.Context, customerID string, lines []checkout.Line) (string, money.Satang, error) {
	converted := make([]order.Line, 0, len(lines))
	for _, l := range lines {
		converted = append(converted, order.Line{
			ProductID: l.ProductID,
			Name:      l.Name,
			UnitPrice: l.UnitPrice,
			Qty:       l.Qty,
		})
	}
	o, err := b.Orders.Place(ctx, customerID, converted)
	if err != nil {
		return "", 0, err
	}
	return o.ID, o.Total(), nil
}

func (b CheckoutOrders) Pay(ctx context.Context, orderID string) error {
	_, err := b.Orders.Pay(ctx, orderID)
	return err
}
