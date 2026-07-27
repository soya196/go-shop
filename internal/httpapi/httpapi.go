// Package httpapi เป็น driving adapter — แปลง HTTP เป็นการเรียก use case
//
// กฎของ package นี้:
//   - ห้ามมีกฎธุรกิจ · หน้าที่มีแค่ 3 อย่าง: decode → เรียก service → encode
//   - ถ้าเจอ if ที่ตัดสินเรื่องธุรกิจในนี้ แปลว่ามีของหลุดออกมาจาก domain
//   - ใช้ net/http ของ standard library ล้วน ไม่มี framework
//     (เพื่อพิสูจน์เทสข้อ 2 ที่คลาสให้ไว้: เปลี่ยน framework แล้วต้องแก้ use case ไหม)
//
// จะเปลี่ยนไปใช้ gin/echo/fiber = เขียน package นี้ใหม่ package เดียว
// internal/order, internal/catalog ฯลฯ ไม่ต้องแตะเลย
package httpapi

import (
	"errors"
	"log/slog"
	"net/http"
	"sync/atomic"
	"time"

	"github.com/soya196/go-shop/internal/cart"
	"github.com/soya196/go-shop/internal/catalog"
	"github.com/soya196/go-shop/internal/checkout"
	"github.com/soya196/go-shop/internal/customer"
	"github.com/soya196/go-shop/internal/money"
	"github.com/soya196/go-shop/internal/order"
	"github.com/soya196/go-shop/internal/payment"
)

// Services คือชุด use case ทั้งหมดที่ API ตัวนี้เปิดให้เรียก
type Services struct {
	Catalog   *catalog.Service
	Customers *customer.Service
	Carts     *cart.Service
	Orders    *order.Service
	Payments  *payment.Service
	Checkout  *checkout.Service
}

// Config คือค่าตั้งของชั้น HTTP (ไม่ใช่ของ domain)
type Config struct {
	// AllowedOrigins สำหรับ CORS · ว่าง = ปิด CORS · ["*"] = อนุญาตทุก origin
	AllowedOrigins []string
	// Version แสดงใน /readyz และ OpenAPI
	Version string
	// DocsEnabled เปิด /docs กับ /openapi.json (ควรปิดใน production ถ้า API ไม่ public)
	DocsEnabled bool
}

// API คือ handler รวมของทั้งระบบ
type API struct {
	svc   Services
	log   *slog.Logger
	cfg   Config
	ready atomic.Bool
}

func New(svc Services, log *slog.Logger, cfg Config) *API {
	if cfg.Version == "" {
		cfg.Version = "dev"
	}
	a := &API{svc: svc, log: log, cfg: cfg}
	a.ready.Store(true)
	return a
}

// SetReady ใช้ประกาศว่าพร้อม/ไม่พร้อมรับ traffic
//
// ตอน shutdown ให้เรียก SetReady(false) ก่อน แล้วรอสักครู่
// เพื่อให้ load balancer ถอดเราออกจาก pool ก่อนปิดจริง (กัน request หล่น)
func (a *API) SetReady(v bool) { a.ready.Store(v) }

// Route คือหนึ่งเส้นทางของ API
//
// ทำเป็นตารางแทนการเรียก mux.HandleFunc กระจาย เพื่อให้ "รายการ endpoint" เป็นข้อมูล
// ที่โปรแกรมอื่นอ่านได้ → เอาไปเทียบกับ openapi.json ได้ว่าเอกสารตรงกับโค้ดไหม
// (ดู openapi_test.go — spec ที่หลุดจากโค้ดคือเอกสารที่โกหก)
type Route struct {
	Method  string
	Pattern string
	Summary string
	handler http.HandlerFunc
}

