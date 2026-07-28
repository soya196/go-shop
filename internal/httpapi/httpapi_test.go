package httpapi_test

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/soya196/go-shop/internal/bridge"
	"github.com/soya196/go-shop/internal/cart"
	"github.com/soya196/go-shop/internal/catalog"
	"github.com/soya196/go-shop/internal/checkout"
	"github.com/soya196/go-shop/internal/clock"
	"github.com/soya196/go-shop/internal/customer"
	"github.com/soya196/go-shop/internal/fakepay"
	"github.com/soya196/go-shop/internal/httpapi"
	"github.com/soya196/go-shop/internal/memory"
	"github.com/soya196/go-shop/internal/order"
	"github.com/soya196/go-shop/internal/payment"
	"github.com/soya196/go-shop/internal/uid"
)

// cfgOverride ให้แต่ละ test ปรับ config ได้โดยไม่ต้องเขียน wiring ซ้ำ
type cfgOverride struct {
	docsEnabled    bool
	allowedOrigins []string
}

func newServer(t *testing.T) *httptest.Server {
	t.Helper()
	return newServerWith(t, nil)
}

func newServerWith(t *testing.T, tweak func(*cfgOverride)) *httptest.Server {
	t.Helper()
	return newTestServer(t, newAPIWith(t, tweak).Routes())
}

// newTestServer ใช้ตอนอยากถือ *API ไว้เอง (เช่นเรียก SetReady ระหว่างเทส)
func newTestServer(t *testing.T, h http.Handler) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	return srv
}

func newAPI(t *testing.T) *httpapi.API {
	t.Helper()
	return newAPIWith(t, nil)
}

// newAPIWith ประกอบระบบทั้งก้อนแบบเดียวกับ cmd/api แต่ใช้ id/clock ที่คาดเดาได้
//
// สังเกตว่า wiring นี้เกือบเหมือน main.go เป๊ะ — ต่างแค่ adapter 2 ตัว
// นั่นคือหลักฐานว่า "เปลี่ยน adapter ได้โดยไม่แตะ domain" เป็นจริง ไม่ใช่แค่คำโฆษณา
func newAPIWith(t *testing.T, tweak func(*cfgOverride)) *httpapi.API {
	t.Helper()

	over := cfgOverride{docsEnabled: true}
	if tweak != nil {
		tweak(&over)
	}

	products := memory.NewProducts()
	customers := memory.NewCustomers()
	carts := memory.NewCarts()
	orders := memory.NewOrders()
	payments := memory.NewPayments()

	fixed := clock.Fixed{At: time.Date(2026, 7, 26, 9, 0, 0, 0, time.UTC)}
	gw := fakepay.New()

	catalogSvc := catalog.NewService(products, &uid.Sequential{Prefix: "prd"})
	customerSvc := customer.NewService(customers, &uid.Sequential{Prefix: "cus"})
	paymentSvc := payment.NewService(payments, gw, &uid.Sequential{Prefix: "pay"}, fixed)
	cartSvc := cart.NewService(carts, bridge.CartCatalog{Catalog: catalogSvc}, &uid.Sequential{Prefix: "crt"})
	orderSvc := order.NewService(
		orders,
		bridge.OrderStock{Catalog: catalogSvc},
		bridge.OrderShoppers{Customers: customerSvc},
		bridge.OrderWallet{Payments: paymentSvc},
		&uid.Sequential{Prefix: "ord"},
		fixed,
		passthroughTx{},
	)
	checkoutSvc := checkout.NewService(
		bridge.CheckoutBaskets{Carts: cartSvc},
		bridge.CheckoutOrders{Orders: orderSvc},
	)

	return httpapi.New(httpapi.Services{
		Catalog:   catalogSvc,
		Customers: customerSvc,
		Carts:     cartSvc,
		Orders:    orderSvc,
		Payments:  paymentSvc,
		Checkout:  checkoutSvc,
	}, slog.New(slog.DiscardHandler), httpapi.Config{
		Version:        "test",
		DocsEnabled:    over.docsEnabled,
		AllowedOrigins: over.allowedOrigins,
	})
}

