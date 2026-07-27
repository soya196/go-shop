package payment_test

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/soya196/go-shop/internal/money"
	"github.com/soya196/go-shop/internal/payment"
)

type fakeRepo struct{ items map[string]*payment.Payment }

func newFakeRepo() *fakeRepo { return &fakeRepo{items: map[string]*payment.Payment{}} }

func (f *fakeRepo) FindByID(_ context.Context, id string) (*payment.Payment, error) {
	p, ok := f.items[id]
	if !ok {
		return nil, payment.ErrNotFound
	}
	return p, nil
}

func (f *fakeRepo) FindByOrder(_ context.Context, orderID string) ([]*payment.Payment, error) {
	var out []*payment.Payment
	for _, p := range f.items {
		if p.OrderID == orderID {
			out = append(out, p)
		}
	}
	return out, nil
}

func (f *fakeRepo) Save(_ context.Context, p *payment.Payment) error {
	f.items[p.ID] = p
	return nil
}

// gateway ปลอม 3 แบบ: อนุมัติ / ปฏิเสธ / พัง
type okGateway struct{}

func (okGateway) Charge(context.Context, string, money.Satang) (payment.Charge, error) {
	return payment.Charge{Reference: "ch_ok_1", Approved: true}, nil
}

type declineGateway struct{}

func (declineGateway) Charge(context.Context, string, money.Satang) (payment.Charge, error) {
	return payment.Charge{Approved: false, Reason: "insufficient funds"}, nil
}

type brokenGateway struct{}

func (brokenGateway) Charge(context.Context, string, money.Satang) (payment.Charge, error) {
	return payment.Charge{}, errors.New("gateway timeout")
}

type seqIDs struct{ n int }

func (s *seqIDs) NewID() string { s.n++; return fmt.Sprintf("pay%03d", s.n) }

type fixedClock struct{ t time.Time }

func (c fixedClock) Now() time.Time { return c.t }

func newClock() fixedClock {
	return fixedClock{t: time.Date(2026, 7, 26, 10, 0, 0, 0, time.UTC)}
}

// ────────────────────────── entity rules ──────────────────────────

func TestCannotSettleTwice(t *testing.T) {
	now := newClock().Now()
	p, err := payment.New("pay1", "o1", money.FromBaht(100), now)
	if err != nil {
		t.Fatal(err)
	}
	if err := p.Succeed("ref", now); err != nil {
		t.Fatal(err)
	}
	if err := p.Succeed("ref2", now); !errors.Is(err, payment.ErrNotPending) {
		t.Fatalf("double succeed: got %v, want ErrNotPending", err)
	}
	if err := p.Fail("nope", now); !errors.Is(err, payment.ErrNotPending) {
		t.Fatalf("fail after succeed: got %v, want ErrNotPending", err)
	}
}

func TestOnlySucceededCanRefund(t *testing.T) {
	now := newClock().Now()
	p, _ := payment.New("pay1", "o1", money.FromBaht(100), now)

	if err := p.Refund(now); !errors.Is(err, payment.ErrNotRefundable) {
		t.Fatalf("refund pending: got %v, want ErrNotRefundable", err)
	}
	_ = p.Succeed("ref", now)
	if err := p.Refund(now); err != nil {
		t.Fatalf("refund succeeded payment: %v", err)
	}
	if p.Status != payment.Refunded {
		t.Fatalf("status = %s, want REFUNDED", p.Status)
	}
}

func TestNewRejectsBadAmount(t *testing.T) {
	now := newClock().Now()
	if _, err := payment.New("pay1", "o1", 0, now); !errors.Is(err, payment.ErrInvalidAmount) {
		t.Fatalf("got %v, want ErrInvalidAmount", err)
	}
}

// ────────────────────────── use cases ──────────────────────────

func TestCollectSuccess(t *testing.T) {
	repo := newFakeRepo()
	svc := payment.NewService(repo, okGateway{}, &seqIDs{}, newClock())

	p, err := svc.Collect(context.Background(), "o1", money.FromBaht(250))
	if err != nil {
		t.Fatal(err)
	}
	if p.Status != payment.Succeeded || p.Reference != "ch_ok_1" {
		t.Fatalf("payment = %+v", p)
	}
	if p.SettledAt == nil {
		t.Fatal("SettledAt should be set")
	}
}

func TestCollectDeclinedIsRecordedNotLost(t *testing.T) {
	repo := newFakeRepo()
	svc := payment.NewService(repo, declineGateway{}, &seqIDs{}, newClock())

	p, err := svc.Collect(context.Background(), "o1", money.FromBaht(250))
	if !errors.Is(err, payment.ErrDeclined) {
		t.Fatalf("got %v, want ErrDeclined", err)
	}
	// สำคัญ: ถึงจะ error ก็ต้องมีร่องรอยว่าเคยพยายามเก็บเงิน
	if p == nil || p.Status != payment.Failed || p.Reason != "insufficient funds" {
		t.Fatalf("failed payment must be persisted, got %+v", p)
	}
	stored, err := repo.FindByID(context.Background(), p.ID)
	if err != nil || stored.Status != payment.Failed {
		t.Fatalf("stored = %+v err=%v", stored, err)
	}
}

func TestCollectGatewayErrorIsRecorded(t *testing.T) {
	repo := newFakeRepo()
	svc := payment.NewService(repo, brokenGateway{}, &seqIDs{}, newClock())

	p, err := svc.Collect(context.Background(), "o1", money.FromBaht(250))
	if !errors.Is(err, payment.ErrDeclined) {
		t.Fatalf("got %v, want ErrDeclined", err)
	}
	if p.Status != payment.Failed || p.Reason == "" {
		t.Fatalf("payment = %+v", p)
	}
}

func TestRefundOrderRefundsOnlySucceeded(t *testing.T) {
	repo := newFakeRepo()
	ctx := context.Background()
	clock := newClock()

	okSvc := payment.NewService(repo, okGateway{}, &seqIDs{n: 0}, clock)
	good, err := okSvc.Collect(ctx, "o1", money.FromBaht(100))
	if err != nil {
		t.Fatal(err)
	}

	badSvc := payment.NewService(repo, declineGateway{}, &seqIDs{n: 50}, clock)
	bad, _ := badSvc.Collect(ctx, "o1", money.FromBaht(100))

	if err := okSvc.RefundOrder(ctx, "o1"); err != nil {
		t.Fatal(err)
	}

	g, _ := repo.FindByID(ctx, good.ID)
	b, _ := repo.FindByID(ctx, bad.ID)
	if g.Status != payment.Refunded {
		t.Errorf("succeeded payment should be refunded, got %s", g.Status)
	}
	if b.Status != payment.Failed {
		t.Errorf("failed payment must stay FAILED, got %s", b.Status)
	}
}
