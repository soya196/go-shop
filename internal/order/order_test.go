package order_test

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/soya196/go-shop/internal/money"
	"github.com/soya196/go-shop/internal/order"
)

// ── fakes ทั้งหมดเขียนเองใน test — order ไม่รู้จัก catalog/customer/payment จริงเลย ──

type fakeRepo struct{ items map[string]*order.Order }

func newFakeRepo() *fakeRepo { return &fakeRepo{items: map[string]*order.Order{}} }

func (f *fakeRepo) FindByID(_ context.Context, id string) (*order.Order, error) {
	o, ok := f.items[id]
	if !ok {
		return nil, order.ErrNotFound
	}
	return o, nil
}

func (f *fakeRepo) FindByCustomer(_ context.Context, cid string) ([]*order.Order, error) {
	var out []*order.Order
	for _, o := range f.items {
		if o.CustomerID == cid {
			out = append(out, o)
		}
	}
	return out, nil
}

func (f *fakeRepo) List(_ context.Context, st order.Status) ([]*order.Order, error) {
	var out []*order.Order
	for _, o := range f.items {
		if st == "" || o.Status == st {
			out = append(out, o)
		}
	}
	return out, nil
}

func (f *fakeRepo) Save(_ context.Context, o *order.Order) error { f.items[o.ID] = o; return nil }

// fakeStock นับ reserve/release เพื่อพิสูจน์เรื่อง compensating action
type fakeStock struct {
	reserved   map[string]int
	fulfilled  map[string]int
	failOn     string // productID ที่จะให้ Reserve ล้มเหลว
	releaseLog []string
}

func newFakeStock() *fakeStock {
	return &fakeStock{reserved: map[string]int{}, fulfilled: map[string]int{}}
}

func (f *fakeStock) Reserve(_ context.Context, pid string, qty int) error {
	if pid == f.failOn {
		return fmt.Errorf("out of stock: %s", pid)
	}
	f.reserved[pid] += qty
	return nil
}

func (f *fakeStock) Release(_ context.Context, pid string, qty int) error {
	f.reserved[pid] -= qty
	f.releaseLog = append(f.releaseLog, fmt.Sprintf("%s:%d", pid, qty))
	return nil
}

func (f *fakeStock) Fulfil(_ context.Context, pid string, qty int) error {
	f.reserved[pid] -= qty
	f.fulfilled[pid] += qty
	return nil
}

type fakeShoppers struct {
	blocked bool
	opened  int
	closed  int
}

func (f *fakeShoppers) EnsureCanOrder(context.Context, string) error {
	if f.blocked {
		return errors.New("customer suspended")
	}
	return nil
}
func (f *fakeShoppers) OrderOpened(context.Context, string) error { f.opened++; return nil }
func (f *fakeShoppers) OrderClosed(context.Context, string) error { f.closed++; return nil }

type fakeWallet struct {
	declined bool
	charged  money.Satang
	refunds  int
}

func (f *fakeWallet) Collect(_ context.Context, orderID string, amount money.Satang) (string, error) {
	if f.declined {
		return "", errors.New("card declined")
	}
	f.charged = amount
	return "pay_1", nil
}
func (f *fakeWallet) RefundOrder(context.Context, string) error { f.refunds++; return nil }

type seqIDs struct{ n int }

func (s *seqIDs) NewID() string { s.n++; return fmt.Sprintf("o%03d", s.n) }

type fixedClock struct{ t time.Time }

func (c *fixedClock) Now() time.Time { c.t = c.t.Add(time.Second); return c.t }

func newClock() *fixedClock {
	return &fixedClock{t: time.Date(2026, 7, 26, 10, 0, 0, 0, time.UTC)}
}

func lines() []order.Line {
	return []order.Line{
		{ProductID: "p1", Name: "Latte", UnitPrice: money.FromBaht(85), Qty: 2},
		{ProductID: "p2", Name: "Mocha", UnitPrice: money.FromBaht(95), Qty: 1},
	}
}