// call ยิง request แล้วคืน status + body ที่ decode แล้ว
func call(t *testing.T, srv *httptest.Server, method, path string, body any) (int, map[string]any) {
	t.Helper()
	var rdr io.Reader
	if body != nil {
		raw, err := json.Marshal(body)
		if err != nil {
			t.Fatal(err)
		}
		rdr = bytes.NewReader(raw)
	}
	req, err := http.NewRequest(method, srv.URL+path, rdr)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := srv.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	raw, _ := io.ReadAll(resp.Body)
	out := map[string]any{}
	if len(bytes.TrimSpace(raw)) > 0 {
		if err := json.Unmarshal(raw, &out); err != nil {
			t.Fatalf("%s %s: body is not a JSON object: %s", method, path, raw)
		}
	}
	return resp.StatusCode, out
}

func mustStatus(t *testing.T, want, got int, what string, body map[string]any) {
	t.Helper()
	if got != want {
		t.Fatalf("%s: status %d, want %d (body: %v)", what, got, want, body)
	}
}

func str(t *testing.T, m map[string]any, key string) string {
	t.Helper()
	v, ok := m[key].(string)
	if !ok {
		t.Fatalf("field %q missing or not a string in %v", key, m)
	}
	return v
}

// satang ดึงค่าเงินออกจาก {"satang":n,"text":"..."}
func satang(t *testing.T, m map[string]any, key string) int64 {
	t.Helper()
	obj, ok := m[key].(map[string]any)
	if !ok {
		t.Fatalf("field %q is not a money object in %v", key, m)
	}
	f, ok := obj["satang"].(float64)
	if !ok {
		t.Fatalf("money %q has no satang in %v", key, obj)
	}
	return int64(f)
}

// ─────────────────────── flow ซื้อของครบวงจร ───────────────────────

func TestFullShoppingJourney(t *testing.T) {
	srv := newServer(t)

	// 1) ร้านเพิ่มสินค้า
	code, latte := call(t, srv, "POST", "/products", map[string]any{
		"sku": "COF-LAT", "name": "Latte", "price_thb": "85.00", "stock": 10,
	})
	mustStatus(t, http.StatusCreated, code, "create latte", latte)
	if got := satang(t, latte, "price"); got != 8500 {
		t.Fatalf("price = %d satang, want 8500", got)
	}

	code, cake := call(t, srv, "POST", "/products", map[string]any{
		"sku": "BAK-CAK", "name": "เค้ก", "price_thb": "129.50", "stock": 3,
	})
	mustStatus(t, http.StatusCreated, code, "create cake", cake)

	// 2) ลูกค้าสมัคร
	code, cus := call(t, srv, "POST", "/customers", map[string]any{
		"name": "สนธยา", "email": "sonthaya@example.com",
	})
	mustStatus(t, http.StatusCreated, code, "register customer", cus)

	// 3) เปิดตะกร้า + หยิบของ
	code, ct := call(t, srv, "POST", "/carts", map[string]any{"customer_id": str(t, cus, "id")})
	mustStatus(t, http.StatusCreated, code, "open cart", ct)
	cartID := str(t, ct, "id")

	code, ct = call(t, srv, "POST", "/carts/"+cartID+"/items",
		map[string]any{"product_id": str(t, latte, "id"), "qty": 2})
	mustStatus(t, http.StatusOK, code, "add latte", ct)

	code, ct = call(t, srv, "POST", "/carts/"+cartID+"/items",
		map[string]any{"product_id": str(t, cake, "id"), "qty": 1})
	mustStatus(t, http.StatusOK, code, "add cake", ct)

	// 85*2 + 129.50 = 299.50
	if got := satang(t, ct, "total"); got != 29950 {
		t.Fatalf("cart total = %d satang, want 29950", got)
	}

	// 4) checkout + จ่ายเงินทันที
	code, receipt := call(t, srv, "POST", "/carts/"+cartID+"/checkout",
		map[string]any{"expected_satang": 29950, "pay_now": true})
	mustStatus(t, http.StatusCreated, code, "checkout", receipt)
	if paid, _ := receipt["paid"].(bool); !paid {
		t.Fatalf("receipt not paid: %v", receipt)
	}
	orderID := str(t, receipt, "order_id")

	// 5) ตะกร้าต้องว่างแล้ว
	code, ct = call(t, srv, "GET", "/carts/"+cartID, nil)
	mustStatus(t, http.StatusOK, code, "get cart", ct)
	if got := satang(t, ct, "total"); got != 0 {
		t.Fatalf("cart should be empty after checkout, total = %d", got)
	}

	// 6) สต็อกต้องถูกจองแล้ว (ยังไม่ตัด)
	code, p := call(t, srv, "GET", "/products/"+str(t, latte, "id"), nil)
	mustStatus(t, http.StatusOK, code, "get latte", p)
	if p["reserved"].(float64) != 2 || p["stock"].(float64) != 10 || p["available"].(float64) != 8 {
		t.Fatalf("after order: stock=%v reserved=%v available=%v, want 10/2/8",
			p["stock"], p["reserved"], p["available"])
	}

	// 7) ร้านเดินสถานะ: prepare → ship → deliver
	code, o := call(t, srv, "POST", "/orders/"+orderID+"/prepare", nil)
	mustStatus(t, http.StatusOK, code, "prepare", o)
	if o["status"] != "PREPARING" {
		t.Fatalf("status = %v", o["status"])
	}

	code, o = call(t, srv, "POST", "/orders/"+orderID+"/ship", map[string]any{"tracking": "TH0001"})
	mustStatus(t, http.StatusOK, code, "ship", o)

	// ส่งของแล้ว → สต็อกจริงต้องถูกตัด
	code, p = call(t, srv, "GET", "/products/"+str(t, latte, "id"), nil)
	mustStatus(t, http.StatusOK, code, "get latte after ship", p)
	if p["stock"].(float64) != 8 || p["reserved"].(float64) != 0 {
		t.Fatalf("after ship: stock=%v reserved=%v, want 8/0", p["stock"], p["reserved"])
	}

	code, o = call(t, srv, "POST", "/orders/"+orderID+"/deliver", nil)
	mustStatus(t, http.StatusOK, code, "deliver", o)
	if o["status"] != "DELIVERED" {
		t.Fatalf("status = %v", o["status"])
	}

	// 8) ลูกค้าต้องไม่มีออเดอร์ค้างแล้ว
	code, cus = call(t, srv, "GET", "/customers/"+str(t, cus, "id"), nil)
	mustStatus(t, http.StatusOK, code, "get customer", cus)
	if cus["open_orders"].(float64) != 0 {
		t.Fatalf("open_orders = %v, want 0", cus["open_orders"])
	}

	// 9) มีบันทึกการชำระเงิน
	code, pays := call(t, srv, "GET", "/orders/"+orderID+"/payments", nil)
	mustStatus(t, http.StatusOK, code, "payments", pays)
	list, _ := pays["payments"].([]any)
	if len(list) != 1 {
		t.Fatalf("payments = %d, want 1", len(list))
	}
}

