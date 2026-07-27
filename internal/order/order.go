// Package order เป็น domain ของ "คำสั่งซื้อ"
//
// นี่คือ package ที่กฎธุรกิจหนาที่สุด และเป็นตัวอย่างที่ชัดที่สุดของสิ่งที่คลาสสอน:
// state machine อยู่บน entity ทั้งหมด — service แค่จัดลำดับ ไม่ตัดสินใจแทน
//
//	PLACED ──pay──► PAID ──prepare──► PREPARING ──ship──► SHIPPED ──deliver──► DELIVERED
//	   │              │                   │
//	   └──cancel──────┴───────────────────┘   (หลัง SHIPPED ยกเลิกไม่ได้แล้ว)
package order

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/soya196/go-shop/internal/money"
)

var (
	ErrNotFound        = errors.New("order: not found")
	ErrNoLines         = errors.New("order: order must have at least one line")
	ErrInvalidQty      = errors.New("order: quantity must be positive")
	ErrInvalidCustomer = errors.New("order: customerID is required")
	ErrCannotCancel    = errors.New("order: order can no longer be cancelled")
	ErrBadTransition   = errors.New("order: invalid status transition")
	ErrAlreadyPaid     = errors.New("order: order is already paid")
)

// Status คือสถานะของออเดอร์
type Status string

const (
	Placed    Status = "PLACED"
	Paid      Status = "PAID"
	Preparing Status = "PREPARING"
	Shipped   Status = "SHIPPED"
	Delivered Status = "DELIVERED"
	Cancelled Status = "CANCELLED"
)

// transitions คือตารางกฎการเปลี่ยนสถานะ — กฎเดียว ที่เดียว อ่านรู้เรื่อง
//
// เขียนเป็น data ไม่ใช่ if ซ้อนกัน → เพิ่มสถานะใหม่ก็แก้ตารางที่เดียว
var transitions = map[Status][]Status{
	Placed:    {Paid, Cancelled},
	Paid:      {Preparing, Cancelled},
	Preparing: {Shipped, Cancelled},
	Shipped:   {Delivered},
	Delivered: {},
	Cancelled: {},
}

// CanTransitionTo บอกว่าเปลี่ยนไปสถานะนั้นได้ไหม
func (s Status) CanTransitionTo(next Status) bool {
	for _, allowed := range transitions[s] {
		if allowed == next {
			return true
		}
	}
	return false
}

// IsOpen บอกว่าออเดอร์ยังเดินอยู่ไหม (ยังไม่ถึงปลายทาง)
func (s Status) IsOpen() bool { return s != Delivered && s != Cancelled }

// Line คือสินค้าหนึ่งบรรทัดในออเดอร์
//
// เก็บ Name/UnitPrice ไว้เป็น snapshot — ราคาในออเดอร์ต้องไม่เปลี่ยนตามแคตตาล็อก
type Line struct {
	ProductID string
	Name      string
	UnitPrice money.Satang
	Qty       int
}

func (l Line) Total() money.Satang { return l.UnitPrice.Mul(l.Qty) }

// Order คือคำสั่งซื้อหนึ่งใบ
type Order struct {
	ID         string
	CustomerID string
	Lines      []Line
	Status     Status
	Tracking   string
	PaymentID  string
	CreatedAt  time.Time
	UpdatedAt  time.Time
}

// New สร้างออเดอร์ใหม่ในสถานะ PLACED
func New(id, customerID string, lines []Line, now time.Time) (*Order, error) {
	if customerID == "" {
		return nil, ErrInvalidCustomer
	}
	if len(lines) == 0 {
		return nil, ErrNoLines
	}
	for _, l := range lines {
		if l.Qty <= 0 {
			return nil, fmt.Errorf("%w: %s", ErrInvalidQty, l.ProductID)
		}
		if err := l.UnitPrice.MustPositive(); err != nil {
			return nil, err
		}
	}
	return &Order{
		ID:         id,
		CustomerID: customerID,
		Lines:      append([]Line(nil), lines...),
		Status:     Placed,
		CreatedAt:  now,
		UpdatedAt:  now,
	}, nil
}

