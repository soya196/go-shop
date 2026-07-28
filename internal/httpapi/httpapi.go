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
	"strings"
	"sync/atomic"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/soya196/go-shop/internal/cart"
	"github.com/soya196/go-shop/internal/catalog"
	"github.com/soya196/go-shop/internal/checkout"
	"github.com/soya196/go-shop/internal/customer"
	"github.com/soya196/go-shop/internal/money"
	"github.com/soya196/go-shop/internal/order"
	"github.com/soya196/go-shop/internal/payment"
	"github.com/soya196/go-shop/internal/token"
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
	// Tokens ใช้ตรวจ JWT · nil = ปิดการยืนยันตัวตน (dev/เดโมเท่านั้น)
	Tokens *token.Issuer
}

// API คือ handler รวมของทั้งระบบ
type API struct {
	svc    Services
	log    *slog.Logger
	cfg    Config
	tokens *token.Issuer // nil = ปิด auth
	ready  atomic.Bool
}

func New(svc Services, log *slog.Logger, cfg Config) *API {
	if cfg.Version == "" {
		cfg.Version = "dev"
	}
	a := &API{svc: svc, log: log, cfg: cfg, tokens: cfg.Tokens}
	if a.tokens == nil {
		log.Warn("⚠️ auth ปิดอยู่ — ทุก endpoint เรียกได้โดยไม่ต้องมี token (ตั้ง -jwt-secret เพื่อเปิด)")
	}
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
	handler gin.HandlerFunc

	// roles คือสิทธิ์ที่ต้องมีถึงจะเรียกได้ · nil = เปิดให้ทุกคน
	// ใช้ตัวช่วย pub/usr/adm ด้านล่างแทนการกรอกเอง จะได้อ่านตารางรู้เรื่องทันที
	roles []token.Role
}

// ตัวช่วยประกาศ route พร้อมระดับสิทธิ์ — อ่านคอลัมน์แรกแล้วรู้เลยว่าใครเรียกได้
var (
	// pub = ใครก็เรียกได้ ไม่ต้องล็อกอิน
	pub = func(m, p, s string, h gin.HandlerFunc) Route { return Route{m, p, s, h, nil} }
	// usr = ต้องล็อกอิน (ลูกค้าหรือแอดมินก็ได้)
	usr = func(m, p, s string, h gin.HandlerFunc) Route {
		return Route{m, p, s, h, []token.Role{token.RoleCustomer, token.RoleAdmin}}
	}
	// adm = เฉพาะแอดมิน (งานหลังร้าน)
	adm = func(m, p, s string, h gin.HandlerFunc) Route {
		return Route{m, p, s, h, []token.Role{token.RoleAdmin}}
	}
)

// routes คือรายการ endpoint ทั้งหมด — ความจริงชุดเดียวของทั้ง router และเอกสาร
func (a *API) routes() []Route {
	return []Route{
		// health
		pub("GET", "/healthz", "liveness — โปรเซสยังอยู่ไหม", a.healthz),
		pub("GET", "/readyz", "readiness — พร้อมรับ traffic ไหม", a.readyz),

		// catalog
		pub("GET", "/products", "ดูสินค้าที่ขายอยู่", a.listProducts),
		adm("POST", "/products", "เพิ่มสินค้า", a.createProduct),
		pub("GET", "/products/{id}", "ดูสินค้ารายตัว", a.getProduct),
		adm("POST", "/products/{id}/restock", "เติมสต็อก", a.restockProduct),
		adm("DELETE", "/products/{id}", "ปิดการขายสินค้า", a.deactivateProduct),

		// customer
		adm("GET", "/customers", "รายชื่อลูกค้า", a.listCustomers),
		pub("POST", "/customers", "สมัครลูกค้าใหม่", a.createCustomer),
		usr("GET", "/customers/{id}", "ดูลูกค้ารายตัว", a.getCustomer),
		usr("GET", "/customers/{id}/orders", "ออเดอร์ของลูกค้า", a.customerOrders),

		// cart
		usr("POST", "/carts", "เปิดตะกร้าให้ลูกค้า", a.openCart),
		usr("GET", "/carts/{id}", "ดูตะกร้า", a.getCart),
		usr("POST", "/carts/{id}/items", "หยิบสินค้าใส่ตะกร้า", a.addCartItem),
		usr("PATCH", "/carts/{id}/items/{productID}", "ปรับจำนวน (0 = เอาออก)", a.setCartQty),
		usr("DELETE", "/carts/{id}/items/{productID}", "เอาสินค้าออกจากตะกร้า", a.removeCartItem),

		// checkout
		usr("POST", "/carts/{id}/checkout", "เปลี่ยนตะกร้าเป็นออเดอร์", a.submitCheckout),

		// order
		adm("GET", "/orders", "รายการออเดอร์", a.listOrders),
		usr("GET", "/orders/{id}", "ดูออเดอร์", a.getOrder),
		usr("POST", "/orders/{id}/pay", "เก็บเงิน (PLACED → PAID)", a.payOrder),
		adm("POST", "/orders/{id}/prepare", "เริ่มจัดของ (PAID → PREPARING)", a.prepareOrder),
		adm("POST", "/orders/{id}/ship", "ส่งของ (PREPARING → SHIPPED)", a.shipOrder),
		adm("POST", "/orders/{id}/deliver", "ส่งถึงมือ (SHIPPED → DELIVERED)", a.deliverOrder),
		usr("POST", "/orders/{id}/cancel", "ยกเลิกออเดอร์", a.cancelOrder),

		// payment
		usr("GET", "/orders/{id}/payments", "ประวัติการชำระเงินของออเดอร์", a.orderPayments),
	}
}

