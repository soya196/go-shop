package cart_test

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/soya196/go-shop/internal/cart"
	"github.com/soya196/go-shop/internal/money"
)

// 🔑 test นี้พิสูจน์ประเด็นสำคัญ: เทส cart ได้ครบ **โดยไม่ต้องมี catalog อยู่ในโปรเจกต์เลย**
// fakeCatalog ข้างล่างคือ Catalog port ทั้งอันแบบง่ายๆ

type fakeCatalog struct{ items map[string]cart.ProductInfo }

func (f fakeCatalog) Lookup(_ context.Context, id string) (cart.ProductInfo, error) {
	p, ok := f.items[id]
	if !ok {
		return cart.ProductInfo{}, fmt.Errorf("no product %s", id)
	}
	return p, nil
}

type fakeRepo struct{ items map[string]*cart.Cart }

func newFakeRepo() *fakeRepo { return &fakeRepo{items: map[string]*cart.Cart{}} }

func (f *fakeRepo) FindByID(_ context.Context, id string) (*cart.Cart, error) {
	c, ok := f.items[id]
	if !ok {
		return nil, cart.ErrNotFound
	}
	return c, nil
}

func (f *fakeRepo) FindByCustomer(_ context.Context, cid string) (*cart.Cart, error) {
	for _, c := range f.items {
		if c.CustomerID == cid {
			return c, nil
		}
	}
	return nil, cart.ErrNotFound
}

func (f *fakeRepo) Save(_ context.Context, c *cart.Cart) error { f.items[c.ID] = c; return nil }
func (f *fakeRepo) Delete(_ context.Context, id string) error  { delete(f.items, id); return nil }

type seqIDs struct{ n int }

func (s *seqIDs) NewID() string { s.n++; return fmt.Sprintf("k%03d", s.n) }

func latte() cart.ProductInfo {
	return cart.ProductInfo{ID: "p1", Name: "Latte", Price: money.FromBaht(85), Sellable: true}
}
func mocha() cart.ProductInfo {
	return cart.ProductInfo{ID: "p2", Name: "Mocha", Price: money.FromBaht(95), Sellable: true}
}

// ────────────────────────── entity rules ──────────────────────────

func TestAddSameProductAccumulates(t *testing.T) {
	c := cart.New("k1", "c1")
	if err := c.Add(latte(), 2); err != nil {
		t.Fatal(err)
	}
	if err := c.Add(latte(), 3); err != nil {
		t.Fatal(err)
	}
	if len(c.Lines) != 1 {
		t.Fatalf("lines = %d, want 1", len(c.Lines))
	}
	if c.Lines[0].Qty != 5 {
		t.Fatalf("qty = %d, want 5", c.Lines[0].Qty)
	}
}

func TestTotalUsesLinePriceNotLiveCatalog(t *testing.T) {
	c := cart.New("k1", "c1")
	_ = c.Add(latte(), 2) // 85.00 x 2
	_ = c.Add(mocha(), 1) // 95.00 x 1

	want := money.FromBaht(265) // 170 + 95
	if got := c.Total(); got != want {
		t.Fatalf("total = %s, want %s", got, want)
	}
	if got := c.ItemCount(); got != 3 {
		t.Fatalf("item count = %d, want 3", got)
	}
}

func TestCannotAddUnsellableProduct(t *testing.T) {
	c := cart.New("k1", "c1")
	p := latte()
	p.Sellable = false
	if err := c.Add(p, 1); !errors.Is(err, cart.ErrNotSellable) {
		t.Fatalf("got %v, want ErrNotSellable", err)
	}
}

func TestSetQtyZeroRemovesLine(t *testing.T) {
	c := cart.New("k1", "c1")
	_ = c.Add(latte(), 2)
	_ = c.Add(mocha(), 1)

	if err := c.SetQty("p1", 0); err != nil {
		t.Fatal(err)
	}
	if len(c.Lines) != 1 || c.Lines[0].ProductID != "p2" {
		t.Fatalf("lines after remove = %+v", c.Lines)
	}
}

func TestRemoveUnknownLine(t *testing.T) {
	c := cart.New("k1", "c1")
	if err := c.Remove("ghost"); !errors.Is(err, cart.ErrLineNotFound) {
		t.Fatalf("got %v, want ErrLineNotFound", err)
	}
}

func TestMaxLines(t *testing.T) {
	c := cart.New("k1", "c1")
	for i := 0; i < cart.MaxLines; i++ {
		p := cart.ProductInfo{ID: fmt.Sprintf("p%d", i), Name: "x", Price: money.FromBaht(1), Sellable: true}
		if err := c.Add(p, 1); err != nil {
			t.Fatalf("line %d: %v", i, err)
		}
	}
	over := cart.ProductInfo{ID: "over", Name: "x", Price: money.FromBaht(1), Sellable: true}
	if err := c.Add(over, 1); !errors.Is(err, cart.ErrTooManyLines) {
		t.Fatalf("got %v, want ErrTooManyLines", err)
	}
}

// ────────────────────────── use cases ──────────────────────────

func newSvc() (*cart.Service, *fakeRepo) {
	repo := newFakeRepo()
	cat := fakeCatalog{items: map[string]cart.ProductInfo{"p1": latte(), "p2": mocha()}}
	return cart.NewService(repo, cat, &seqIDs{}), repo
}

func TestOpenForIsIdempotent(t *testing.T) {
	svc, _ := newSvc()
	ctx := context.Background()

	a, err := svc.OpenFor(ctx, "c1")
	if err != nil {
		t.Fatal(err)
	}
	b, err := svc.OpenFor(ctx, "c1")
	if err != nil {
		t.Fatal(err)
	}
	if a.ID != b.ID {
		t.Fatalf("second OpenFor made a new cart: %s vs %s", a.ID, b.ID)
	}
}

func TestAddItemPullsPriceThroughPort(t *testing.T) {
	svc, _ := newSvc()
	ctx := context.Background()
	c, _ := svc.OpenFor(ctx, "c1")

	got, err := svc.AddItem(ctx, c.ID, "p1", 2)
	if err != nil {
		t.Fatal(err)
	}
	if want := money.FromBaht(170); got.Total() != want {
		t.Fatalf("total = %s, want %s", got.Total(), want)
	}
}

func TestAddUnknownProductFails(t *testing.T) {
	svc, _ := newSvc()
	ctx := context.Background()
	c, _ := svc.OpenFor(ctx, "c1")

	if _, err := svc.AddItem(ctx, c.ID, "ghost", 1); err == nil {
		t.Fatal("expected error for unknown product")
	}
}

func TestClear(t *testing.T) {
	svc, _ := newSvc()
	ctx := context.Background()
	c, _ := svc.OpenFor(ctx, "c1")
	_, _ = svc.AddItem(ctx, c.ID, "p1", 1)

	if err := svc.Clear(ctx, c.ID); err != nil {
		t.Fatal(err)
	}
	got, _ := svc.Get(ctx, c.ID)
	if !got.IsEmpty() {
		t.Fatalf("cart should be empty, got %+v", got.Lines)
	}
}