// routes คือรายการ endpoint ทั้งหมด — ความจริงชุดเดียวของทั้ง router และเอกสาร
func (a *API) routes() []Route {
	return []Route{
		// health
		{"GET", "/healthz", "liveness — โปรเซสยังอยู่ไหม", a.healthz},
		{"GET", "/readyz", "readiness — พร้อมรับ traffic ไหม", a.readyz},

		// catalog
		{"GET", "/products", "ดูสินค้าที่ขายอยู่", a.listProducts},
		{"POST", "/products", "เพิ่มสินค้า", a.createProduct},
		{"GET", "/products/{id}", "ดูสินค้ารายตัว", a.getProduct},
		{"POST", "/products/{id}/restock", "เติมสต็อก", a.restockProduct},
		{"DELETE", "/products/{id}", "ปิดการขายสินค้า", a.deactivateProduct},

		// customer
		{"GET", "/customers", "รายชื่อลูกค้า", a.listCustomers},
		{"POST", "/customers", "สมัครลูกค้าใหม่", a.createCustomer},
		{"GET", "/customers/{id}", "ดูลูกค้ารายตัว", a.getCustomer},
		{"GET", "/customers/{id}/orders", "ออเดอร์ของลูกค้า", a.customerOrders},

		// cart
		{"POST", "/carts", "เปิดตะกร้าให้ลูกค้า", a.openCart},
		{"GET", "/carts/{id}", "ดูตะกร้า", a.getCart},
		{"POST", "/carts/{id}/items", "หยิบสินค้าใส่ตะกร้า", a.addCartItem},
		{"PATCH", "/carts/{id}/items/{productID}", "ปรับจำนวน (0 = เอาออก)", a.setCartQty},
		{"DELETE", "/carts/{id}/items/{productID}", "เอาสินค้าออกจากตะกร้า", a.removeCartItem},

		// checkout
		{"POST", "/carts/{id}/checkout", "เปลี่ยนตะกร้าเป็นออเดอร์", a.submitCheckout},

		// order
		{"GET", "/orders", "รายการออเดอร์", a.listOrders},
		{"GET", "/orders/{id}", "ดูออเดอร์", a.getOrder},
		{"POST", "/orders/{id}/pay", "เก็บเงิน (PLACED → PAID)", a.payOrder},
		{"POST", "/orders/{id}/prepare", "เริ่มจัดของ (PAID → PREPARING)", a.prepareOrder},
		{"POST", "/orders/{id}/ship", "ส่งของ (PREPARING → SHIPPED)", a.shipOrder},
		{"POST", "/orders/{id}/deliver", "ส่งถึงมือ (SHIPPED → DELIVERED)", a.deliverOrder},
		{"POST", "/orders/{id}/cancel", "ยกเลิกออเดอร์", a.cancelOrder},

		// payment
		{"GET", "/orders/{id}/payments", "ประวัติการชำระเงินของออเดอร์", a.orderPayments},
	}
}

// Routes สร้าง http.Handler พร้อมเส้นทางทั้งหมด
//
// ใช้ pattern routing ของ net/http (Go 1.22+) — "METHOD /path/{param}"
// middleware ห่อจากนอกเข้าใน: recover → requestID → log → mux
func (a *API) Routes() http.Handler {
	mux := http.NewServeMux()

	for _, r := range a.routes() {
		mux.HandleFunc(r.Method+" "+r.Pattern, r.handler)
	}

	// เอกสาร — ไม่อยู่ในตาราง routes เพราะไม่ใช่ API ของธุรกิจ
	a.mountDocs(mux)

	// legacy alias — ของเดิมใช้ /health ก่อนแยกเป็น healthz/readyz
	mux.HandleFunc("GET /health", a.healthz)

	var h http.Handler = mux
	h = requestLog(a.log)(h)
	h = cors(a.cfg.AllowedOrigins)(h)
	h = requestID(h)
	h = recoverer(a.log)(h)
	return h
}

// ───────────────────────── health ─────────────────────────