// ─────────────────────── กฎธุรกิจต้องทะลุถึง HTTP ───────────────────────

func TestOutOfStockReturns422(t *testing.T) {
	srv := newServer(t)
	_, prod := call(t, srv, "POST", "/products", map[string]any{
		"sku": "X", "name": "ของน้อย", "price_thb": "10.00", "stock": 1,
	})
	_, cus := call(t, srv, "POST", "/customers", map[string]any{"name": "A", "email": "a@x.com"})
	_, ct := call(t, srv, "POST", "/carts", map[string]any{"customer_id": str(t, cus, "id")})
	cartID := str(t, ct, "id")
	_, _ = call(t, srv, "POST", "/carts/"+cartID+"/items",
		map[string]any{"product_id": str(t, prod, "id"), "qty": 5})

	code, body := call(t, srv, "POST", "/carts/"+cartID+"/checkout", map[string]any{"pay_now": false})
	mustStatus(t, http.StatusUnprocessableEntity, code, "checkout over stock", body)
}

func TestIllegalTransitionReturns409(t *testing.T) {
	srv := newServer(t)
	_, prod := call(t, srv, "POST", "/products", map[string]any{
		"sku": "X", "name": "ของ", "price_thb": "10.00", "stock": 5,
	})
	_, cus := call(t, srv, "POST", "/customers", map[string]any{"name": "A", "email": "a@x.com"})
	_, ct := call(t, srv, "POST", "/carts", map[string]any{"customer_id": str(t, cus, "id")})
	cartID := str(t, ct, "id")
	_, _ = call(t, srv, "POST", "/carts/"+cartID+"/items",
		map[string]any{"product_id": str(t, prod, "id"), "qty": 1})
	_, receipt := call(t, srv, "POST", "/carts/"+cartID+"/checkout", map[string]any{"pay_now": false})
	orderID := str(t, receipt, "order_id")

	// ยังไม่จ่ายเงิน แต่สั่งส่งของเลย → ต้องโดนปฏิเสธ
	code, body := call(t, srv, "POST", "/orders/"+orderID+"/ship", map[string]any{"tracking": "TH1"})
	mustStatus(t, http.StatusConflict, code, "ship before pay", body)
}

