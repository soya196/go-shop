// Command api คือ composition root ของระบบ
//
// 🔑 ไฟล์นี้คือ **จุดเดียวในโปรเจกต์ที่รู้จักทุกอย่าง** — domain, adapter, bridge
// ทุก package อื่นรู้จักแค่ interface ที่ตัวเองประกาศ
//
// Go ไม่มี annotation แบบ Spring (@Service, @Autowired) และเราไม่พยายามเลียนแบบมัน
// DI ที่นี่คือ "เรียก constructor แล้วส่งของให้กัน" — อ่านจากบนลงล่างแล้วเห็นทั้งระบบ
// ไม่มี reflection ไม่มี magic ไม่มี container ที่ต้อง debug ตอนตี 2
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/soya196/go-shop/internal/bridge"
	"github.com/soya196/go-shop/internal/cart"
	"github.com/soya196/go-shop/internal/catalog"
	"github.com/soya196/go-shop/internal/checkout"
	"github.com/soya196/go-shop/internal/clock"
	"github.com/soya196/go-shop/internal/customer"
	"github.com/soya196/go-shop/internal/fakepay"
	"github.com/soya196/go-shop/internal/httpapi"
	"github.com/soya196/go-shop/internal/jsonstore"
	"github.com/soya196/go-shop/internal/memory"
	"github.com/soya196/go-shop/internal/money"
	"github.com/soya196/go-shop/internal/order"
	"github.com/soya196/go-shop/internal/payment"
	"github.com/soya196/go-shop/internal/uid"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "fatal:", err)
		os.Exit(1)
	}
}

// env อ่านค่าจาก environment variable ถ้าไม่มีก็ใช้ค่า default
//
// ลำดับความสำคัญ: flag > env > default
// (12-factor: container ตั้งค่าผ่าน env · dev สั่ง flag ทับได้)
func env(key, def string) string {
	if v, ok := os.LookupEnv(key); ok && v != "" {
		return v
	}
	return def
}

func envBool(key string, def bool) bool {
	if v, ok := os.LookupEnv(key); ok {
		b, err := strconv.ParseBool(v)
		if err == nil {
			return b
		}
	}
	return def
}

// version ถูกใส่ตอน build: go build -ldflags "-X main.version=$(git rev-parse --short HEAD)"
var version = "dev"

