// Package payment เป็น domain ของ "การรับเงิน"
//
// จุดที่ควรดู: Gateway คือ port ฝั่ง driven ที่ชี้ออกไปหาโลกภายนอก (ธนาคาร/PSP)
// payment ไม่รู้ว่าปลายทางเป็น Omise, Stripe หรือ fake — มันรู้แค่ "มีคนรับเรื่องชาร์จเงินให้"
package payment

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/soya196/go-shop/internal/money"
)

var (
	ErrNotFound       = errors.New("payment: not found")
	ErrInvalidAmount  = errors.New("payment: amount must be positive")
	ErrAlreadySettled = errors.New("payment: already settled")
	ErrNotPending     = errors.New("payment: payment is not pending")
	ErrDeclined       = errors.New("payment: declined by gateway")
	ErrNotRefundable  = errors.New("payment: only succeeded payments can be refunded")
)

// Status คือสถานะการชำระเงิน
type Status string

const (
	Pending   Status = "PENDING"
	Succeeded Status = "SUCCEEDED"
	Failed    Status = "FAILED"
	Refunded  Status = "REFUNDED"
)

// Payment คือความพยายามเก็บเงินหนึ่งครั้งของออเดอร์หนึ่งใบ
type Payment struct {
	ID        string
	OrderID   string
	Amount    money.Satang
	Status    Status
	Reference string // เลขอ้างอิงจาก gateway
	Reason    string // เหตุผลตอนล้มเหลว
	CreatedAt time.Time
	SettledAt *time.Time
}

// New สร้างรายการชำระเงินสถานะ PENDING
func New(id, orderID string, amount money.Satang, now time.Time) (*Payment, error) {
	if amount <= 0 {
		return nil, ErrInvalidAmount
	}
	if orderID == "" {
		return nil, fmt.Errorf("payment: orderID is required")
	}
	return &Payment{
		ID:        id,
		OrderID:   orderID,
		Amount:    amount,
		Status:    Pending,
		CreatedAt: now,
	}, nil
}

// Succeed บันทึกว่าเก็บเงินสำเร็จ
func (p *Payment) Succeed(reference string, now time.Time) error {
	if p.Status != Pending {
		return fmt.Errorf("%w: %s", ErrNotPending, p.Status)
	}
	p.Status = Succeeded
	p.Reference = reference
	p.SettledAt = &now
	return nil
}

// Fail บันทึกว่าเก็บเงินไม่สำเร็จ
func (p *Payment) Fail(reason string, now time.Time) error {
	if p.Status != Pending {
		return fmt.Errorf("%w: %s", ErrNotPending, p.Status)
	}
	p.Status = Failed
	p.Reason = reason
	p.SettledAt = &now
	return nil
}

// Refund คืนเงิน — ทำได้เฉพาะรายการที่สำเร็จแล้ว
func (p *Payment) Refund(now time.Time) error {
	if p.Status != Succeeded {
		return fmt.Errorf("%w: %s", ErrNotRefundable, p.Status)
	}
	p.Status = Refunded
	p.SettledAt = &now
	return nil
}

func (p *Payment) IsSettled() bool { return p.Status != Pending }

// Charge คือผลลัพธ์จาก gateway
type Charge struct {
	Reference string
	Approved  bool
	Reason    string
}

// Gateway คือ port ฝั่ง driven ไปหาผู้ให้บริการรับชำระเงิน
//
// ประกาศที่นี่เพราะ payment เป็นผู้ใช้ · adapter จริงอยู่ข้างนอก
type Gateway interface {
	Charge(ctx context.Context, paymentID string, amount money.Satang) (Charge, error)
}

// Clock เป็น port สำหรับเวลา — แยกออกมาเพื่อให้ test deterministic
type Clock interface {
	Now() time.Time
}

// Repository เป็น port ฝั่ง driven
type Repository interface {
	FindByID(ctx context.Context, id string) (*Payment, error)
	FindByOrder(ctx context.Context, orderID string) ([]*Payment, error)
	Save(ctx context.Context, p *Payment) error
}