func TestPriceMismatchReturns409(t *testing.T) {
	srv := newServer(t)
	_, prod := call(t, srv, "POST", "/products", map[string]any{
		"sku": "X", "name": "ของ", "price_thb": "10.00", "stock": 5,
	})
	_, cus := call(t, srv, "POST", "/customers", map[string]any{"name": "A", "email": "a@x.com"})
	_, ct := call(t, srv, "POST", "/carts", map[string]any{"customer_id": str(t, cus, "id")})
	cartID := str(t, ct, "id")
	_, _ = call(t, srv, "POST", "/carts/"+cartID+"/items",
		map[string]any{"product_id": str(t, prod, "id"), "qty": 1})

	code, body := call(t, srv, "POST", "/carts/"+cartID+"/checkout",
		map[string]any{"expected_satang": 999, "pay_now": false})
	mustStatus(t, http.StatusConflict, code, "price mismatch", body)
}

func TestCancelPaidOrderRestoresStock(t *testing.T) {
	srv := newServer(t)
	_, prod := call(t, srv, "POST", "/products", map[string]any{
		"sku": "X", "name": "ของ", "price_thb": "50.00", "stock": 10,
	})
	_, cus := call(t, srv, "POST", "/customers", map[string]any{"name": "A", "email": "a@x.com"})
	_, ct := call(t, srv, "POST", "/carts", map[string]any{"customer_id": str(t, cus, "id")})
	cartID := str(t, ct, "id")
	_, _ = call(t, srv, "POST", "/carts/"+cartID+"/items",
		map[string]any{"product_id": str(t, prod, "id"), "qty": 4})
	_, receipt := call(t, srv, "POST", "/carts/"+cartID+"/checkout", map[string]any{"pay_now": true})

	code, o := call(t, srv, "POST", "/orders/"+str(t, receipt, "order_id")+"/cancel", nil)
	mustStatus(t, http.StatusOK, code, "cancel", o)

	code, p := call(t, srv, "GET", "/products/"+str(t, prod, "id"), nil)
	mustStatus(t, http.StatusOK, code, "product after cancel", p)
	if p["available"].(float64) != 10 || p["reserved"].(float64) != 0 {
		t.Fatalf("stock not restored: available=%v reserved=%v", p["available"], p["reserved"])
	}

	// เงินต้องถูกคืน
	code, pays := call(t, srv, "GET", "/orders/"+str(t, receipt, "order_id")+"/payments", nil)
	mustStatus(t, http.StatusOK, code, "payments", pays)
	list, _ := pays["payments"].([]any)
	first, _ := list[0].(map[string]any)
	if first["status"] != "REFUNDED" {
		t.Fatalf("payment status = %v, want REFUNDED", first["status"])
	}
}

func TestDuplicateSKUReturns409(t *testing.T) {
	srv := newServer(t)
	body := map[string]any{"sku": "DUP", "name": "ของ", "price_thb": "10.00", "stock": 1}
	_, _ = call(t, srv, "POST", "/products", body)
	code, out := call(t, srv, "POST", "/products", body)
	mustStatus(t, http.StatusConflict, code, "duplicate sku", out)
}

func TestUnknownOrderReturns404(t *testing.T) {
	srv := newServer(t)
	code, out := call(t, srv, "GET", "/orders/ghost", nil)
	mustStatus(t, http.StatusNotFound, code, "unknown order", out)
}

func TestBadJSONReturns400(t *testing.T) {
	srv := newServer(t)
	resp, err := srv.Client().Post(srv.URL+"/products", "application/json",
		bytes.NewReader([]byte(`{"sku": `)))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", resp.StatusCode)
	}
}

func TestUnknownFieldRejected(t *testing.T) {
	srv := newServer(t)
	code, out := call(t, srv, "POST", "/customers",
		map[string]any{"name": "A", "email": "a@x.com", "is_admin": true})
	mustStatus(t, http.StatusBadRequest, code, "unknown field", out)
}

func TestHealth(t *testing.T) {
	srv := newServer(t)
	code, out := call(t, srv, "GET", "/health", nil)
	mustStatus(t, http.StatusOK, code, "health", out)
	if out["status"] != "ok" {
		t.Fatalf("health = %v", out)
	}
}

// passthroughTx = TxManager สำหรับเทสที่ใช้ memory store (ไม่มี transaction จริง)
type passthroughTx struct{}

func (passthroughTx) Do(ctx context.Context, fn func(context.Context) error) error { return fn(ctx) }