// healthz = liveness · ตอบ ok ตราบใดที่โปรเซสยังตอบได้
//
// อย่าใส่การเช็ค dependency ตรงนี้ — ไม่งั้น DB ล่มชั่วคราวจะทำให้ k8s ฆ่า pod ทิ้ง
// ทั้งที่แค่รอ DB กลับมาก็พอ
func (a *API) healthz(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{"status": "ok"})
}

// readyz = readiness · บอกว่าพร้อมรับ traffic ไหม
func (a *API) readyz(w http.ResponseWriter, _ *http.Request) {
	if !a.ready.Load() {
		writeJSON(w, http.StatusServiceUnavailable, map[string]any{
			"status":  "shutting_down",
			"version": a.cfg.Version,
		})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"status":  "ready",
		"version": a.cfg.Version,
	})
}

// ───────────────────────── catalog ─────────────────────────

type productView struct {
	ID        string       `json:"id"`
	SKU       string       `json:"sku"`
	Name      string       `json:"name"`
	Price     money.Satang `json:"price"`
	Stock     int          `json:"stock"`
	Reserved  int          `json:"reserved"`
	Available int          `json:"available"`
	Active    bool         `json:"active"`
}

func toProductView(p *catalog.Product) productView {
	return productView{
		ID: p.ID, SKU: p.SKU, Name: p.Name, Price: p.Price,
		Stock: p.Stock, Reserved: p.Reserved, Available: p.Available(), Active: p.Active,
	}
}

func (a *API) listProducts(w http.ResponseWriter, r *http.Request) {
	var (
		list []*catalog.Product
		err  error
	)
	if r.URL.Query().Get("all") == "true" {
		list, err = a.svc.Catalog.ListAll(r.Context())
	} else {
		list, err = a.svc.Catalog.Browse(r.Context())
	}
	if err != nil {
		a.fail(w, err)
		return
	}
	out := make([]productView, 0, len(list))
	for _, p := range list {
		out = append(out, toProductView(p))
	}
	writeJSON(w, http.StatusOK, map[string]any{"products": out})
}

func (a *API) createProduct(w http.ResponseWriter, r *http.Request) {
	var body struct {
		SKU      string `json:"sku"`
		Name     string `json:"name"`
		PriceTHB string `json:"price_thb"`
		Satang   int64  `json:"satang"`
		Stock    int    `json:"stock"`
	}
	if !decode(w, r, &body) {
		return
	}
	price := money.FromSatang(body.Satang)
	if body.PriceTHB != "" {
		p, err := parseBaht(body.PriceTHB)
		if err != nil {
			writeErr(w, http.StatusBadRequest, "invalid price_thb")
			return
		}
		price = p
	}
	p, err := a.svc.Catalog.AddProduct(r.Context(), body.SKU, body.Name, price, body.Stock)
	if err != nil {
		a.fail(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, toProductView(p))
}

func (a *API) getProduct(w http.ResponseWriter, r *http.Request) {
	p, err := a.svc.Catalog.Get(r.Context(), r.PathValue("id"))
	if err != nil {
		a.fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, toProductView(p))
}

func (a *API) restockProduct(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Qty int `json:"qty"`
	}
	if !decode(w, r, &body) {
		return
	}
	if err := a.svc.Catalog.Restock(r.Context(), r.PathValue("id"), body.Qty); err != nil {
		a.fail(w, err)
		return
	}
	p, err := a.svc.Catalog.Get(r.Context(), r.PathValue("id"))
	if err != nil {
		a.fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, toProductView(p))
}