// Routes สร้าง http.Handler พร้อมเส้นทางทั้งหมด
//
// ใช้ pattern routing ของ net/http (Go 1.22+) — "METHOD /path/{param}"
// middleware ห่อจากนอกเข้าใน: recover → requestID → log → mux
func (a *API) Routes() http.Handler {
	// ReleaseMode: ปิด debug log ของ gin (เราใช้ requestLog ของเราเอง)
	// และปิดคำเตือน "running in debug mode" ที่รกเวลารันเทส
	gin.SetMode(gin.ReleaseMode)
	r := gin.New()

	// middleware — gin เรียงตามลำดับที่ Use() ตรงๆ อ่านจากบนลงล่างได้เลย
	//
	// ⚠️ recoverer ต้องอยู่นอกสุด (ตัวแรก) เพราะต้องดัก panic ที่เกิดใน middleware ตัวอื่นด้วย
	r.Use(recoverer(a.log), requestID(), cors(a.cfg.AllowedOrigins), requestLog(a.log))

	for _, rt := range a.routes() {
		if len(rt.roles) > 0 {
			r.Handle(rt.Method, ginPath(rt.Pattern), a.requireRole(rt.roles...), rt.handler)
			continue
		}
		r.Handle(rt.Method, ginPath(rt.Pattern), rt.handler)
	}

	// เอกสาร — ไม่อยู่ในตาราง routes เพราะไม่ใช่ API ของธุรกิจ
	a.mountDocs(r)

	// legacy alias — ของเดิมใช้ /health ก่อนแยกเป็น healthz/readyz
	r.GET("/health", a.healthz)

	return r
}

// ginPath แปลง pattern แบบ stdlib เป็นแบบ gin: "/carts/{id}/items" → "/carts/:id/items"
//
// 🔑 ทำไมไม่เก็บเป็น ":id" ในตาราง routes ไปเลย:
// openapi.json ใช้รูปแบบ {id} ตามมาตรฐาน OpenAPI — ถ้าตารางเปลี่ยนไปใช้ :id
// เทสที่เทียบ "โค้ด ↔ เอกสาร" (openapi_test.go) จะเทียบกันไม่ได้อีก
// → เก็บรูปแบบมาตรฐานไว้ แล้วแปลงเฉพาะตอน register กับ router
func ginPath(pattern string) string {
	return strings.NewReplacer("{", ":", "}", "").Replace(pattern)
}

// ───────────────────────── health ─────────────────────────

// healthz = liveness · ตอบ ok ตราบใดที่โปรเซสยังตอบได้
//
// อย่าใส่การเช็ค dependency ตรงนี้ — ไม่งั้น DB ล่มชั่วคราวจะทำให้ k8s ฆ่า pod ทิ้ง
// ทั้งที่แค่รอ DB กลับมาก็พอ
func (a *API) healthz(c *gin.Context) {
	writeJSON(c, http.StatusOK, map[string]any{"status": "ok"})
}

