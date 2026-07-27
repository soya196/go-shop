// Package jsonstore เป็น driven adapter ตัวที่สอง — เก็บของลงไฟล์ JSON
//
// 🔑 เหตุผลที่ package นี้มีอยู่: เพื่อ **พิสูจน์** ข้ออ้างของ Clean Architecture
//
//	go run ./cmd/api                 → ใช้ memory
//	go run ./cmd/api -store=json     → ใช้ jsonstore
//
// สลับที่เก็บข้อมูลทั้งระบบด้วย flag เดียว โดยที่ internal/catalog, internal/order ฯลฯ
// ไม่ถูกแก้แม้แต่ตัวอักษรเดียว — นั่นคือเทสข้อ 1 ที่คลาสให้ไว้:
// "เปลี่ยนชนิด database แล้วต้องแก้ use case ไหม? ถ้าไม่ต้อง → ผ่าน"
//
// (เขียนแบบง่ายสุด: โหลดทั้งไฟล์ตอนเปิด เขียนทั้งไฟล์ตอน Save — พอสำหรับ demo
// ของจริงจะเป็น postgres/ ที่ implement interface ชุดเดียวกันนี้)
package jsonstore

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"sync"

	"github.com/soya196/go-shop/internal/cart"
	"github.com/soya196/go-shop/internal/catalog"
	"github.com/soya196/go-shop/internal/customer"
	"github.com/soya196/go-shop/internal/order"
	"github.com/soya196/go-shop/internal/payment"
)

// table คือถังเก็บของแบบ generic ที่ทุก repository ใช้ร่วมกัน
type table[T any] struct {
	mu    sync.RWMutex
	path  string
	items map[string]T
	order []string
}

func openTable[T any](dir, name string) (*table[T], error) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, fmt.Errorf("jsonstore: mkdir %s: %w", dir, err)
	}
	t := &table[T]{
		path:  filepath.Join(dir, name+".json"),
		items: map[string]T{},
	}
	raw, err := os.ReadFile(t.path)
	if os.IsNotExist(err) {
		return t, nil
	}
	if err != nil {
		return nil, fmt.Errorf("jsonstore: read %s: %w", t.path, err)
	}
	var payload struct {
		Order []string     `json:"order"`
		Items map[string]T `json:"items"`
	}
	if err := json.Unmarshal(raw, &payload); err != nil {
		return nil, fmt.Errorf("jsonstore: parse %s: %w", t.path, err)
	}
	if payload.Items != nil {
		t.items = payload.Items
	}
	t.order = payload.Order
	return t, nil
}

func (t *table[T]) get(id string) (T, bool) {
	t.mu.RLock()
	defer t.mu.RUnlock()
	v, ok := t.items[id]
	return v, ok
}

func (t *table[T]) all() []T {
	t.mu.RLock()
	defer t.mu.RUnlock()
	out := make([]T, 0, len(t.items))
	for _, id := range t.order {
		if v, ok := t.items[id]; ok {
			out = append(out, v)
		}
	}
	return out
}

func (t *table[T]) put(id string, v T) error {
	t.mu.Lock()
	defer t.mu.Unlock()
	if _, exists := t.items[id]; !exists {
		t.order = append(t.order, id)
	}
	t.items[id] = v
	return t.flushLocked()
}

func (t *table[T]) del(id string) error {
	t.mu.Lock()
	defer t.mu.Unlock()
	delete(t.items, id)
	for i, x := range t.order {
		if x == id {
			t.order = append(t.order[:i], t.order[i+1:]...)
			break
		}
	}
	return t.flushLocked()
}

// flushLocked เขียนไฟล์แบบ atomic — เขียนไฟล์ชั่วคราวแล้ว rename ทับ
// กันไฟล์พังครึ่งๆ กลางๆ ถ้าโปรเซสตายระหว่างเขียน
func (t *table[T]) flushLocked() error {
	payload := struct {
		Order []string     `json:"order"`
		Items map[string]T `json:"items"`
	}{t.order, t.items}

	raw, err := json.MarshalIndent(payload, "", "  ")
	if err != nil {
		return fmt.Errorf("jsonstore: encode %s: %w", t.path, err)
	}
	tmp := t.path + ".tmp"
	if err := os.WriteFile(tmp, raw, 0o644); err != nil {
		return fmt.Errorf("jsonstore: write %s: %w", tmp, err)
	}
	if err := os.Rename(tmp, t.path); err != nil {
		return fmt.Errorf("jsonstore: rename %s: %w", t.path, err)
	}
	return nil
}

// Store รวม repository ทุกตัวที่ผูกกับโฟลเดอร์เดียวกัน
type Store struct {
	Products  *Products
	Customers *Customers
	Carts     *Carts
	Orders    *Orders
	Payments  *Payments
}

