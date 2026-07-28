package httpapi

import (
	"context"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"

	"github.com/soya196/go-shop/internal/token"
)

// ═══════════════════════════════════════════════════════════════════
// การยืนยันตัวตน — เป็นเรื่องของ "ขอบระบบ" ไม่ใช่ของ domain
//
// internal/order ไม่รู้และไม่ควรรู้ว่ามีคนล็อกอินอยู่ไหม
// มันรู้แค่ "ออเดอร์นี้เป็นของลูกค้า id นี้" ส่วนคนที่ยิงเข้ามาเป็นใคร
// เป็นหน้าที่ของชั้นนี้ตรวจ
//
// ⚠️ ปิดไว้เป็นค่าเริ่มต้น (ไม่ตั้ง secret = ไม่มีการตรวจ)
// เพื่อให้ dev/เดโมยิงได้เลย — แต่ตอนเปิดจะ log เตือนดังๆ ว่าปิดอยู่
// ═══════════════════════════════════════════════════════════════════

type claimsKey struct{}

// ClaimsFrom ดึงข้อมูลผู้ใช้จาก context (คืน nil ถ้าไม่ได้ล็อกอินหรือปิด auth อยู่)
func ClaimsFrom(ctx context.Context) *token.Claims {
	c, _ := ctx.Value(claimsKey{}).(*token.Claims)
	return c
}

// requireRole คืน middleware ที่ปล่อยผ่านเฉพาะ token ที่มีสิทธิ์ตามที่ระบุ
//
// ส่ง role ว่าง = ขอแค่ "ล็อกอินแล้ว" ไม่สนว่าเป็นใคร
func (a *API) requireRole(roles ...token.Role) gin.HandlerFunc {
	return func(c *gin.Context) {
		// ไม่ได้ตั้ง secret = auth ปิด → ปล่อยผ่านทุก request
		// (มี log เตือนตอน start server แล้ว ไม่ต้องเตือนซ้ำทุก request)
		if a.tokens == nil {
			c.Next()
			return
		}

		raw := bearerToken(c.GetHeader("Authorization"))
		claims, err := a.tokens.Verify(raw)
		if err != nil {
			// 401 = "ไม่รู้ว่าคุณเป็นใคร" · ต่างจาก 403 = "รู้ว่าเป็นใคร แต่ห้าม"
			c.Header("WWW-Authenticate", `Bearer realm="go-shop"`)
			writeErr(c, http.StatusUnauthorized, "ต้องแนบ Bearer token ที่ถูกต้อง")
			return
		}

		if len(roles) > 0 && !hasRole(claims.Role, roles) {
			a.log.Warn("ปฏิเสธเพราะสิทธิ์ไม่พอ",
				"request_id", RequestIDFrom(c.Request.Context()),
				"subject", claims.Subject, "role", claims.Role,
				"path", c.Request.URL.Path)
			writeErr(c, http.StatusForbidden, "สิทธิ์ไม่พอสำหรับคำสั่งนี้")
			return
		}

		c.Request = c.Request.WithContext(context.WithValue(c.Request.Context(), claimsKey{}, claims))
		c.Next()
	}
}

func hasRole(got token.Role, want []token.Role) bool {
	for _, r := range want {
		if got == r {
			return true
		}
	}
	return false
}

// bearerToken แกะ "Bearer xxx" ออกจาก header
//
// ไม่สนตัวพิมพ์ของคำว่า Bearer ตาม RFC 7235 (scheme เป็น case-insensitive)
func bearerToken(header string) string {
	scheme, rest, found := strings.Cut(strings.TrimSpace(header), " ")
	if !found || !strings.EqualFold(scheme, "Bearer") {
		return ""
	}
	return strings.TrimSpace(rest)
}