type rig struct {
	svc      *order.Service
	repo     *fakeRepo
	stock    *fakeStock
	shoppers *fakeShoppers
	wallet   *fakeWallet
}

func newRig() *rig {
	r := &rig{repo: newFakeRepo(), stock: newFakeStock(), shoppers: &fakeShoppers{}, wallet: &fakeWallet{}}
	r.svc = order.NewService(r.repo, r.stock, r.shoppers, r.wallet, &seqIDs{}, newClock())
	return r
}

// ────────────────── state machine (กฎบน entity ล้วนๆ) ──────────────────

func TestHappyPathTransitions(t *testing.T) {
	now := time.Now()
	o, err := order.New("o1", "c1", lines(), now)
	if err != nil {
		t.Fatal(err)
	}
	if o.Status != order.Placed {
		t.Fatalf("new order status = %s, want PLACED", o.Status)
	}

	steps := []struct {
		name string
		fn   func() error
		want order.Status
	}{
		{"pay", func() error { return o.MarkPaid("pay_1", now) }, order.Paid},
		{"prepare", func() error { return o.StartPreparing(now) }, order.Preparing},
		{"ship", func() error { return o.Ship("TH123", now) }, order.Shipped},
		{"deliver", func() error { return o.Deliver(now) }, order.Delivered},
	}
	for _, s := range steps {
		if err := s.fn(); err != nil {
			t.Fatalf("%s: %v", s.name, err)
		}
		if o.Status != s.want {
			t.Fatalf("after %s: status = %s, want %s", s.name, o.Status, s.want)
		}
	}
}

func TestIllegalTransitionsAreRejected(t *testing.T) {
	now := time.Now()

	// ข้ามขั้น: PLACED → SHIPPED ไม่ได้
	o, _ := order.New("o1", "c1", lines(), now)
	if err := o.Ship("TH1", now); !errors.Is(err, order.ErrBadTransition) {
		t.Errorf("PLACED→SHIPPED: got %v, want ErrBadTransition", err)
	}

	// ยกเลิกหลังส่งของแล้วไม่ได้
	o2, _ := order.New("o2", "c1", lines(), now)
	_ = o2.MarkPaid("pay", now)
	_ = o2.StartPreparing(now)
	_ = o2.Ship("TH1", now)
	if err := o2.Cancel(now); !errors.Is(err, order.ErrCannotCancel) {
		t.Errorf("cancel after ship: got %v, want ErrCannotCancel", err)
	}

	// สถานะปลายทางไปต่อไม่ได้
	o3, _ := order.New("o3", "c1", lines(), now)
	_ = o3.Cancel(now)
	if err := o3.MarkPaid("pay", now); !errors.Is(err, order.ErrBadTransition) {
		t.Errorf("pay after cancel: got %v, want ErrBadTransition", err)
	}
}

func TestShipRequiresTracking(t *testing.T) {
	now := time.Now()
	o, _ := order.New("o1", "c1", lines(), now)
	_ = o.MarkPaid("pay", now)
	_ = o.StartPreparing(now)
	if err := o.Ship("", now); err == nil {
		t.Fatal("ship without tracking should fail")
	}
	if o.Status != order.Preparing {
		t.Fatalf("failed ship must not change status, got %s", o.Status)
	}
}

func TestNewValidates(t *testing.T) {
	now := time.Now()
	if _, err := order.New("o1", "", lines(), now); !errors.Is(err, order.ErrInvalidCustomer) {
		t.Errorf("no customer: got %v", err)
	}
	if _, err := order.New("o1", "c1", nil, now); !errors.Is(err, order.ErrNoLines) {
		t.Errorf("no lines: got %v", err)
	}
	bad := []order.Line{{ProductID: "p1", UnitPrice: money.FromBaht(10), Qty: 0}}
	if _, err := order.New("o1", "c1", bad, now); !errors.Is(err, order.ErrInvalidQty) {
		t.Errorf("zero qty: got %v", err)
	}
}

