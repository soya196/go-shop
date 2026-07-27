package customer_test

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/soya196/go-shop/internal/customer"
)

type fakeRepo struct{ items map[string]*customer.Customer }

func newFakeRepo() *fakeRepo { return &fakeRepo{items: map[string]*customer.Customer{}} }

func (f *fakeRepo) FindByID(_ context.Context, id string) (*customer.Customer, error) {
	c, ok := f.items[id]
	if !ok {
		return nil, customer.ErrNotFound
	}
	return c, nil
}

func (f *fakeRepo) FindByEmail(_ context.Context, email string) (*customer.Customer, error) {
	for _, c := range f.items {
		if c.Email == email {
			return c, nil
		}
	}
	return nil, customer.ErrNotFound
}

func (f *fakeRepo) List(context.Context) ([]*customer.Customer, error) {
	out := make([]*customer.Customer, 0, len(f.items))
	for _, c := range f.items {
		out = append(out, c)
	}
	return out, nil
}

func (f *fakeRepo) Save(_ context.Context, c *customer.Customer) error {
	f.items[c.ID] = c
	return nil
}

type seqIDs struct{ n int }

func (s *seqIDs) NewID() string { s.n++; return fmt.Sprintf("c%03d", s.n) }

func TestEmailValidation(t *testing.T) {
	bad := []string{"", "nope", "@x.com", "a@", "a@b", "a b@x.com", "a@@x.com", "a@x."}
	for _, e := range bad {
		if _, err := customer.New("c1", "Somchai", e); !errors.Is(err, customer.ErrInvalidEmail) {
			t.Errorf("email %q should be rejected, got %v", e, err)
		}
	}
	if _, err := customer.New("c1", "Somchai", "  Somchai@Example.CO.TH "); err != nil {
		t.Errorf("valid email rejected: %v", err)
	}
}

func TestEmailIsNormalised(t *testing.T) {
	c, err := customer.New("c1", " Somchai ", "  Somchai@Example.com ")
	if err != nil {
		t.Fatal(err)
	}
	if c.Email != "somchai@example.com" {
		t.Errorf("email = %q, want lowercased+trimmed", c.Email)
	}
	if c.Name != "Somchai" {
		t.Errorf("name = %q, want trimmed", c.Name)
	}
}

func TestOpenOrderLimit(t *testing.T) {
	c, _ := customer.New("c1", "Somchai", "s@x.com")
	for i := 0; i < customer.MaxOpenOrders; i++ {
		if err := c.OrderOpened(); err != nil {
			t.Fatalf("order %d rejected: %v", i+1, err)
		}
	}
	if err := c.OrderOpened(); err == nil {
		t.Fatalf("order %d should be rejected", customer.MaxOpenOrders+1)
	}
	c.OrderClosed()
	if err := c.OrderOpened(); err != nil {
		t.Fatalf("after closing one, should allow again: %v", err)
	}
}

func TestOrderClosedNeverGoesNegative(t *testing.T) {
	c, _ := customer.New("c1", "Somchai", "s@x.com")
	c.OrderClosed()
	c.OrderClosed()
	if c.OpenOrders != 0 {
		t.Fatalf("OpenOrders = %d, want 0", c.OpenOrders)
	}
}

func TestSuspendedCannotOrder(t *testing.T) {
	c, _ := customer.New("c1", "Somchai", "s@x.com")
	c.Suspend()
	if err := c.CanOrder(); !errors.Is(err, customer.ErrSuspended) {
		t.Fatalf("got %v, want ErrSuspended", err)
	}
	c.Restore()
	if err := c.CanOrder(); err != nil {
		t.Fatalf("restored customer should order: %v", err)
	}
}

func TestRegisterRejectsDuplicateEmailCaseInsensitive(t *testing.T) {
	svc := customer.NewService(newFakeRepo(), &seqIDs{})
	ctx := context.Background()

	if _, err := svc.Register(ctx, "Somchai", "s@x.com"); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.Register(ctx, "Somchai 2", "S@X.COM"); !errors.Is(err, customer.ErrDuplicate) {
		t.Fatalf("got %v, want ErrDuplicate", err)
	}
}

func TestEnsureCanOrderThroughService(t *testing.T) {
	svc := customer.NewService(newFakeRepo(), &seqIDs{})
	ctx := context.Background()
	c, _ := svc.Register(ctx, "Somchai", "s@x.com")

	if err := svc.EnsureCanOrder(ctx, c.ID); err != nil {
		t.Fatalf("fresh customer: %v", err)
	}
	if err := svc.Suspend(ctx, c.ID); err != nil {
		t.Fatal(err)
	}
	if err := svc.EnsureCanOrder(ctx, c.ID); !errors.Is(err, customer.ErrSuspended) {
		t.Fatalf("got %v, want ErrSuspended", err)
	}
}
