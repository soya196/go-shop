package checkout_test

import (
	"context"
	"errors"
	"testing"

	"github.com/soya196/go-shop/internal/checkout"
	"github.com/soya196/go-shop/internal/money"
)

type fakeBaskets struct {
	basket   checkout.Basket
	readErr  error
	emptyErr error
	emptied  int
}

func (f *fakeBaskets) Read(context.Context, string) (checkout.Basket, error) {
	return f.basket, f.readErr
}
func (f *fakeBaskets) Empty(context.Context, string) error { f.emptied++; return f.emptyErr }

type fakeOrders struct {
	placeErr error
	payErr   error
	placed   int
	paid     int
	gotLines []checkout.Line
}

func (f *fakeOrders) Place(_ context.Context, _ string, lines []checkout.Line) (string, money.Satang, error) {
	if f.placeErr != nil {
		return "", 0, f.placeErr
	}
	f.placed++
	f.gotLines = lines
	var sum money.Satang
	for _, l := range lines {
		sum = sum.Add(l.UnitPrice.Mul(l.Qty))
	}
	return "o1", sum, nil
}

func (f *fakeOrders) Pay(context.Context, string) error {
	if f.payErr != nil {
		return f.payErr
	}
	f.paid++
	return nil
}

func basket() checkout.Basket {
	return checkout.Basket{
		CartID:     "k1",
		CustomerID: "c1",
		Lines: []checkout.Line{
			{ProductID: "p1", Name: "Latte", UnitPrice: money.FromBaht(85), Qty: 2},
			{ProductID: "p2", Name: "Mocha", UnitPrice: money.FromBaht(95), Qty: 1},
		},
	}
}

func TestSubmitPlacesAndClears(t *testing.T) {
	b := &fakeBaskets{basket: basket()}
	o := &fakeOrders{}
	svc := checkout.NewService(b, o)

	r, err := svc.Submit(context.Background(), "k1", 0, false)
	if err != nil {
		t.Fatal(err)
	}
	if r.OrderID != "o1" || r.Paid {
		t.Fatalf("receipt = %+v", r)
	}
	if want := money.FromBaht(265); r.Total != want {
		t.Fatalf("total = %s, want %s", r.Total, want)
	}
	if o.placed != 1 || b.emptied != 1 {
		t.Fatalf("placed=%d emptied=%d, want 1/1", o.placed, b.emptied)
	}
}

func TestSubmitPayNow(t *testing.T) {
	b := &fakeBaskets{basket: basket()}
	o := &fakeOrders{}
	svc := checkout.NewService(b, o)

	r, err := svc.Submit(context.Background(), "k1", 0, true)
	if err != nil {
		t.Fatal(err)
	}
	if !r.Paid || o.paid != 1 {
		t.Fatalf("receipt=%+v paid=%d", r, o.paid)
	}
}

func TestEmptyCartRejected(t *testing.T) {
	b := &fakeBaskets{basket: checkout.Basket{CartID: "k1", CustomerID: "c1"}}
	svc := checkout.NewService(b, &fakeOrders{})

	if _, err := svc.Submit(context.Background(), "k1", 0, false); !errors.Is(err, checkout.ErrEmptyCart) {
		t.Fatalf("got %v, want ErrEmptyCart", err)
	}
	if b.emptied != 0 {
		t.Fatal("must not clear a cart we refused to check out")
	}
}

// 🔑 กันเคสราคาขยับระหว่างลูกค้ากดจ่าย
func TestPriceMismatchIsRejectedBeforeOrdering(t *testing.T) {
	b := &fakeBaskets{basket: basket()} // จริง 265
	o := &fakeOrders{}
	svc := checkout.NewService(b, o)

	_, err := svc.Submit(context.Background(), "k1", money.FromBaht(200), false)
	if !errors.Is(err, checkout.ErrMismatch) {
		t.Fatalf("got %v, want ErrMismatch", err)
	}
	if o.placed != 0 {
		t.Fatal("must not place an order on price mismatch")
	}
}

func TestMatchingExpectedTotalPasses(t *testing.T) {
	svc := checkout.NewService(&fakeBaskets{basket: basket()}, &fakeOrders{})
	if _, err := svc.Submit(context.Background(), "k1", money.FromBaht(265), false); err != nil {
		t.Fatal(err)
	}
}

// ออเดอร์เปิดแล้วแต่จ่ายไม่ผ่าน → ต้องคืน receipt ให้ไปจ่ายซ้ำได้ ไม่ใช่กลืนหาย
func TestPaymentFailureStillReturnsOrder(t *testing.T) {
	b := &fakeBaskets{basket: basket()}
	o := &fakeOrders{payErr: errors.New("card declined")}
	svc := checkout.NewService(b, o)

	r, err := svc.Submit(context.Background(), "k1", 0, true)
	if err == nil {
		t.Fatal("expected payment error")
	}
	if r == nil || r.OrderID != "o1" || r.Paid {
		t.Fatalf("receipt must carry the created order, got %+v", r)
	}
	if b.emptied != 0 {
		t.Fatal("cart must survive a failed payment")
	}
}

func TestPlaceFailureKeepsCart(t *testing.T) {
	b := &fakeBaskets{basket: basket()}
	o := &fakeOrders{placeErr: errors.New("out of stock")}
	svc := checkout.NewService(b, o)

	if _, err := svc.Submit(context.Background(), "k1", 0, false); err == nil {
		t.Fatal("expected place error")
	}
	if b.emptied != 0 {
		t.Fatal("cart must survive a failed order placement")
	}
}