func TestTotal(t *testing.T) {
	o, _ := order.New("o1", "c1", lines(), time.Now())
	if want := money.FromBaht(265); o.Total() != want {
		t.Fatalf("total = %s, want %s", o.Total(), want)
	}
	if o.ItemCount() != 3 {
		t.Fatalf("item count = %d, want 3", o.ItemCount())
	}
}

func TestLinesAreCopiedNotAliased(t *testing.T) {
	src := lines()
	o, _ := order.New("o1", "c1", src, time.Now())
	src[0].Qty = 999 // แก้ slice ต้นทางหลังสร้างออเดอร์
	if o.Lines[0].Qty != 2 {
		t.Fatalf("order lines aliased caller slice: qty = %d", o.Lines[0].Qty)
	}
}

// ────────────────────────── use cases ──────────────────────────

func TestPlaceReservesEveryLine(t *testing.T) {
	r := newRig()
	o, err := r.svc.Place(context.Background(), "c1", lines())
	if err != nil {
		t.Fatal(err)
	}
	if r.stock.reserved["p1"] != 2 || r.stock.reserved["p2"] != 1 {
		t.Fatalf("reserved = %v", r.stock.reserved)
	}
	if r.shoppers.opened != 1 {
		t.Fatalf("OrderOpened called %d times, want 1", r.shoppers.opened)
	}
	if o.Status != order.Placed {
		t.Fatalf("status = %s", o.Status)
	}
}

// 🔑 test สำคัญ: จองบรรทัดแรกผ่าน บรรทัดสองพลาด → ต้องคืนบรรทัดแรก ไม่ทิ้งของค้างจอง
func TestPlaceReleasesReservationsWhenOneLineFails(t *testing.T) {
	r := newRig()
	r.stock.failOn = "p2"

	if _, err := r.svc.Place(context.Background(), "c1", lines()); err == nil {
		t.Fatal("expected failure")
	}
	if got := r.stock.reserved["p1"]; got != 0 {
		t.Fatalf("p1 left reserved = %d, want 0 (compensating release missing)", got)
	}
	if len(r.stock.releaseLog) != 1 || r.stock.releaseLog[0] != "p1:2" {
		t.Fatalf("release log = %v, want [p1:2]", r.stock.releaseLog)
	}
	if r.shoppers.opened != 0 {
		t.Fatalf("must not count an order that failed")
	}
}

func TestPlaceRejectsBlockedCustomerBeforeTouchingStock(t *testing.T) {
	r := newRig()
	r.shoppers.blocked = true

	if _, err := r.svc.Place(context.Background(), "c1", lines()); err == nil {
		t.Fatal("expected failure")
	}
	if len(r.stock.reserved) != 0 {
		t.Fatalf("stock must not be touched, got %v", r.stock.reserved)
	}
}

func TestPayChargesExactTotal(t *testing.T) {
	r := newRig()
	ctx := context.Background()
	o, _ := r.svc.Place(ctx, "c1", lines())

	paid, err := r.svc.Pay(ctx, o.ID)
	if err != nil {
		t.Fatal(err)
	}
	if want := money.FromBaht(265); r.wallet.charged != want {
		t.Fatalf("charged %s, want %s", r.wallet.charged, want)
	}
	if paid.Status != order.Paid || paid.PaymentID != "pay_1" {
		t.Fatalf("order = %+v", paid)
	}
}

func TestPayDeclinedLeavesOrderPlaced(t *testing.T) {
	r := newRig()
	r.wallet.declined = true
	ctx := context.Background()
	o, _ := r.svc.Place(ctx, "c1", lines())

	if _, err := r.svc.Pay(ctx, o.ID); err == nil {
		t.Fatal("expected decline")
	}
	got, _ := r.svc.Get(ctx, o.ID)
	if got.Status != order.Placed {
		t.Fatalf("status = %s, want PLACED", got.Status)
	}
}