func run() error {
	var (
		addr     = flag.String("addr", env("SHOP_ADDR", ":8080"), "ที่อยู่ที่จะ listen [SHOP_ADDR]")
		store    = flag.String("store", env("SHOP_STORE", "memory"), "ที่เก็บข้อมูล: memory | json [SHOP_STORE]")
		dataDir  = flag.String("data", env("SHOP_DATA_DIR", "./data"), "โฟลเดอร์ข้อมูล (ใช้เมื่อ -store=json) [SHOP_DATA_DIR]")
		seed     = flag.Bool("seed", envBool("SHOP_SEED", true), "ใส่ข้อมูลตัวอย่างตอนเปิด [SHOP_SEED]")
		maxCharg = flag.Int64("decline-over", 0, "ให้ gateway ปฏิเสธยอดที่เกินกี่สตางค์ (0 = อนุมัติหมด)")
		logFmt   = flag.String("log-format", env("SHOP_LOG_FORMAT", "text"), "รูปแบบ log: text | json [SHOP_LOG_FORMAT]")
		logLevel = flag.String("log-level", env("SHOP_LOG_LEVEL", "info"), "ระดับ log: debug | info | warn | error [SHOP_LOG_LEVEL]")
		docs     = flag.Bool("docs", envBool("SHOP_DOCS", true), "เปิด /docs + /openapi.json [SHOP_DOCS]")
		origins  = flag.String("cors-origins", env("SHOP_CORS_ORIGINS", ""), "CORS origins คั่นด้วย comma · '*' = เปิดหมด [SHOP_CORS_ORIGINS]")
		drain    = flag.Duration("drain", 3*time.Second, "รอให้ load balancer ถอดเราออกกี่วินาทีก่อนปิดจริง")
	)
	flag.Parse()

	log, err := newLogger(*logFmt, *logLevel)
	if err != nil {
		return err
	}
	log.Info("starting", "version", version, "store", *store, "docs", *docs)

	// ── 1) เลือก driven adapter ───────────────────────────────────
	// 🔑 นี่คือ "เทสข้อ 1" ของคลาส: เปลี่ยนที่เก็บข้อมูลทั้งระบบตรงนี้จุดเดียว
	//    โดย internal/catalog, internal/order ฯลฯ ไม่ถูกแตะเลย
	repos, err := openRepos(*store, *dataDir)
	if err != nil {
		return err
	}
	log.Info("storage ready", "kind", *store)

	// ── 2) adapter สนับสนุน ───────────────────────────────────────
	wallClock := clock.System{}
	gateway := fakepay.New()
	gateway.DeclineOver = money.FromSatang(*maxCharg)

	// ── 3) ประกอบ domain service — ล่างขึ้นบนตามลำดับการพึ่งพา ────
	catalogSvc := catalog.NewService(repos.products, uid.Random{Prefix: "prd"})
	customerSvc := customer.NewService(repos.customers, uid.Random{Prefix: "cus"})
	paymentSvc := payment.NewService(repos.payments, gateway, uid.Random{Prefix: "pay"}, wallClock)

	cartSvc := cart.NewService(
		repos.carts,
		bridge.CartCatalog{Catalog: catalogSvc}, // cart.Catalog ← catalog
		uid.Random{Prefix: "crt"},
	)

	orderSvc := order.NewService(
		repos.orders,
		bridge.OrderStock{Catalog: catalogSvc},       // order.Stock    ← catalog
		bridge.OrderShoppers{Customers: customerSvc}, // order.Shoppers ← customer
		bridge.OrderWallet{Payments: paymentSvc},     // order.Wallet   ← payment
		uid.Random{Prefix: "ord"},
		wallClock,
	)

	checkoutSvc := checkout.NewService(
		bridge.CheckoutBaskets{Carts: cartSvc},  // checkout.Baskets ← cart
		bridge.CheckoutOrders{Orders: orderSvc}, // checkout.Orders  ← order
	)

	if *seed {
		if err := seedData(context.Background(), catalogSvc, customerSvc, log); err != nil {
			return fmt.Errorf("seed: %w", err)
		}
	}

	// ── 4) driving adapter ────────────────────────────────────────
	api := httpapi.New(httpapi.Services{
		Catalog:   catalogSvc,
		Customers: customerSvc,
		Carts:     cartSvc,
		Orders:    orderSvc,
		Payments:  paymentSvc,
		Checkout:  checkoutSvc,
	}, log, httpapi.Config{
		Version:        version,
		DocsEnabled:    *docs,
		AllowedOrigins: splitCSV(*origins),
	})

	srv := &http.Server{
		Addr:              *addr,
		Handler:           api.Routes(),
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       15 * time.Second,
		WriteTimeout:      15 * time.Second,
		IdleTimeout:       60 * time.Second,
		ErrorLog:          slog.NewLogLogger(log.Handler(), slog.LevelWarn),
	}

	// ── 5) graceful shutdown ──────────────────────────────────────
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	errCh := make(chan error, 1)
	go func() {
		log.Info("listening", "addr", *addr, "docs", "http://localhost"+*addr+"/docs")
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			errCh <- err
		}
	}()

	select {
	case err := <-errCh:
		return err
	case <-ctx.Done():
		// ⚠️ ลำดับนี้สำคัญ: ประกาศ not-ready ก่อน แล้วรอให้ load balancer ถอดเราออกจาก pool
		// ถ้าปิดเลยทันที request ที่ LB เพิ่งส่งมาจะหล่นกลางทาง
		log.Info("draining — /readyz จะตอบ 503", "for", drain.String())
		api.SetReady(false)
		time.Sleep(*drain)

		log.Info("shutting down")
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if err := srv.Shutdown(shutdownCtx); err != nil {
			return err
		}
		log.Info("bye")
		return nil
	}
}

