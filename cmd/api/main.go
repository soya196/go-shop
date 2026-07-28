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
	"github.com/soya196/go-shop/internal/pgstore"
	"github.com/soya196/go-shop/internal/token"
	"github.com/soya196/go-shop/internal/tracing"
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
		addr      = flag.String("addr", env("SHOP_ADDR", ":8080"), "ที่อยู่ที่จะ listen [SHOP_ADDR]")
		store     = flag.String("store", env("SHOP_STORE", "memory"), "ที่เก็บข้อมูล: memory | json | postgres [SHOP_STORE]")
		dsn       = flag.String("dsn", env("DATABASE_URL", ""), "PostgreSQL DSN (ใช้เมื่อ -store=postgres) [DATABASE_URL]")
		dataDir   = flag.String("data", env("SHOP_DATA_DIR", "./data"), "โฟลเดอร์ข้อมูล (ใช้เมื่อ -store=json) [SHOP_DATA_DIR]")
		seed      = flag.Bool("seed", envBool("SHOP_SEED", true), "ใส่ข้อมูลตัวอย่างตอนเปิด [SHOP_SEED]")
		maxCharg  = flag.Int64("decline-over", 0, "ให้ gateway ปฏิเสธยอดที่เกินกี่สตางค์ (0 = อนุมัติหมด)")
		logFmt    = flag.String("log-format", env("SHOP_LOG_FORMAT", "text"), "รูปแบบ log: text | json [SHOP_LOG_FORMAT]")
		logLevel  = flag.String("log-level", env("SHOP_LOG_LEVEL", "info"), "ระดับ log: debug | info | warn | error [SHOP_LOG_LEVEL]")
		docs      = flag.Bool("docs", envBool("SHOP_DOCS", true), "เปิด /docs + /openapi.json [SHOP_DOCS]")
		origins   = flag.String("cors-origins", env("SHOP_CORS_ORIGINS", ""), "CORS origins คั่นด้วย comma · '*' = เปิดหมด [SHOP_CORS_ORIGINS]")
		drain     = flag.Duration("drain", 3*time.Second, "รอให้ load balancer ถอดเราออกกี่วินาทีก่อนปิดจริง")
		jwtSecret = flag.String("jwt-secret", env("JWT_SECRET", ""), "เปิดการยืนยันตัวตนด้วย JWT · ว่าง = ปิด auth [JWT_SECRET]")
		jwtTTL    = flag.Duration("jwt-ttl", 1*time.Hour, "อายุของ token ที่ยอมรับ")
		traceExp  = flag.String("trace", env("OTEL_EXPORTER", "none"), "tracing: none | stdout | otlp [OTEL_EXPORTER]")
		traceEP   = flag.String("trace-endpoint", env("OTEL_EXPORTER_OTLP_ENDPOINT", "localhost:4318"), "ที่อยู่ OTLP collector [OTEL_EXPORTER_OTLP_ENDPOINT]")
		traceRate = flag.Float64("trace-sample", 1.0, "สัดส่วนที่จะเก็บ trace 0.0-1.0 (prod traffic สูงควรต่ำกว่า 1)")
	)
	flag.Parse()

	log, err := newLogger(*logFmt, *logLevel)
	if err != nil {
		return err
	}
	log.Info("starting", "version", version, "store", *store, "docs", *docs)

	// ── 0) tracing — ตั้งก่อนอย่างอื่น เพื่อให้ span ครอบตั้งแต่ต้น ────
	stopTracing, err := startTracing(*traceExp, *traceEP, *traceRate, log)
	if err != nil {
		return err
	}
	defer stopTracing()

	// context สำหรับขั้นตอน startup เท่านั้น (ต่อ DB, seed)
	// แยกจาก ctx ของ signal ด้านล่าง — ถ้าต่อ DB ไม่ติดใน 15 วิ ให้ยอมแพ้แล้วตายไปเลย
	// ดีกว่าค้างรอจน k8s ฆ่าทิ้งโดยไม่มี log บอกสาเหตุ
	startCtx, cancelStart := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancelStart()

	// ── 1) เลือก driven adapter ───────────────────────────────────
	// 🔑 นี่คือ "เทสข้อ 1" ของคลาส: เปลี่ยนที่เก็บข้อมูลทั้งระบบตรงนี้จุดเดียว
	//    โดย internal/catalog, internal/order ฯลฯ ไม่ถูกแตะเลย
	repos, err := openRepos(startCtx, *store, *dataDir, *dsn, *traceExp != "none")
	if err != nil {
		return err
	}
	if repos.close != nil {
		defer repos.close()
	}
	log.Info("storage ready", "kind", *store)

	// ── 2-3) adapter สนับสนุน + ประกอบ domain service ─────────────
	svc := buildServices(repos, money.FromSatang(*maxCharg))

	if *seed {
		if err := seedData(startCtx, svc.Catalog, svc.Customers, log); err != nil {
			return fmt.Errorf("seed: %w", err)
		}
	}

	// ── 4) driving adapter ────────────────────────────────────────
	//
	// ⚠️ ไม่ตั้ง -jwt-secret = auth ปิด (สะดวกตอน dev/เดโม)
	//    httpapi.New จะ log เตือนดังๆ ให้เอง — ห้ามขึ้น production แบบนี้
	var issuer *token.Issuer
	if *jwtSecret != "" {
		issuer, err = token.New(*jwtSecret, *jwtTTL)
		if err != nil {
			return err
		}
		log.Info("auth เปิดอยู่", "token_ttl", jwtTTL.String())
	}

	api := httpapi.New(svc, log, httpapi.Config{
		Version:        version,
		DocsEnabled:    *docs,
		AllowedOrigins: splitCSV(*origins),
		Tokens:         issuer,
		TracingEnabled: *traceExp != "none",
		ServiceName:    "go-shop",
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

// startTracing ตั้งค่า OpenTelemetry แล้วคืนฟังก์ชันปิดที่เรียกได้เลย
//
// รวม flush timeout ไว้ในตัว — ผู้เรียกแค่ defer ไม่ต้องจัดการ context เอง
func startTracing(exporter, endpoint string, sample float64, log *slog.Logger) (func(), error) {
	shutdown, err := tracing.Setup(context.Background(), tracing.Config{
		Exporter:    exporter,
		Endpoint:    endpoint,
		ServiceName: "go-shop",
		Version:     version,
		SampleRatio: sample,
	})
	if err != nil {
		return func() {}, fmt.Errorf("tracing: %w", err)
	}
	if exporter != "none" {
		log.Info("tracing เปิดอยู่", "exporter", exporter, "sample", sample)
	}
	return func() {
		// ให้เวลา flush span ที่ค้างอยู่ก่อนโปรเซสตาย ไม่งั้น trace ท่อนสุดท้ายหาย
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := shutdown(ctx); err != nil {
			log.Warn("ปิด tracing ไม่เรียบร้อย", "err", err)
		}
	}, nil
}

// buildServices ประกอบ domain service ทั้งหมดจาก repository ที่เลือกไว้
//
// 🔑 ฟังก์ชันนี้คือ "แผนที่การพึ่งพา" ของทั้งระบบในที่เดียว
// อ่านจากบนลงล่างแล้วเห็นว่าใครต้องการอะไร โดยไม่มี reflection ไม่มี container
//
// สังเกตว่าทุก dependency ที่ข้าม domain ถูกส่งผ่าน bridge.* เสมอ
// — ไม่มี domain ไหน import domain อื่นตรงๆ (archlint บังคับไว้)
func buildServices(repos *repoSet, declineOver money.Satang) httpapi.Services {
	wallClock := clock.System{}
	gateway := fakepay.New()
	gateway.DeclineOver = declineOver

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
		repos.tx, // order.TxManager
	)

	return httpapi.Services{
		Catalog:   catalogSvc,
		Customers: customerSvc,
		Carts:     cartSvc,
		Orders:    orderSvc,
		Payments:  paymentSvc,
		Checkout: checkout.NewService(
			bridge.CheckoutBaskets{Carts: cartSvc},  // checkout.Baskets ← cart
			bridge.CheckoutOrders{Orders: orderSvc}, // checkout.Orders  ← order
		),
	}
}

// repoSet รวม repository ทุกตัว — ทำให้ openRepos คืนของชุดเดียวจบ
type repoSet struct {
	products  catalog.Repository
	customers customer.Repository
	carts     cart.Repository
	orders    order.Repository
	payments  payment.Repository

	// tx คือตัวจัดการ transaction ของ store ตัวนั้น
	// postgres = ของจริง · memory/json = ตัวที่ไม่ทำอะไร (ดู noTx)
	tx order.TxManager

	// close ปิดทรัพยากรของ adapter (มีเฉพาะบางตัว เช่น connection pool)
	close func()
}

// openRepos เลือก adapter ตาม flag
//
// สังเกตว่า return type เป็น **interface ของ domain** ไม่ใช่ struct ของ adapter
// → ที่เหลือของโปรแกรมไม่รู้เลยว่าข้างในเป็น memory หรือ json
func openRepos(ctx context.Context, kind, dir, dsn string, tracing bool) (*repoSet, error) {
	switch kind {
	case "postgres", "pg":
		if dsn == "" {
			return nil, fmt.Errorf("-store=postgres ต้องระบุ -dsn หรือ env DATABASE_URL\n" +
				"ตัวอย่าง: postgres://shop:shop@localhost:5433/shop?sslmode=disable")
		}
		s, err := pgstore.Open(ctx, dsn, pgstore.Options{Tracing: tracing})
		if err != nil {
			return nil, err
		}
		return &repoSet{
			products:  s.Catalog(),
			customers: s.Customers(),
			carts:     s.Carts(),
			orders:    s.Orders(),
			payments:  s.Payments(),
			tx:        s.Tx(), // ✅ transaction จริง
			close:     s.Close,
		}, nil
	case "memory":
		return &repoSet{
			products:  memory.NewProducts(),
			customers: memory.NewCustomers(),
			carts:     memory.NewCarts(),
			orders:    memory.NewOrders(),
			payments:  memory.NewPayments(),
			tx:        noTx{}, // ⚠️ ไม่มี transaction — ดู noTx
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
			tx:        noTx{}, // ⚠️ ไม่มี transaction — ดู noTx
		}, nil
	default:
		return nil, fmt.Errorf("unknown -store=%q (ใช้ memory, json หรือ postgres)", kind)
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

// noTx เป็น TxManager สำหรับ store ที่ไม่รองรับ transaction (memory, json)
//
// ⚠️ มันแค่เรียก fn ตรงๆ — ไม่มี rollback
// ความปลอดภัยของ Place/Cancel/Ship บน store พวกนี้จึงพึ่ง "compensating action"
// (releaseAll ใน internal/order/service.go) ซึ่งเป็น best-effort ไม่ใช่ของแท้
//
// ตั้งใจให้เห็นชัดว่าตรงนี้คือข้อจำกัดของ adapter ไม่ใช่ของ domain:
// เปลี่ยนเป็น -store=postgres เมื่อไหร่ ได้ atomicity จริงทันทีโดยไม่ต้องแก้ domain
type noTx struct{}

func (noTx) Do(ctx context.Context, fn func(ctx context.Context) error) error { return fn(ctx) }