// Open เปิด (หรือสร้าง) ที่เก็บข้อมูลในโฟลเดอร์ dir
func Open(dir string) (*Store, error) {
	products, err := openTable[catalog.Product](dir, "products")
	if err != nil {
		return nil, err
	}
	customers, err := openTable[customer.Customer](dir, "customers")
	if err != nil {
		return nil, err
	}
	carts, err := openTable[cart.Cart](dir, "carts")
	if err != nil {
		return nil, err
	}
	orders, err := openTable[order.Order](dir, "orders")
	if err != nil {
		return nil, err
	}
	payments, err := openTable[payment.Payment](dir, "payments")
	if err != nil {
		return nil, err
	}
	return &Store{
		Products:  &Products{t: products},
		Customers: &Customers{t: customers},
		Carts:     &Carts{t: carts},
		Orders:    &Orders{t: orders},
		Payments:  &Payments{t: payments},
	}, nil
}

// ─────────────────────────── catalog ───────────────────────────

type Products struct{ t *table[catalog.Product] }

func (r *Products) FindByID(_ context.Context, id string) (*catalog.Product, error) {
	p, ok := r.t.get(id)
	if !ok {
		return nil, catalog.ErrNotFound
	}
	return &p, nil
}

func (r *Products) FindBySKU(_ context.Context, sku string) (*catalog.Product, error) {
	for _, p := range r.t.all() {
		if p.SKU == sku {
			return &p, nil
		}
	}
	return nil, catalog.ErrNotFound
}

func (r *Products) List(_ context.Context) ([]*catalog.Product, error) {
	all := r.t.all()
	out := make([]*catalog.Product, 0, len(all))
	for _, p := range all {
		out = append(out, &p)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].SKU < out[j].SKU })
	return out, nil
}

func (r *Products) Save(_ context.Context, p *catalog.Product) error { return r.t.put(p.ID, *p) }

// ─────────────────────────── customer ───────────────────────────

type Customers struct{ t *table[customer.Customer] }

func (r *Customers) FindByID(_ context.Context, id string) (*customer.Customer, error) {
	c, ok := r.t.get(id)
	if !ok {
		return nil, customer.ErrNotFound
	}
	return &c, nil
}

func (r *Customers) FindByEmail(_ context.Context, email string) (*customer.Customer, error) {
	for _, c := range r.t.all() {
		if c.Email == email {
			return &c, nil
		}
	}
	return nil, customer.ErrNotFound
}

func (r *Customers) List(_ context.Context) ([]*customer.Customer, error) {
	all := r.t.all()
	out := make([]*customer.Customer, 0, len(all))
	for _, c := range all {
		out = append(out, &c)
	}
	return out, nil
}

func (r *Customers) Save(_ context.Context, c *customer.Customer) error { return r.t.put(c.ID, *c) }

// ─────────────────────────── cart ───────────────────────────

type Carts struct{ t *table[cart.Cart] }

func (r *Carts) FindByID(_ context.Context, id string) (*cart.Cart, error) {
	c, ok := r.t.get(id)
	if !ok {
		return nil, cart.ErrNotFound
	}
	return &c, nil
}

func (r *Carts) FindByCustomer(_ context.Context, cid string) (*cart.Cart, error) {
	for _, c := range r.t.all() {
		if c.CustomerID == cid {
			return &c, nil
		}
	}
	return nil, cart.ErrNotFound
}

func (r *Carts) Save(_ context.Context, c *cart.Cart) error { return r.t.put(c.ID, *c) }
func (r *Carts) Delete(_ context.Context, id string) error  { return r.t.del(id) }

// ─────────────────────────── order ───────────────────────────

type Orders struct{ t *table[order.Order] }

func (r *Orders) FindByID(_ context.Context, id string) (*order.Order, error) {
	o, ok := r.t.get(id)
	if !ok {
		return nil, order.ErrNotFound
	}
	return &o, nil
}

func (r *Orders) FindByCustomer(_ context.Context, cid string) ([]*order.Order, error) {
	var out []*order.Order
	for _, o := range r.t.all() {
		if o.CustomerID == cid {
			out = append(out, &o)
		}
	}
	return out, nil
}

func (r *Orders) List(_ context.Context, st order.Status) ([]*order.Order, error) {
	var out []*order.Order
	for _, o := range r.t.all() {
		if st == "" || o.Status == st {
			out = append(out, &o)
		}
	}
	return out, nil
}

func (r *Orders) Save(_ context.Context, o *order.Order) error { return r.t.put(o.ID, *o) }

// ─────────────────────────── payment ───────────────────────────

type Payments struct{ t *table[payment.Payment] }

func (r *Payments) FindByID(_ context.Context, id string) (*payment.Payment, error) {
	p, ok := r.t.get(id)
	if !ok {
		return nil, payment.ErrNotFound
	}
	return &p, nil
}

func (r *Payments) FindByOrder(_ context.Context, orderID string) ([]*payment.Payment, error) {
	var out []*payment.Payment
	for _, p := range r.t.all() {
		if p.OrderID == orderID {
			out = append(out, &p)
		}
	}
	return out, nil
}

func (r *Payments) Save(_ context.Context, p *payment.Payment) error { return r.t.put(p.ID, *p) }