// newLogger สร้าง slog handler ตาม format/level ที่สั่ง
//
// json สำหรับ production (ให้ log aggregator แกะได้) · text สำหรับอ่านด้วยตาตอน dev
func newLogger(format, level string) (*slog.Logger, error) {
	var lv slog.Level
	switch strings.ToLower(level) {
	case "debug":
		lv = slog.LevelDebug
	case "info":
		lv = slog.LevelInfo
	case "warn", "warning":
		lv = slog.LevelWarn
	case "error":
		lv = slog.LevelError
	default:
		return nil, fmt.Errorf("unknown -log-level=%q (debug|info|warn|error)", level)
	}

	opts := &slog.HandlerOptions{Level: lv}
	switch strings.ToLower(format) {
	case "json":
		return slog.New(slog.NewJSONHandler(os.Stdout, opts)), nil
	case "text":
		return slog.New(slog.NewTextHandler(os.Stdout, opts)), nil
	default:
		return nil, fmt.Errorf("unknown -log-format=%q (text|json)", format)
	}
}

// splitCSV แยกสตริงคั่น comma แล้วตัดช่องว่าง — คืน nil ถ้าว่าง
func splitCSV(s string) []string {
	if strings.TrimSpace(s) == "" {
		return nil
	}
	parts := strings.Split(s, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}

// repoSet รวม repository ทุกตัว — ทำให้ openRepos คืนของชุดเดียวจบ
type repoSet struct {
	products  catalog.Repository
	customers customer.Repository
	carts     cart.Repository
	orders    order.Repository
	payments  payment.Repository
}

// openRepos เลือก adapter ตาม flag
//
// สังเกตว่า return type เป็น **interface ของ domain** ไม่ใช่ struct ของ adapter
// → ที่เหลือของโปรแกรมไม่รู้เลยว่าข้างในเป็น memory หรือ json
func openRepos(kind, dir string) (*repoSet, error) {
	switch kind {
	case "memory":
		return &repoSet{
			products:  memory.NewProducts(),
			customers: memory.NewCustomers(),
			carts:     memory.NewCarts(),
			orders:    memory.NewOrders(),
			payments:  memory.NewPayments(),
		}, nil
	case "json":
		s, err := jsonstore.Open(dir)
		if err != nil {
			return nil, err
		}
		return &repoSet{
			products:  s.Products,
			customers: s.Customers,
			carts:     s.Carts,
			orders:    s.Orders,
			payments:  s.Payments,
		}, nil
	default:
		return nil, fmt.Errorf("unknown -store=%q (ใช้ memory หรือ json)", kind)
	}
}

// seedData ใส่ข้อมูลตัวอย่างให้ลองยิง API ได้ทันที
func seedData(ctx context.Context, cat *catalog.Service, cus *customer.Service, log *slog.Logger) error {
	products := []struct {
		sku, name string
		baht      float64
		stock     int
	}{
		{"COF-LAT", "Latte เย็น", 85, 40},
		{"COF-MOC", "Mocha ร้อน", 95, 25},
		{"COF-AME", "Americano", 65, 60},
		{"BAK-CRO", "ครัวซองต์เนยสด", 79.50, 12},
		{"BAK-CAK", "เค้กช็อกโกแลต", 129, 8},
	}
	added := 0
	for _, p := range products {
		if _, err := cat.AddProduct(ctx, p.sku, p.name, money.FromBaht(p.baht), p.stock); err != nil {
			if errors.Is(err, catalog.ErrDuplicateSKU) {
				continue // seed ซ้ำได้ ไม่พัง
			}
			return err
		}
		added++
	}

	customers := []struct{ name, email string }{
		{"สนธยา", "sonthaya@example.com"},
		{"ดองกี้", "dongky@example.com"},
	}
	for _, c := range customers {
		if _, err := cus.Register(ctx, c.name, c.email); err != nil {
			if errors.Is(err, customer.ErrDuplicate) {
				continue
			}
			return err
		}
	}
	log.Info("seed complete", "new_products", added)
	return nil
}