func TestPayTwiceIsRejected(t *testing.T) {
	r := newRig()
	ctx := context.Background()
	o, _ := r.svc.Place(ctx, "c1", lines())
	if _, err := r.svc.Pay(ctx, o.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := r.svc.Pay(ctx, o.ID); !errors.Is(err, order.ErrBadTransition) {
		t.Fatalf("got %v, want ErrBadTransition", err)
	}
}

func TestShipFulfilsStock(t *testing.T) {
	r := newRig()
	ctx := context.Background()
	o, _ := r.svc.Place(ctx, "c1", lines())
	_, _ = r.svc.Pay(ctx, o.ID)
	_, _ = r.svc.StartPreparing(ctx, o.ID)

	if _, err := r.svc.Ship(ctx, o.ID, "TH999"); err != nil {
		t.Fatal(err)
	}
	if r.stock.fulfilled["p1"] != 2 || r.stock.fulfilled["p2"] != 1 {
		t.Fatalf("fulfilled = %v", r.stock.fulfilled)
	}
	if r.stock.reserved["p1"] != 0 {
		t.Fatalf("reservation should be consumed, got %v", r.stock.reserved)
	}
}

func TestCancelPaidOrderRefundsAndReleases(t *testing.T) {
	r := newRig()
	ctx := context.Background()
	o, _ := r.svc.Place(ctx, "c1", lines())
	_, _ = r.svc.Pay(ctx, o.ID)

	got, err := r.svc.Cancel(ctx, o.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != order.Cancelled {
		t.Fatalf("status = %s", got.Status)
	}
	if r.wallet.refunds != 1 {
		t.Fatalf("refunds = %d, want 1", r.wallet.refunds)
	}
	if r.stock.reserved["p1"] != 0 || r.stock.reserved["p2"] != 0 {
		t.Fatalf("stock not released: %v", r.stock.reserved)
	}
	if r.shoppers.closed != 1 {
		t.Fatalf("OrderClosed = %d, want 1", r.shoppers.closed)
	}
}

func TestCancelUnpaidOrderDoesNotRefund(t *testing.T) {
	r := newRig()
	ctx := context.Background()
	o, _ := r.svc.Place(ctx, "c1", lines())

	if _, err := r.svc.Cancel(ctx, o.ID); err != nil {
		t.Fatal(err)
	}
	if r.wallet.refunds != 0 {
		t.Fatalf("refunds = %d, want 0", r.wallet.refunds)
	}
}

func TestDeliverClosesCustomerOrder(t *testing.T) {
	r := newRig()
	ctx := context.Background()
	o, _ := r.svc.Place(ctx, "c1", lines())
	_, _ = r.svc.Pay(ctx, o.ID)
	_, _ = r.svc.StartPreparing(ctx, o.ID)
	_, _ = r.svc.Ship(ctx, o.ID, "TH1")

	if _, err := r.svc.Deliver(ctx, o.ID); err != nil {
		t.Fatal(err)
	}
	if r.shoppers.closed != 1 {
		t.Fatalf("OrderClosed = %d, want 1", r.shoppers.closed)
	}
}

func TestListByStatus(t *testing.T) {
	r := newRig()
	ctx := context.Background()
	a, _ := r.svc.Place(ctx, "c1", lines())
	_, _ = r.svc.Place(ctx, "c2", lines())
	_, _ = r.svc.Pay(ctx, a.ID)

	placed, _ := r.svc.List(ctx, order.Placed)
	if len(placed) != 1 {
		t.Fatalf("PLACED = %d, want 1", len(placed))
	}
	all, _ := r.svc.List(ctx, "")
	if len(all) != 2 {
		t.Fatalf("all = %d, want 2", len(all))
	}
}
