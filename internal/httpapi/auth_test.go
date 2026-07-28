package httpapi_test

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/soya196/go-shop/internal/httpapi"
	"github.com/soya196/go-shop/internal/token"
)

// secret สำหรับเทสเท่านั้น — ยาว 32+ ตามที่ token.New บังคับ
const testSecret = "test-secret-ที่ยาวพอสำหรับ-HS256-อย่างน้อย-32-ไบต์"

func newIssuer(t *testing.T) *token.Issuer {
	t.Helper()
	is, err := token.New(testSecret, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	return is
}

// authServer สร้าง API ที่ "เปิด auth" — ต่างจาก newServer() ที่ปิดไว้
func authServer(t *testing.T) (*httptest.Server, *token.Issuer) {
	t.Helper()
	is := newIssuer(t)
	srv := newServerWith(t, func(c *cfgOverride) { c.tokens = is })
	return srv, is
}

func do(t *testing.T, srv *httptest.Server, method, path, bearer string) int {
	t.Helper()
	req, err := http.NewRequest(method, srv.URL+path, strings.NewReader(`{}`))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	if bearer != "" {
		req.Header.Set("Authorization", "Bearer "+bearer)
	}
	resp, err := srv.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	return resp.StatusCode
}

// ═══════════════════════════════════════════════════════════════════
// เส้นแบ่งที่เทสชุดนี้คุมไว้
//
//	401 = "ไม่รู้ว่าคุณเป็นใคร"      (ไม่มี token / token ใช้ไม่ได้)
//	403 = "รู้ว่าเป็นใคร แต่ห้าม"   (token ใช้ได้ แต่สิทธิ์ไม่ถึง)
//
// แยกให้ถูกสำคัญมาก: client เห็น 401 แล้วรู้ว่าต้องล็อกอินใหม่
// เห็น 403 แล้วรู้ว่าล็อกอินใหม่ก็ไม่ช่วย ต้องไปขอสิทธิ์
// ═══════════════════════════════════════════════════════════════════

func TestPublicEndpointsNeedNoToken(t *testing.T) {
	srv, _ := authServer(t)
	for _, path := range []string{"/healthz", "/readyz", "/products"} {
		if code := do(t, srv, "GET", path, ""); code != http.StatusOK {
			t.Errorf("%s ควรเปิดให้ทุกคน แต่ได้ %d", path, code)
		}
	}
}

func TestProtectedEndpointWithoutTokenIs401(t *testing.T) {
	srv, _ := authServer(t)
	if code := do(t, srv, "GET", "/orders", ""); code != http.StatusUnauthorized {
		t.Fatalf("ไม่มี token ต้องได้ 401 แต่ได้ %d", code)
	}
}

func TestCustomerTokenOnAdminEndpointIs403(t *testing.T) {
	srv, is := authServer(t)
	tk, err := is.Issue("cus_1", token.RoleCustomer, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if code := do(t, srv, "GET", "/orders", tk); code != http.StatusForbidden {
		t.Fatalf("ลูกค้าเรียก endpoint แอดมิน ต้องได้ 403 แต่ได้ %d", code)
	}
}

func TestAdminTokenPasses(t *testing.T) {
	srv, is := authServer(t)
	tk, err := is.Issue("adm_1", token.RoleAdmin, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if code := do(t, srv, "GET", "/orders", tk); code != http.StatusOK {
		t.Fatalf("แอดมินต้องผ่าน แต่ได้ %d", code)
	}
}

func TestCustomerCanUseCustomerEndpoints(t *testing.T) {
	srv, is := authServer(t)
	tk, _ := is.Issue("cus_1", token.RoleCustomer, time.Now())
	// /carts ต้องการแค่ "ล็อกอินแล้ว" — ลูกค้าเรียกได้
	// (จะ 400 เพราะ body ว่างไม่มี customer_id แต่ต้องไม่ใช่ 401/403)
	code := do(t, srv, "POST", "/carts", tk)
	if code == http.StatusUnauthorized || code == http.StatusForbidden {
		t.Fatalf("ลูกค้าควรผ่านด่าน auth ของ /carts แต่ได้ %d", code)
	}
}

func TestExpiredTokenIs401(t *testing.T) {
	srv, is := authServer(t)
	// ออก token ที่หมดอายุไปแล้ว (ttl 1 ชม. · ออกย้อนหลัง 2 ชม.)
	tk, _ := is.Issue("adm_1", token.RoleAdmin, time.Now().Add(-2*time.Hour))
	if code := do(t, srv, "GET", "/orders", tk); code != http.StatusUnauthorized {
		t.Fatalf("token หมดอายุต้องได้ 401 แต่ได้ %d", code)
	}
}

func TestTokenSignedWithOtherSecretIs401(t *testing.T) {
	srv, _ := authServer(t)
	other, err := token.New("secret-อื่นที่ไม่ใช่ของเรา-แต่ยาวพอ-32-ไบต์-นะ", time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	tk, _ := other.Issue("adm_1", token.RoleAdmin, time.Now())
	if code := do(t, srv, "GET", "/orders", tk); code != http.StatusUnauthorized {
		t.Fatalf("token ที่เซ็นด้วย secret อื่นต้องได้ 401 แต่ได้ %d", code)
	}
}

func TestMalformedAuthorizationHeaderIs401(t *testing.T) {
	srv, is := authServer(t)
	tk, _ := is.Issue("adm_1", token.RoleAdmin, time.Now())

	for _, h := range []string{
		"",                 // ไม่มี header
		tk,                 // ลืมคำว่า Bearer
		"Basic " + tk,      // scheme ผิด
		"Bearer ",          // มี scheme แต่ไม่มี token
		"Bearer not.a.jwt", // ไม่ใช่ JWT
	} {
		req, _ := http.NewRequest("GET", srv.URL+"/orders", nil)
		if h != "" {
			req.Header.Set("Authorization", h)
		}
		resp, err := srv.Client().Do(req)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if resp.StatusCode != http.StatusUnauthorized {
			t.Errorf("Authorization=%q ต้องได้ 401 แต่ได้ %d", h, resp.StatusCode)
		}
		if h == "" && resp.Header.Get("WWW-Authenticate") == "" {
			t.Error("401 ควรมี header WWW-Authenticate บอก client ว่าต้องใช้ scheme ไหน")
		}
	}
}

// 🔑 กันช่องโหว่คลาสสิก: ผู้โจมตีสลับ alg เป็น none เพื่อเลี่ยงการตรวจลายเซ็น
func TestAlgNoneTokenRejected(t *testing.T) {
	is := newIssuer(t)
	// header {"alg":"none","typ":"JWT"} + payload ที่อ้างว่าเป็น admin + ลายเซ็นว่าง
	const forged = "eyJhbGciOiJub25lIiwidHlwIjoiSldUIn0." +
		"eyJyb2xlIjoiYWRtaW4iLCJzdWIiOiJoYWNrZXIiLCJpc3MiOiJnby1zaG9wIiwiZXhwIjo0MTAyNDQ0ODAwfQ."
	if _, err := is.Verify(forged); err == nil {
		t.Fatal("🚨 token ที่ alg=none ผ่านการตรวจ — ช่องโหว่ร้ายแรง")
	}
}

// เมื่อไม่ตั้ง secret ระบบต้องยังใช้งานได้ (dev/เดโม) — และเทสอื่นทั้งหมดพึ่งพฤติกรรมนี้
func TestAuthDisabledLetsEveryoneThrough(t *testing.T) {
	srv := newServer(t) // ไม่ได้ตั้ง tokens
	if code := do(t, srv, "GET", "/orders", ""); code != http.StatusOK {
		t.Fatalf("auth ปิดอยู่ ต้องเรียกได้เลย แต่ได้ %d", code)
	}
}

func TestShortSecretRejected(t *testing.T) {
	if _, err := token.New("สั้นไป", time.Hour); err == nil {
		t.Fatal("secret สั้นๆ ต้องถูกปฏิเสธ — ไม่งั้น token ปลอมได้")
	}
}

var _ = httpapi.RequestIDHeader // กัน import ไม่ถูกใช้ถ้าตัดเทสบางตัวออก