// Total คือยอดรวมของออเดอร์
func (o *Order) Total() money.Satang {
	var sum money.Satang
	for _, l := range o.Lines {
		sum = sum.Add(l.Total())
	}
	return sum
}

// ItemCount คือจำนวนชิ้นรวม
func (o *Order) ItemCount() int {
	n := 0
	for _, l := range o.Lines {
		n += l.Qty
	}
	return n
}

// MarkPaid บันทึกว่าชำระเงินแล้ว
func (o *Order) MarkPaid(paymentID string, now time.Time) error {
	if o.Status == Paid {
		return ErrAlreadyPaid
	}
	if err := o.to(Paid, now); err != nil {
		return err
	}
	o.PaymentID = paymentID
	return nil
}

// StartPreparing เริ่มจัดของ
func (o *Order) StartPreparing(now time.Time) error { return o.to(Preparing, now) }

// Ship ส่งของออกพร้อมเลขพัสดุ
func (o *Order) Ship(tracking string, now time.Time) error {
	if tracking == "" {
		return fmt.Errorf("order: tracking number is required to ship")
	}
	if err := o.to(Shipped, now); err != nil {
		return err
	}
	o.Tracking = tracking
	return nil
}

// Deliver ส่งถึงมือลูกค้า
func (o *Order) Deliver(now time.Time) error { return o.to(Delivered, now) }

// Cancel ยกเลิกออเดอร์ — ทำได้ก่อนของออกจากคลังเท่านั้น
func (o *Order) Cancel(now time.Time) error {
	if !o.Status.CanTransitionTo(Cancelled) {
		return fmt.Errorf("%w: สถานะ %s", ErrCannotCancel, o.Status)
	}
	return o.to(Cancelled, now)
}

// WasPaid บอกว่าออเดอร์นี้เคยจ่ายเงินสำเร็จหรือยัง (ใช้ตัดสินว่าต้องคืนเงินไหม)
func (o *Order) WasPaid() bool { return o.PaymentID != "" }

// to คือประตูเดียวที่เปลี่ยนสถานะได้ — ทุก transition ผ่านตรงนี้
func (o *Order) to(next Status, now time.Time) error {
	if !o.Status.CanTransitionTo(next) {
		return fmt.Errorf("%w: %s → %s", ErrBadTransition, o.Status, next)
	}
	o.Status = next
	o.UpdatedAt = now
	return nil
}

// ── ports ที่ order ประกาศเอง ────────────────────────────────────────
// order ไม่ import catalog / customer / payment — มันบอกแค่ว่า "ฉันต้องการความสามารถแบบนี้"

// Stock คือความสามารถ "จอง/คืน/ตัด" ของในคลัง
type Stock interface {
	Reserve(ctx context.Context, productID string, qty int) error
	Release(ctx context.Context, productID string, qty int) error
	Fulfil(ctx context.Context, productID string, qty int) error
}

// Shoppers คือความสามารถ "ตรวจสิทธิ์ลูกค้า + นับออเดอร์ค้าง"
type Shoppers interface {
	EnsureCanOrder(ctx context.Context, customerID string) error
	OrderOpened(ctx context.Context, customerID string) error
	OrderClosed(ctx context.Context, customerID string) error
}

// Wallet คือความสามารถ "เก็บเงิน / คืนเงิน"
type Wallet interface {
	Collect(ctx context.Context, orderID string, amount money.Satang) (paymentID string, err error)
	RefundOrder(ctx context.Context, orderID string) error
}

// Clock เป็น port สำหรับเวลา
type Clock interface {
	Now() time.Time
}

// IDGenerator เป็น port สำหรับสร้าง id
type IDGenerator interface {
	NewID() string
}

// Repository เป็น port ฝั่ง driven
type Repository interface {
	FindByID(ctx context.Context, id string) (*Order, error)
	FindByCustomer(ctx context.Context, customerID string) ([]*Order, error)
	List(ctx context.Context, status Status) ([]*Order, error)
	Save(ctx context.Context, o *Order) error
}