// readyz = readiness · บอกว่าพร้อมรับ traffic ไหม
func (a *API) readyz(c *gin.Context) {
	if !a.ready.Load() {
		writeJSON(c, http.StatusServiceUnavailable, map[string]any{
			"status":  "shutting_down",
			"version": a.cfg.Version,
		})
		return
	}
	writeJSON(c, http.StatusOK, map[string]any{
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

func (a *API) listProducts(c *gin.Context) {
	var (
		list []*catalog.Product
		err  error
	)
	if c.Query("all") == "true" {
		list, err = a.svc.Catalog.ListAll(c.Request.Context())
	} else {
		list, err = a.svc.Catalog.Browse(c.Request.Context())
	}
	if err != nil {
		a.fail(c, err)
		return
	}
	out := make([]productView, 0, len(list))
	for _, p := range list {
		out = append(out, toProductView(p))
	}
	writeJSON(c, http.StatusOK, map[string]any{"products": out})
}

func (a *API) createProduct(c *gin.Context) {
	var body struct {
		SKU      string `json:"sku" binding:"required,max=64"`
		Name     string `json:"name" binding:"required,max=200"`
		PriceTHB string `json:"price_thb"`
		Satang   int64  `json:"satang" binding:"gte=0"`
		Stock    int    `json:"stock" binding:"gte=0"`
	}
	if !bind(c, &body) {
		return
	}
	price := money.FromSatang(body.Satang)
	if body.PriceTHB != "" {
		p, err := parseBaht(body.PriceTHB)
		if err != nil {
			writeErr(c, http.StatusBadRequest, "invalid price_thb")
			return
		}
		price = p
	}
	p, err := a.svc.Catalog.AddProduct(c.Request.Context(), body.SKU, body.Name, price, body.Stock)
	if err != nil {
		a.fail(c, err)
		return
	}
	writeJSON(c, http.StatusCreated, toProductView(p))
}

func (a *API) getProduct(c *gin.Context) {
	p, err := a.svc.Catalog.Get(c.Request.Context(), c.Param("id"))
	if err != nil {
		a.fail(c, err)
		return
	}
	writeJSON(c, http.StatusOK, toProductView(p))
}

func (a *API) restockProduct(c *gin.Context) {
	var body struct {
		Qty int `json:"qty" binding:"required,gt=0,lte=100000"`
	}
	if !bind(c, &body) {
		return
	}
	if err := a.svc.Catalog.Restock(c.Request.Context(), c.Param("id"), body.Qty); err != nil {
		a.fail(c, err)
		return
	}
	p, err := a.svc.Catalog.Get(c.Request.Context(), c.Param("id"))
	if err != nil {
		a.fail(c, err)
		return
	}
	writeJSON(c, http.StatusOK, toProductView(p))
}

func (a *API) deactivateProduct(c *gin.Context) {
	if err := a.svc.Catalog.Deactivate(c.Request.Context(), c.Param("id")); err != nil {
		a.fail(c, err)
		return
	}
	c.Status(http.StatusNoContent)
}

// ───────────────────────── customer ─────────────────────────

type customerView struct {
	ID         string `json:"id"`
	Name       string `json:"name"`
	Email      string `json:"email"`
	Suspended  bool   `json:"suspended"`
	OpenOrders int    `json:"open_orders"`
}

func toCustomerView(cus *customer.Customer) customerView {
	return customerView{ID: cus.ID, Name: cus.Name, Email: cus.Email, Suspended: cus.Suspended, OpenOrders: cus.OpenOrders}
}

func (a *API) listCustomers(c *gin.Context) {
	list, err := a.svc.Customers.List(c.Request.Context())
	if err != nil {
		a.fail(c, err)
		return
	}
	out := make([]customerView, 0, len(list))
	for _, cus := range list {
		out = append(out, toCustomerView(cus))
	}
	writeJSON(c, http.StatusOK, map[string]any{"customers": out})
}

func (a *API) createCustomer(c *gin.Context) {
	var body struct {
		Name  string `json:"name" binding:"required,max=200"`
		Email string `json:"email" binding:"required,email,max=320"`
	}
	if !bind(c, &body) {
		return
	}
	cus, err := a.svc.Customers.Register(c.Request.Context(), body.Name, body.Email)
	if err != nil {
		a.fail(c, err)
		return
	}
	writeJSON(c, http.StatusCreated, toCustomerView(cus))
}

func (a *API) getCustomer(c *gin.Context) {
	cus, err := a.svc.Customers.Get(c.Request.Context(), c.Param("id"))
	if err != nil {
		a.fail(c, err)
		return
	}
	writeJSON(c, http.StatusOK, toCustomerView(cus))
}

func (a *API) customerOrders(c *gin.Context) {
	list, err := a.svc.Orders.ForCustomer(c.Request.Context(), c.Param("id"))
	if err != nil {
		a.fail(c, err)
		return
	}
	out := make([]orderView, 0, len(list))
	for _, o := range list {
		out = append(out, toOrderView(o))
	}
	writeJSON(c, http.StatusOK, map[string]any{"orders": out})
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

func toCartView(crt *cart.Cart) cartView {
	lines := make([]lineView, 0, len(crt.Lines))
	for _, l := range crt.Lines {
		lines = append(lines, lineView{l.ProductID, l.Name, l.UnitPrice, l.Qty, l.Total()})
	}
	return cartView{ID: crt.ID, CustomerID: crt.CustomerID, Lines: lines, ItemCount: crt.ItemCount(), Total: crt.Total()}
}

func (a *API) openCart(c *gin.Context) {
	var body struct {
		CustomerID string `json:"customer_id" binding:"required"`
	}
	if !bind(c, &body) {
		return
	}
	crt, err := a.svc.Carts.OpenFor(c.Request.Context(), body.CustomerID)
	if err != nil {
		a.fail(c, err)
		return
	}
	writeJSON(c, http.StatusCreated, toCartView(crt))
}

func (a *API) getCart(c *gin.Context) {
	crt, err := a.svc.Carts.Get(c.Request.Context(), c.Param("id"))
	if err != nil {
		a.fail(c, err)
		return
	}
	writeJSON(c, http.StatusOK, toCartView(crt))
}

func (a *API) addCartItem(c *gin.Context) {
	var body struct {
		ProductID string `json:"product_id" binding:"required"`
		Qty       int    `json:"qty" binding:"required,gt=0,lte=999"`
	}
	if !bind(c, &body) {
		return
	}
	crt, err := a.svc.Carts.AddItem(c.Request.Context(), c.Param("id"), body.ProductID, body.Qty)
	if err != nil {
		a.fail(c, err)
		return
	}
	writeJSON(c, http.StatusOK, toCartView(crt))
}

func (a *API) setCartQty(c *gin.Context) {
	var body struct {
		Qty int `json:"qty" binding:"gte=0,lte=999"`
	}
	if !bind(c, &body) {
		return
	}
	crt, err := a.svc.Carts.SetQty(c.Request.Context(), c.Param("id"), c.Param("productID"), body.Qty)
	if err != nil {
		a.fail(c, err)
		return
	}
	writeJSON(c, http.StatusOK, toCartView(crt))
}

func (a *API) removeCartItem(c *gin.Context) {
	crt, err := a.svc.Carts.RemoveItem(c.Request.Context(), c.Param("id"), c.Param("productID"))
	if err != nil {
		a.fail(c, err)
		return
	}
	writeJSON(c, http.StatusOK, toCartView(crt))
}

// ───────────────────────── checkout ─────────────────────────

func (a *API) submitCheckout(c *gin.Context) {
	var body struct {
		ExpectedSatang int64 `json:"expected_satang"`
		PayNow         bool  `json:"pay_now"`
	}
	if c.Request.ContentLength > 0 && !bind(c, &body) {
		return
	}
	receipt, err := a.svc.Checkout.Submit(c.Request.Context(), c.Param("id"), money.FromSatang(body.ExpectedSatang), body.PayNow)
	if err != nil {
		// ออเดอร์อาจถูกสร้างแล้วแต่จ่ายไม่ผ่าน — ส่ง receipt กลับไปด้วยเพื่อให้จ่ายซ้ำได้
		if receipt != nil {
			writeJSON(c, http.StatusAccepted, map[string]any{
				"order_id": receipt.OrderID,
				"total":    receipt.Total,
				"paid":     receipt.Paid,
				"warning":  err.Error(),
			})
			return
		}
		a.fail(c, err)
		return
	}
	writeJSON(c, http.StatusCreated, map[string]any{
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

func (a *API) listOrders(c *gin.Context) {
	list, err := a.svc.Orders.List(c.Request.Context(), order.Status(c.Query("status")))
	if err != nil {
		a.fail(c, err)
		return
	}
	out := make([]orderView, 0, len(list))
	for _, o := range list {
		out = append(out, toOrderView(o))
	}
	writeJSON(c, http.StatusOK, map[string]any{"orders": out})
}

func (a *API) getOrder(c *gin.Context) {
	o, err := a.svc.Orders.Get(c.Request.Context(), c.Param("id"))
	if err != nil {
		a.fail(c, err)
		return
	}
	writeJSON(c, http.StatusOK, toOrderView(o))
}

func (a *API) payOrder(c *gin.Context) {
	a.transition(c, a.svc.Orders.Pay)
}

func (a *API) prepareOrder(c *gin.Context) {
	a.transition(c, a.svc.Orders.StartPreparing)
}

func (a *API) deliverOrder(c *gin.Context) {
	a.transition(c, a.svc.Orders.Deliver)
}

func (a *API) cancelOrder(c *gin.Context) {
	a.transition(c, a.svc.Orders.Cancel)
}

func (a *API) shipOrder(c *gin.Context) {
	var body struct {
		Tracking string `json:"tracking" binding:"required,max=64"`
	}
	if !bind(c, &body) {
		return
	}
	o, err := a.svc.Orders.Ship(c.Request.Context(), c.Param("id"), body.Tracking)
	if err != nil {
		a.fail(c, err)
		return
	}
	writeJSON(c, http.StatusOK, toOrderView(o))
}

func (a *API) orderPayments(c *gin.Context) {
	list, err := a.svc.Payments.ForOrder(c.Request.Context(), c.Param("id"))
	if err != nil {
		a.fail(c, err)
		return
	}
	out := make([]map[string]any, 0, len(list))
	for _, p := range list {
		out = append(out, map[string]any{
			"id": p.ID, "order_id": p.OrderID, "amount": p.Amount,
			"status": p.Status, "reference": p.Reference, "reason": p.Reason,
		})
	}
	writeJSON(c, http.StatusOK, map[string]any{"payments": out})
}

// transition ใช้ซ้ำสำหรับทุก endpoint ที่แค่เลื่อนสถานะ
func (a *API) transition(c *gin.Context, fn func(ctx contextT, id string) (*order.Order, error)) {
	o, err := fn(c.Request.Context(), c.Param("id"))
	if err != nil {
		a.fail(c, err)
		return
	}
	writeJSON(c, http.StatusOK, toOrderView(o))
}

// ───────────────────────── error mapping ─────────────────────────

// fail แปลง domain error → HTTP status
//
// 🔑 นี่คือจุดเดียวที่ HTTP กับ domain มาเจอกัน — domain ไม่รู้จักเลข 404/409
// ถ้าวันหนึ่งเปลี่ยนไปเป็น gRPC ก็มาเขียนตารางแปลงใหม่ที่ adapter ตัวนั้น
func (a *API) fail(c *gin.Context, err error) {
	switch {
	case errors.Is(err, catalog.ErrNotFound),
		errors.Is(err, customer.ErrNotFound),
		errors.Is(err, cart.ErrNotFound),
		errors.Is(err, order.ErrNotFound),
		errors.Is(err, payment.ErrNotFound):
		writeErr(c, http.StatusNotFound, err.Error())

	case errors.Is(err, catalog.ErrDuplicateSKU),
		errors.Is(err, customer.ErrDuplicate):
		writeErr(c, http.StatusConflict, err.Error())

	case errors.Is(err, order.ErrBadTransition),
		errors.Is(err, order.ErrCannotCancel),
		errors.Is(err, order.ErrAlreadyPaid),
		errors.Is(err, payment.ErrNotPending),
		errors.Is(err, payment.ErrNotRefundable),
		errors.Is(err, checkout.ErrMismatch):
		writeErr(c, http.StatusConflict, err.Error())

	case errors.Is(err, catalog.ErrOutOfStock),
		errors.Is(err, catalog.ErrInactive),
		errors.Is(err, cart.ErrNotSellable),
		errors.Is(err, customer.ErrSuspended),
		errors.Is(err, customer.ErrCreditTooLow),
		errors.Is(err, payment.ErrDeclined):
		writeErr(c, http.StatusUnprocessableEntity, err.Error())

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
		writeErr(c, http.StatusBadRequest, err.Error())

	default:
		a.log.Error("unhandled error", "err", err)
		writeErr(c, http.StatusInternalServerError, "internal error")
	}
}
