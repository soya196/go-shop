package money_test

import (
	"encoding/json"
	"testing"

	"github.com/soya196/go-shop/internal/money"
)

func TestFromBahtAvoidsFloatRounding(t *testing.T) {
	// เคสคลาสสิก: 0.1 + 0.2 ในโลก float = 0.30000000000000004
	a := money.FromBaht(0.1)
	b := money.FromBaht(0.2)
	if got, want := a.Add(b), money.FromSatang(30); got != want {
		t.Fatalf("0.1+0.2 = %d satang, want %d", got, want)
	}
}

func TestString(t *testing.T) {
	tests := []struct {
		in   money.Satang
		want string
	}{
		{money.FromSatang(0), "฿0.00"},
		{money.FromSatang(5), "฿0.05"},
		{money.FromSatang(12345), "฿123.45"},
		{money.FromSatang(123450), "฿1,234.50"},
		{money.FromSatang(100000000), "฿1,000,000.00"},
		{money.FromSatang(-12345), "-฿123.45"},
	}
	for _, tc := range tests {
		if got := tc.in.String(); got != tc.want {
			t.Errorf("Satang(%d).String() = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestMulForLineTotal(t *testing.T) {
	unit := money.FromBaht(59.50)
	if got, want := unit.Mul(3), money.FromSatang(17850); got != want {
		t.Fatalf("59.50 x 3 = %s, want ฿178.50", got)
	}
}

func TestJSONRoundTrip(t *testing.T) {
	in := money.FromBaht(1234.5)
	b, err := json.Marshal(in)
	if err != nil {
		t.Fatal(err)
	}
	if want := `{"satang":123450,"text":"฿1,234.50"}`; string(b) != want {
		t.Fatalf("marshal = %s, want %s", b, want)
	}
	var out money.Satang
	if err := json.Unmarshal(b, &out); err != nil {
		t.Fatal(err)
	}
	if out != in {
		t.Fatalf("round trip = %d, want %d", out, in)
	}
	// รับตัวเลขล้วนได้ด้วย
	if err := json.Unmarshal([]byte("999"), &out); err != nil || out != money.FromSatang(999) {
		t.Fatalf("plain number unmarshal failed: %v %d", err, out)
	}
}

func TestMustPositive(t *testing.T) {
	if err := money.FromBaht(10).MustPositive(); err != nil {
		t.Errorf("10 baht should be valid: %v", err)
	}
	if err := money.FromBaht(-1).MustPositive(); err == nil {
		t.Error("negative should be rejected")
	}
}