func (a *API) deactivateProduct(w http.ResponseWriter, r *http.Request) {
	if err := a.svc.Catalog.Deactivate(r.Context(), r.PathValue("id")); err != nil {
		a.fail(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// ───────────────────────── customer ─────────────────────────

type customerView struct {
	ID         string `json:"id"`
	Name       string `json:"name"`
	Email      string `json:"email"`
	Suspended  bool   `json:"suspended"`
	OpenOrders int    `json:"open_orders"`
}

func toCustomerView(c *customer.Customer) customerView {
	return customerView{ID: c.ID, Name: c.Name, Email: c.Email, Suspended: c.Suspended, OpenOrders: c.OpenOrders}
}

func (a *API) listCustomers(w http.ResponseWriter, r *http.Request) {
	list, err := a.svc.Customers.List(r.Context())
	if err != nil {
		a.fail(w, err)
		return
	}
	out := make([]customerView, 0, len(list))
	for _, c := range list {
		out = append(out, toCustomerView(c))
	}
	writeJSON(w, http.StatusOK, map[string]any{"customers": out})
}

func (a *API) createCustomer(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Name  string `json:"name"`
		Email string `json:"email"`
	}
	if !decode(w, r, &body) {
		return
	}
	c, err := a.svc.Customers.Register(r.Context(), body.Name, body.Email)
	if err != nil {
		a.fail(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, toCustomerView(c))
}

func (a *API) getCustomer(w http.ResponseWriter, r *http.Request) {
	c, err := a.svc.Customers.Get(r.Context(), r.PathValue("id"))
	if err != nil {
		a.fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, toCustomerView(c))
}

func (a *API) customerOrders(w http.ResponseWriter, r *http.Request) {
	list, err := a.svc.Orders.ForCustomer(r.Context(), r.PathValue("id"))
	if err != nil {
		a.fail(w, err)
		return
	}
	out := make([]orderView, 0, len(list))
	for _, o := range list {
		out = append(out, toOrderView(o))
	}
	writeJSON(w, http.StatusOK, map[string]any{"orders": out})
}

// ───────────────────────── cart ─────────────────────────

type cartView struct {
	ID         string       `json:"id"`
	CustomerID string       `json:"customer_id"`
	Lines      []lineView   `json:"lines"`
	ItemCount  int          `json:"item_count"`
	Total      money.Satang `json:"total"`
}

type lineView struct {
	ProductID string       `json:"product_id"`
	Name      string       `json:"name"`
	UnitPrice money.Satang `json:"unit_price"`
	Qty       int          `json:"qty"`
	Total     money.Satang `json:"total"`
}

func toCartView(c *cart.Cart) cartView {
	lines := make([]lineView, 0, len(c.Lines))
	for _, l := range c.Lines {
		lines = append(lines, lineView{l.ProductID, l.Name, l.UnitPrice, l.Qty, l.Total()})
	}
	return cartView{ID: c.ID, CustomerID: c.CustomerID, Lines: lines, ItemCount: c.ItemCount(), Total: c.Total()}
}

func (a *API) openCart(w http.ResponseWriter, r *http.Request) {
	var body struct {
		CustomerID string `json:"customer_id"`
	}
	if !decode(w, r, &body) {
		return
	}
	c, err := a.svc.Carts.OpenFor(r.Context(), body.CustomerID)
	if err != nil {
		a.fail(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, toCartView(c))
}

func (a *API) getCart(w http.ResponseWriter, r *http.Request) {
	c, err := a.svc.Carts.Get(r.Context(), r.PathValue("id"))
	if err != nil {
		a.fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, toCartView(c))
}

func (a *API) addCartItem(w http.ResponseWriter, r *http.Request) {
	var body struct {
		ProductID string `json:"product_id"`
		Qty       int    `json:"qty"`
	}
	if !decode(w, r, &body) {
		return
	}
	c, err := a.svc.Carts.AddItem(r.Context(), r.PathValue("id"), body.ProductID, body.Qty)
	if err != nil {
		a.fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, toCartView(c))
}

func (a *API) setCartQty(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Qty int `json:"qty"`
	}
	if !decode(w, r, &body) {
		return
	}
	c, err := a.svc.Carts.SetQty(r.Context(), r.PathValue("id"), r.PathValue("productID"), body.Qty)
	if err != nil {
		a.fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, toCartView(c))
}

func (a *API) removeCartItem(w http.ResponseWriter, r *http.Request) {
	c, err := a.svc.Carts.RemoveItem(r.Context(), r.PathValue("id"), r.PathValue("productID"))
	if err != nil {
		a.fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, toCartView(c))
}

// ───────────────────────── checkout ─────────────────────────

func (a *API) submitCheckout(w http.ResponseWriter, r *http.Request) {
	var body struct {
		ExpectedSatang int64 `json:"expected_satang"`
		PayNow         bool  `json:"pay_now"`
	}
	if r.ContentLength > 0 && !decode(w, r, &body) {
		return
	}
	receipt, err := a.svc.Checkout.Submit(r.Context(), r.PathValue("id"), money.FromSatang(body.ExpectedSatang), body.PayNow)
	if err != nil {
		// ออเดอร์อาจถูกสร้างแล้วแต่จ่ายไม่ผ่าน — ส่ง receipt กลับไปด้วยเพื่อให้จ่ายซ้ำได้
		if receipt != nil {
			writeJSON(w, http.StatusAccepted, map[string]any{
				"order_id": receipt.OrderID,
				"total":    receipt.Total,
				"paid":     receipt.Paid,
				"warning":  err.Error(),
			})
			return
		}
		a.fail(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{
		"order_id": receipt.OrderID,
		"total":    receipt.Total,
		"paid":     receipt.Paid,
	})
}

// ───────────────────────── order ─────────────────────────

type orderView struct {
	ID         string       `json:"id"`
	CustomerID string       `json:"customer_id"`
	Status     order.Status `json:"status"`
	Lines      []lineView   `json:"lines"`
	ItemCount  int          `json:"item_count"`
	Total      money.Satang `json:"total"`
	Tracking   string       `json:"tracking,omitempty"`
	PaymentID  string       `json:"payment_id,omitempty"`
	CreatedAt  time.Time    `json:"created_at"`
	UpdatedAt  time.Time    `json:"updated_at"`
}

func toOrderView(o *order.Order) orderView {
	lines := make([]lineView, 0, len(o.Lines))
	for _, l := range o.Lines {
		lines = append(lines, lineView{l.ProductID, l.Name, l.UnitPrice, l.Qty, l.Total()})
	}
	return orderView{
		ID: o.ID, CustomerID: o.CustomerID, Status: o.Status, Lines: lines,
		ItemCount: o.ItemCount(), Total: o.Total(), Tracking: o.Tracking,
		PaymentID: o.PaymentID, CreatedAt: o.CreatedAt, UpdatedAt: o.UpdatedAt,
	}
}

func (a *API) listOrders(w http.ResponseWriter, r *http.Request) {
	list, err := a.svc.Orders.List(r.Context(), order.Status(r.URL.Query().Get("status")))
	if err != nil {
		a.fail(w, err)
		return
	}
	out := make([]orderView, 0, len(list))
	for _, o := range list {
		out = append(out, toOrderView(o))
	}
	writeJSON(w, http.StatusOK, map[string]any{"orders": out})
}

func (a *API) getOrder(w http.ResponseWriter, r *http.Request) {
	o, err := a.svc.Orders.Get(r.Context(), r.PathValue("id"))
	if err != nil {
		a.fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, toOrderView(o))
}

func (a *API) payOrder(w http.ResponseWriter, r *http.Request) {
	a.transition(w, r, a.svc.Orders.Pay)
}

func (a *API) prepareOrder(w http.ResponseWriter, r *http.Request) {
	a.transition(w, r, a.svc.Orders.StartPreparing)
}

func (a *API) deliverOrder(w http.ResponseWriter, r *http.Request) {
	a.transition(w, r, a.svc.Orders.Deliver)
}

func (a *API) cancelOrder(w http.ResponseWriter, r *http.Request) {
	a.transition(w, r, a.svc.Orders.Cancel)
}

func (a *API) shipOrder(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Tracking string `json:"tracking"`
	}
	if !decode(w, r, &body) {
		return
	}
	o, err := a.svc.Orders.Ship(r.Context(), r.PathValue("id"), body.Tracking)
	if err != nil {
		a.fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, toOrderView(o))
}

func (a *API) orderPayments(w http.ResponseWriter, r *http.Request) {
	list, err := a.svc.Payments.ForOrder(r.Context(), r.PathValue("id"))
	if err != nil {
		a.fail(w, err)
		return
	}
	out := make([]map[string]any, 0, len(list))
	for _, p := range list {
		out = append(out, map[string]any{
			"id": p.ID, "order_id": p.OrderID, "amount": p.Amount,
			"status": p.Status, "reference": p.Reference, "reason": p.Reason,
		})
	}
	writeJSON(w, http.StatusOK, map[string]any{"payments": out})
}

// transition ใช้ซ้ำสำหรับทุก endpoint ที่แค่เลื่อนสถานะ
func (a *API) transition(w http.ResponseWriter, r *http.Request, fn func(ctx contextT, id string) (*order.Order, error)) {
	o, err := fn(r.Context(), r.PathValue("id"))
	if err != nil {
		a.fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, toOrderView(o))
}

// ───────────────────────── error mapping ─────────────────────────

// fail แปลง domain error → HTTP status
//
// 🔑 นี่คือจุดเดียวที่ HTTP กับ domain มาเจอกัน — domain ไม่รู้จักเลข 404/409
// ถ้าวันหนึ่งเปลี่ยนไปเป็น gRPC ก็มาเขียนตารางแปลงใหม่ที่ adapter ตัวนั้น
func (a *API) fail(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, catalog.ErrNotFound),
		errors.Is(err, customer.ErrNotFound),
		errors.Is(err, cart.ErrNotFound),
		errors.Is(err, order.ErrNotFound),
		errors.Is(err, payment.ErrNotFound):
		writeErr(w, http.StatusNotFound, err.Error())

	case errors.Is(err, catalog.ErrDuplicateSKU),
		errors.Is(err, customer.ErrDuplicate):
		writeErr(w, http.StatusConflict, err.Error())

	case errors.Is(err, order.ErrBadTransition),
		errors.Is(err, order.ErrCannotCancel),
		errors.Is(err, order.ErrAlreadyPaid),
		errors.Is(err, payment.ErrNotPending),
		errors.Is(err, payment.ErrNotRefundable),
		errors.Is(err, checkout.ErrMismatch):
		writeErr(w, http.StatusConflict, err.Error())

	case errors.Is(err, catalog.ErrOutOfStock),
		errors.Is(err, catalog.ErrInactive),
		errors.Is(err, cart.ErrNotSellable),
		errors.Is(err, customer.ErrSuspended),
		errors.Is(err, customer.ErrCreditTooLow),
		errors.Is(err, payment.ErrDeclined):
		writeErr(w, http.StatusUnprocessableEntity, err.Error())

	case errors.Is(err, catalog.ErrInvalidName),
		errors.Is(err, catalog.ErrInvalidPrice),
		errors.Is(err, catalog.ErrInvalidQty),
		errors.Is(err, catalog.ErrReleaseTooMuch),
		errors.Is(err, customer.ErrInvalidName),
		errors.Is(err, customer.ErrInvalidEmail),
		errors.Is(err, cart.ErrInvalidQty),
		errors.Is(err, cart.ErrLineNotFound),
		errors.Is(err, cart.ErrTooManyLines),
		errors.Is(err, order.ErrNoLines),
		errors.Is(err, order.ErrInvalidQty),
		errors.Is(err, order.ErrInvalidCustomer),
		errors.Is(err, payment.ErrInvalidAmount),
		errors.Is(err, checkout.ErrEmptyCart),
		errors.Is(err, money.ErrNegative):
		writeErr(w, http.StatusBadRequest, err.Error())

	default:
		a.log.Error("unhandled error", "err", err)
		writeErr(w, http.StatusInternalServerError, "internal error")
	}
}
