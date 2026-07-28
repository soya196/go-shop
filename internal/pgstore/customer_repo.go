package pgstore

import (
	"context"
	"fmt"

	"github.com/soya196/go-shop/internal/customer"
	"github.com/soya196/go-shop/internal/pgstore/gen"
)

// Customers implements customer.Repository
type Customers struct{ s *Store }

var _ customer.Repository = (*Customers)(nil)

func (s *Store) Customers() *Customers { return &Customers{s: s} }

func (r *Customers) FindByID(ctx context.Context, id string) (*customer.Customer, error) {
	row, err := r.s.q(ctx).GetCustomer(ctx, id)
	if err != nil {
		return nil, mapErr(err, customer.ErrNotFound, "หาลูกค้า")
	}
	return toCustomer(row), nil
}

func (r *Customers) FindByEmail(ctx context.Context, email string) (*customer.Customer, error) {
	row, err := r.s.q(ctx).GetCustomerByEmail(ctx, email)
	if err != nil {
		return nil, mapErr(err, customer.ErrNotFound, "หาลูกค้าจากอีเมล")
	}
	return toCustomer(row), nil
}

func (r *Customers) List(ctx context.Context) ([]*customer.Customer, error) {
	rows, err := r.s.q(ctx).ListCustomers(ctx)
	if err != nil {
		return nil, fmt.Errorf("pgstore: ลิสต์ลูกค้า: %w", err)
	}
	out := make([]*customer.Customer, 0, len(rows))
	for _, row := range rows {
		out = append(out, toCustomer(row))
	}
	return out, nil
}

func (r *Customers) Save(ctx context.Context, c *customer.Customer) error {
	err := r.s.q(ctx).UpsertCustomer(ctx, gen.UpsertCustomerParams{
		ID:         c.ID,
		Name:       c.Name,
		Email:      c.Email,
		Suspended:  c.Suspended,
		OpenOrders: int32(c.OpenOrders),
	})
	if err != nil {
		return fmt.Errorf("pgstore: บันทึกลูกค้า %s: %w", c.ID, err)
	}
	return nil
}

func toCustomer(row gen.Customer) *customer.Customer {
	return &customer.Customer{
		ID:         row.ID,
		Name:       row.Name,
		Email:      row.Email,
		Suspended:  row.Suspended,
		OpenOrders: int(row.OpenOrders),
	}
}
