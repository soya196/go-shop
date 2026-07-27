package httpapi_test

import (
	"net/http"
	"testing"

	"github.com/soya196/go-shop/internal/httpapi"
)

// ───────────────────────── request id ─────────────────────────

func TestRequestIDIsGeneratedWhenMissing(t *testing.T) {
	srv := newServer(t)

	resp, err := srv.Client().Get(srv.URL + "/healthz")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	id := resp.Header.Get(httpapi.RequestIDHeader)
	if id == "" {
		t.Fatal("ทุก response ต้องมี X-Request-Id ติดกลับมา")
	}
	if len(id) < 8 {
		t.Fatalf("request id สั้นผิดปกติ: %q", id)
	}
}

// ส่ง id มาเอง (เช่นจาก API gateway ต้นทาง) → ต้องใช้ของเดิม ไม่สร้างใหม่
// ไม่งั้น trace ข้าม service จะขาด
func TestRequestIDIsPropagated(t *testing.T) {
	srv := newServer(t)

	req, _ := http.NewRequest("GET", srv.URL+"/healthz", nil)
	req.Header.Set(httpapi.RequestIDHeader, "trace-from-gateway-123")

	resp, err := srv.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	if got := resp.Header.Get(httpapi.RequestIDHeader); got != "trace-from-gateway-123" {
		t.Fatalf("request id = %q, want ของเดิมที่ส่งมา", got)
	}
}

func TestAbsurdlyLongRequestIDIsReplaced(t *testing.T) {
	srv := newServer(t)

	long := make([]byte, 500)
	for i := range long {
		long[i] = 'x'
	}
	req, _ := http.NewRequest("GET", srv.URL+"/healthz", nil)
	req.Header.Set(httpapi.RequestIDHeader, string(long))

	resp, err := srv.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	if got := resp.Header.Get(httpapi.RequestIDHeader); len(got) > 128 {
		t.Fatalf("ต้องแทนที่ id ที่ยาวเกินเหตุ, ได้ยาว %d", len(got))
	}
}

func TestRequestIDIsUniquePerRequest(t *testing.T) {
	srv := newServer(t)
	seen := map[string]bool{}
	for i := 0; i < 20; i++ {
		resp, err := srv.Client().Get(srv.URL + "/healthz")
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		id := resp.Header.Get(httpapi.RequestIDHeader)
		if seen[id] {
			t.Fatalf("request id ซ้ำ: %s", id)
		}
		seen[id] = true
	}
}

// ───────────────────────── cors ─────────────────────────

func TestCORSClosedByDefault(t *testing.T) {
	srv := newServer(t) // ไม่ตั้ง AllowedOrigins

	req, _ := http.NewRequest("GET", srv.URL+"/products", nil)
	req.Header.Set("Origin", "https://evil.example.com")
	resp, err := srv.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	if got := resp.Header.Get("Access-Control-Allow-Origin"); got != "" {
		t.Fatalf("ค่าเริ่มต้นต้องปิด CORS, ได้ %q", got)
	}
}

func TestCORSAllowsListedOrigin(t *testing.T) {
	srv := newServerWith(t, func(c *cfgOverride) {
		c.allowedOrigins = []string{"https://shop.example.com"}
	})

	req, _ := http.NewRequest("GET", srv.URL+"/products", nil)
	req.Header.Set("Origin", "https://shop.example.com")
	resp, err := srv.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	if got := resp.Header.Get("Access-Control-Allow-Origin"); got != "https://shop.example.com" {
		t.Fatalf("Allow-Origin = %q", got)
	}
	if got := resp.Header.Get("Vary"); got != "Origin" {
		t.Errorf("ต้องมี Vary: Origin เพื่อไม่ให้ cache ปนกันข้าม origin, ได้ %q", got)
	}
}

func TestCORSRejectsUnlistedOrigin(t *testing.T) {
	srv := newServerWith(t, func(c *cfgOverride) {
		c.allowedOrigins = []string{"https://shop.example.com"}
	})

	req, _ := http.NewRequest("GET", srv.URL+"/products", nil)
	req.Header.Set("Origin", "https://evil.example.com")
	resp, err := srv.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	if got := resp.Header.Get("Access-Control-Allow-Origin"); got != "" {
		t.Fatalf("origin ที่ไม่ได้อยู่ในรายการต้องไม่ผ่าน, ได้ %q", got)
	}
}

func TestCORSPreflight(t *testing.T) {
	srv := newServerWith(t, func(c *cfgOverride) { c.allowedOrigins = []string{"*"} })

	req, _ := http.NewRequest("OPTIONS", srv.URL+"/products", nil)
	req.Header.Set("Origin", "https://any.example.com")
	req.Header.Set("Access-Control-Request-Method", "POST")

	resp, err := srv.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("preflight status = %d, want 204", resp.StatusCode)
	}
	if got := resp.Header.Get("Access-Control-Allow-Methods"); got == "" {
		t.Error("preflight ต้องบอก method ที่อนุญาต")
	}
}

// ───────────────────────── health / readiness ─────────────────────────

func TestReadyzReportsVersion(t *testing.T) {
	srv := newServer(t)
	code, body := call(t, srv, "GET", "/readyz", nil)
	mustStatus(t, http.StatusOK, code, "readyz", body)

	if body["status"] != "ready" {
		t.Errorf("status = %v, want ready", body["status"])
	}
	if body["version"] != "test" {
		t.Errorf("version = %v, want test", body["version"])
	}
}

// ตอน drain ต้องตอบ 503 เพื่อให้ load balancer ถอดเราออกจาก pool
func TestReadyzTurns503WhenDraining(t *testing.T) {
	api := newAPI(t)
	srv := newTestServer(t, api.Routes())

	api.SetReady(false)

	code, body := call(t, srv, "GET", "/readyz", nil)
	mustStatus(t, http.StatusServiceUnavailable, code, "readyz while draining", body)

	// liveness ต้องยัง 200 — ไม่งั้น k8s จะฆ่า pod ทิ้งกลางคัน แทนที่จะปล่อยให้ drain จบ
	code, body = call(t, srv, "GET", "/healthz", nil)
	mustStatus(t, http.StatusOK, code, "healthz while draining", body)
}

func TestLegacyHealthStillWorks(t *testing.T) {
	srv := newServer(t)
	code, body := call(t, srv, "GET", "/health", nil)
	mustStatus(t, http.StatusOK, code, "legacy /health", body)
}
