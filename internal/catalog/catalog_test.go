package catalog_test

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/soya196/go-shop/internal/catalog"
	"github.com/soya196/go-shop/internal/money"
)

// ── fake ที่เขียนเองใน test ── ไม่ต้องมี DB ไม่ต้องมี mock library
// นี่คือเสา "Easy to Test" ที่จับต้องได้: domain ไม่รู้จัก infra จึงเทสได้ด้วย map เปล่าๆ

type fakeRepo struct {
	items   map[string]*catalog.Product
	saveErr error
}

func newFakeRepo() *fakeRepo { return &fakeRepo{items: map[string]*catalog.Product{}} }

func (f *fakeRepo) FindByID(_ context.Context, id string) (*catalog.Product, error) {
	p, ok := f.items[id]
	if !ok {
		return nil, catalog.ErrNotFound
	}
	return p, nil
}

func (f *fakeRepo) FindBySKU(_ context.Context, sku string) (*catalog.Product, error) {
	for _, p := range f.items {
		if p.SKU == sku {
			return p, nil
		}
	}
	return nil, catalog.ErrNotFound
}

func (f *fakeRepo) List(context.Context) ([]*catalog.Product, error) {
	out := make([]*catalog.Product, 0, len(f.items))
	for _, p := range f.items {
		out = append(out, p)
	}
	return out, nil
}

func (f *fakeRepo) Save(_ context.Context, p *catalog.Product) error {
	if f.saveErr != nil {
		return f.saveErr
	}
	f.items[p.ID] = p
	return nil
}

type seqIDs struct{ n int }

func (s *seqIDs) NewID() string { s.n++; return fmt.Sprintf("p%03d", s.n) }

// ────────────────────────── entity rules ──────────────────────────

func TestNewValidates(t *testing.T) {
	if _, err := catalog.New("p1", "SKU", "  ", money.FromBaht(10), 1); !errors.Is(err, catalog.ErrInvalidName) {
		t.Errorf("empty name: got %v", err)
	}
	if _, err := catalog.New("p1", "SKU", "Latte", 0, 1); !errors.Is(err, catalog.ErrInvalidPrice) {
		t.Errorf("zero price: got %v", err)
	}
	if _, err := catalog.New("p1", "SKU", "Latte", money.FromBaht(10), -1); !errors.Is(err, catalog.ErrInvalidQty) {
		t.Errorf("negative stock: got %v", err)
	}
}

func TestReserveRespectsAvailability(t *testing.T) {
	p, err := catalog.New("p1", "LATTE", "Latte", money.FromBaht(85), 10)
	if err != nil {
		t.Fatal(err)
	}

	if err := p.Reserve(4); err != nil {
		t.Fatalf("reserve 4 of 10: %v", err)
	}
	if got := p.Available(); got != 6 {
		t.Fatalf("available = %d, want 6", got)
	}

	// จองเกินที่เหลือ ต้องไม่ผ่าน และต้องไม่แก้ state
	if err := p.Reserve(7); !errors.Is(err, catalog.ErrOutOfStock) {
		t.Fatalf("over-reserve: got %v, want ErrOutOfStock", err)
	}
	if got := p.Reserved; got != 4 {
		t.Fatalf("failed reserve must not mutate: reserved = %d, want 4", got)
	}
}

func TestInactiveProductCannotBeReserved(t *testing.T) {
	p, _ := catalog.New("p1", "LATTE", "Latte", money.FromBaht(85), 10)
	p.Deactivate()
	if err := p.Reserve(1); !errors.Is(err, catalog.ErrInactive) {
		t.Fatalf("got %v, want ErrInactive", err)
	}
}

func TestFulfilCutsRealStock(t *testing.T) {
	p, _ := catalog.New("p1", "LATTE", "Latte", money.FromBaht(85), 10)
	_ = p.Reserve(3)

	if err := p.Fulfil(3); err != nil {
		t.Fatal(err)
	}
	if p.Stock != 7 || p.Reserved != 0 {
		t.Fatalf("after fulfil: stock=%d reserved=%d, want 7/0", p.Stock, p.Reserved)
	}
}

func TestReleaseCannotExceedReserved(t *testing.T) {
	p, _ := catalog.New("p1", "LATTE", "Latte", money.FromBaht(85), 10)
	_ = p.Reserve(2)
	if err := p.Release(3); !errors.Is(err, catalog.ErrReleaseTooMuch) {
		t.Fatalf("got %v, want ErrReleaseTooMuch", err)
	}
}

// ────────────────────────── use cases ──────────────────────────

func TestAddProductRejectsDuplicateSKU(t *testing.T) {
	svc := catalog.NewService(newFakeRepo(), &seqIDs{})
	ctx := context.Background()

	if _, err := svc.AddProduct(ctx, "LATTE", "Latte", money.FromBaht(85), 10); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.AddProduct(ctx, "LATTE", "Latte ซ้ำ", money.FromBaht(90), 5); !errors.Is(err, catalog.ErrDuplicateSKU) {
		t.Fatalf("got %v, want ErrDuplicateSKU", err)
	}
}

func TestBrowseHidesInactive(t *testing.T) {
	svc := catalog.NewService(newFakeRepo(), &seqIDs{})
	ctx := context.Background()

	a, _ := svc.AddProduct(ctx, "LATTE", "Latte", money.FromBaht(85), 10)
	_, _ = svc.AddProduct(ctx, "MOCHA", "Mocha", money.FromBaht(95), 10)
	if err := svc.Deactivate(ctx, a.ID); err != nil {
		t.Fatal(err)
	}

	got, err := svc.Browse(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].Name != "Mocha" {
		t.Fatalf("browse = %d items, want only Mocha", len(got))
	}

	all, _ := svc.ListAll(ctx)
	if len(all) != 2 {
		t.Fatalf("ListAll = %d, want 2", len(all))
	}
}

func TestServicePropagatesEntityRule(t *testing.T) {
	repo := newFakeRepo()
	svc := catalog.NewService(repo, &seqIDs{})
	ctx := context.Background()
	p, _ := svc.AddProduct(ctx, "LATTE", "Latte", money.FromBaht(85), 2)

	if err := svc.Reserve(ctx, p.ID, 5); !errors.Is(err, catalog.ErrOutOfStock) {
		t.Fatalf("service must surface entity rule: got %v", err)
	}
}

func TestGetUnknownProduct(t *testing.T) {
	svc := catalog.NewService(newFakeRepo(), &seqIDs{})
	if _, err := svc.Get(context.Background(), "nope"); !errors.Is(err, catalog.ErrNotFound) {
		t.Fatalf("got %v, want ErrNotFound", err)
	}
}
