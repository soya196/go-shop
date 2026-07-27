// Package fakepay เป็น adapter ฝั่ง driven ที่ปลอมเป็นผู้ให้บริการรับชำระเงิน
//
// ในระบบจริงตรงนี้จะเป็น omise/, stripe/, scb/ — โครงเหมือนกันเป๊ะ
// ประเด็นคือ payment domain ไม่รู้และไม่ต้องรู้ว่าปลายทางเป็นใคร
//
// เปลี่ยนเจ้าผู้ให้บริการ = เขียน adapter ใหม่ 1 ไฟล์ + แก้ main.go 1 บรรทัด
// ไม่ต้องแตะ internal/payment เลยแม้แต่ตัวอักษรเดียว
package fakepay

import (
	"context"
	"fmt"
	"strings"
	"sync"

	"github.com/soya196/go-shop/internal/money"
	"github.com/soya196/go-shop/internal/payment"
)

// Gateway implements payment.Gateway
type Gateway struct {
	mu sync.Mutex
	n  int

	// DeclineOver ถ้ามากกว่า 0 จะปฏิเสธรายการที่เกินยอดนี้
	// (มีไว้ให้ลองยิง API แล้วเห็น path ที่จ่ายไม่ผ่าน)
	DeclineOver money.Satang
}

func New() *Gateway { return &Gateway{} }

func (g *Gateway) Charge(_ context.Context, paymentID string, amount money.Satang) (payment.Charge, error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.n++

	if g.DeclineOver > 0 && amount > g.DeclineOver {
		return payment.Charge{
			Approved: false,
			Reason:   fmt.Sprintf("amount %s exceeds test limit %s", amount, g.DeclineOver),
		}, nil
	}
	return payment.Charge{
		Approved:  true,
		Reference: fmt.Sprintf("ch_%s_%04d", strings.TrimPrefix(paymentID, "pay_"), g.n),
	}, nil
}
