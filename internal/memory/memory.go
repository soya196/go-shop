// Package memory เป็น driven adapter ที่เก็บของไว้ในหน่วยความจำ
//
// ทิศทางการพึ่งพา: memory → domain (ชั้นนอกมองเข้าไปเห็น entity ตรงๆ ได้ = Onion)
// ในทางกลับกัน domain ไม่มีบรรทัด import "memory" เลยสักที่ (= Hexagonal)
// ตรวจได้ด้วย: go run ./cmd/archlint
//
// adapter ตัวนี้มีค่าเกินกว่าจะเป็นแค่ของเล่น — มันทำให้รันแอปได้โดยไม่ต้องมี DB
// และเป็นตัวยืนยันว่า port ที่ domain ประกาศไว้ใช้งานได้จริง
package memory

import (
	"context"
	"sort"
	"sync"

	"github.com/soya196/go-shop/internal/cart"
	"github.com/soya196/go-shop/internal/catalog"
	"github.com/soya196/go-shop/internal/customer"
	"github.com/soya196/go-shop/internal/order"
	"github.com/soya196/go-shop/internal/payment"
)

// ─────────────────────────── catalog ───────────────────────────

// Products implements catalog.Repository
type Products struct {
	mu    sync.RWMutex
	items map[string]catalog.Product
}

func NewProducts() *Products { return &Products{items: map[string]catalog.Product{}} }

func (r *Products) FindByID(_ context.Context, id string) (*catalog.Product, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	p, ok := r.items[id]
	if !ok {
		return nil, catalog.ErrNotFound
	}
	return &p, nil // คืน copy — กัน caller แก้ state ในถังโดยไม่ผ่าน Save
}

func (r *Products) FindBySKU(_ context.Context, sku string) (*catalog.Product, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	for _, p := range r.items {
		if p.SKU == sku {
			return &p, nil
		}
	}
	return nil, catalog.ErrNotFound
}

func (r *Products) List(_ context.Context) ([]*catalog.Product, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]*catalog.Product, 0, len(r.items))
	for _, p := range r.items {
		out = append(out, &p)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].SKU < out[j].SKU })
	return out, nil
}

func (r *Products) Save(_ context.Context, p *catalog.Product) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.items[p.ID] = *p
	return nil
}

// ─────────────────────────── customer ───────────────────────────

// Customers implements customer.Repository
type Customers struct {
	mu    sync.RWMutex
	items map[string]customer.Customer
}

func NewCustomers() *Customers { return &Customers{items: map[string]customer.Customer{}} }

func (r *Customers) FindByID(_ context.Context, id string) (*customer.Customer, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	c, ok := r.items[id]
	if !ok {
		return nil, customer.ErrNotFound
	}
	return &c, nil
}

func (r *Customers) FindByEmail(_ context.Context, email string) (*customer.Customer, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	for _, c := range r.items {
		if c.Email == email {
			return &c, nil
		}
	}
	return nil, customer.ErrNotFound
}

func (r *Customers) List(_ context.Context) ([]*customer.Customer, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]*customer.Customer, 0, len(r.items))
	for _, c := range r.items {
		out = append(out, &c)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Email < out[j].Email })
	return out, nil
}

func (r *Customers) Save(_ context.Context, c *customer.Customer) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.items[c.ID] = *c
	return nil
}

// ─────────────────────────── cart ───────────────────────────

// Carts implements cart.Repository
type Carts struct {
	mu    sync.RWMutex
	items map[string]cart.Cart
}

func NewCarts() *Carts { return &Carts{items: map[string]cart.Cart{}} }

func (r *Carts) FindByID(_ context.Context, id string) (*cart.Cart, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	c, ok := r.items[id]
	if !ok {
		return nil, cart.ErrNotFound
	}
	return cloneCart(c), nil
}

func (r *Carts) FindByCustomer(_ context.Context, cid string) (*cart.Cart, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	for _, c := range r.items {
		if c.CustomerID == cid {
			return cloneCart(c), nil
		}
	}
	return nil, cart.ErrNotFound
}

func (r *Carts) Save(_ context.Context, c *cart.Cart) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.items[c.ID] = *cloneCart(*c)
	return nil
}

func (r *Carts) Delete(_ context.Context, id string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	delete(r.items, id)
	return nil
}

// cloneCart คัดลอกลึก — Lines เป็น slice ถ้าไม่ copy จะแชร์ backing array กัน
func cloneCart(c cart.Cart) *cart.Cart {
	out := c
	out.Lines = append([]cart.Line(nil), c.Lines...)
	return &out
}

// ─────────────────────────── order ───────────────────────────

// Orders implements order.Repository
type Orders struct {
	mu    sync.RWMutex
	items map[string]order.Order
	seq   []string // รักษาลำดับการสร้าง ให้ List คืนผลคงที่
}

func NewOrders() *Orders { return &Orders{items: map[string]order.Order{}} }

func (r *Orders) FindByID(_ context.Context, id string) (*order.Order, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	o, ok := r.items[id]
	if !ok {
		return nil, order.ErrNotFound
	}
	return cloneOrder(o), nil
}

func (r *Orders) FindByCustomer(_ context.Context, cid string) ([]*order.Order, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	var out []*order.Order
	for _, id := range r.seq {
		if o := r.items[id]; o.CustomerID == cid {
			out = append(out, cloneOrder(o))
		}
	}
	return out, nil
}

func (r *Orders) List(_ context.Context, st order.Status) ([]*order.Order, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	var out []*order.Order
	for _, id := range r.seq {
		if o := r.items[id]; st == "" || o.Status == st {
			out = append(out, cloneOrder(o))
		}
	}
	return out, nil
}

func (r *Orders) Save(_ context.Context, o *order.Order) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, exists := r.items[o.ID]; !exists {
		r.seq = append(r.seq, o.ID)
	}
	r.items[o.ID] = *cloneOrder(*o)
	return nil
}

func cloneOrder(o order.Order) *order.Order {
	out := o
	out.Lines = append([]order.Line(nil), o.Lines...)
	return &out
}

// ─────────────────────────── payment ───────────────────────────

// Payments implements payment.Repository
type Payments struct {
	mu    sync.RWMutex
	items map[string]payment.Payment
	seq   []string
}

func NewPayments() *Payments { return &Payments{items: map[string]payment.Payment{}} }

func (r *Payments) FindByID(_ context.Context, id string) (*payment.Payment, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	p, ok := r.items[id]
	if !ok {
		return nil, payment.ErrNotFound
	}
	return &p, nil
}

func (r *Payments) FindByOrder(_ context.Context, orderID string) ([]*payment.Payment, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	var out []*payment.Payment
	for _, id := range r.seq {
		if p := r.items[id]; p.OrderID == orderID {
			cp := p
			out = append(out, &cp)
		}
	}
	return out, nil
}

func (r *Payments) Save(_ context.Context, p *payment.Payment) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, exists := r.items[p.ID]; !exists {
		r.seq = append(r.seq, p.ID)
	}
	r.items[p.ID] = *p
	return nil
}
