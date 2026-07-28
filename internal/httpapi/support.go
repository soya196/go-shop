package httpapi

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"runtime/debug"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/gin-gonic/gin/binding"

	"github.com/soya196/go-shop/internal/money"
)

// contextT ตั้งชื่อย่อไว้ให้ signature ของ transition อ่านง่าย
type contextT = context.Context

// maxBody จำกัดขนาด request body — กัน client ยัด payload มหึมาเข้ามา
const maxBody = 1 << 20 // 1 MiB

func writeJSON(c *gin.Context, status int, v any) {
	if v == nil {
		c.Status(status)
		return
	}
	c.IndentedJSON(status, v)
}

func writeErr(c *gin.Context, status int, msg string) {
	c.AbortWithStatusJSON(status, gin.H{"error": msg})
}

// bind อ่าน JSON body พร้อมกันเคสพื้นฐาน — คืน false ถ้าตอบ error ไปแล้ว
//
// ทำไมไม่ใช้ c.ShouldBindJSON ตรงๆ:
// ค่า default ของ gin ยอมรับ field ที่ไม่รู้จักแบบเงียบๆ และไม่เช็ค JSON ก้อนที่สอง
// ซึ่งหลวมกว่าที่ API นี้ต้องการ — พิมพ์ชื่อ field ผิดควรได้ 400 ไม่ใช่ค่า zero เงียบๆ
//
// แต่ยังได้ของดีจาก gin: หลัง decode สำเร็จจะเรียก validator ให้ด้วย
// → ใส่ tag `binding:"required,min=1"` บน struct แล้วใช้ได้เลย
func bind(c *gin.Context, dst any) bool {
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, maxBody)
	dec := json.NewDecoder(c.Request.Body)
	dec.DisallowUnknownFields()

	if err := dec.Decode(dst); err != nil {
		var maxErr *http.MaxBytesError
		switch {
		case errors.Is(err, io.EOF):
			writeErr(c, http.StatusBadRequest, "request body is empty")
		case errors.As(err, &maxErr):
			writeErr(c, http.StatusRequestEntityTooLarge, "request body too large")
		default:
			writeErr(c, http.StatusBadRequest, "invalid JSON: "+err.Error())
		}
		return false
	}
	// ห้ามมี JSON ก้อนที่สองต่อท้าย
	if dec.More() {
		writeErr(c, http.StatusBadRequest, "request body must contain a single JSON object")
		return false
	}
	// validator ของ gin — เป็น no-op ถ้า struct ไม่มี tag `binding:"..."`
	if err := binding.Validator.ValidateStruct(dst); err != nil {
		writeErr(c, http.StatusBadRequest, "validation failed: "+err.Error())
		return false
	}
	return true
}

// parseBaht แปลง "123.45" → 12345 สตางค์ โดยไม่ผ่าน float
//
// ตั้งใจไม่ใช้ strconv.ParseFloat เพราะ 123.45 แทนใน float64 ไม่ตรง
func parseBaht(s string) (money.Satang, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return 0, fmt.Errorf("empty amount")
	}
	neg := strings.HasPrefix(s, "-")
	s = strings.TrimPrefix(s, "-")

	whole, frac, hasFrac := strings.Cut(s, ".")
	if whole == "" {
		whole = "0"
	}
	baht, err := strconv.ParseInt(whole, 10, 64)
	if err != nil {
		return 0, fmt.Errorf("invalid amount %q", s)
	}
	var st int64
	if hasFrac {
		switch len(frac) {
		case 1:
			frac += "0"
		case 2:
		default:
			return 0, fmt.Errorf("amount %q has more than 2 decimals", s)
		}
		if st, err = strconv.ParseInt(frac, 10, 64); err != nil {
			return 0, fmt.Errorf("invalid amount %q", s)
		}
	}
	total := baht*100 + st
	if neg {
		total = -total
	}
	return money.FromSatang(total), nil
}

// ───────────────────────── request id ─────────────────────────

type ctxKey int

const requestIDKey ctxKey = iota

// RequestIDHeader คือ header ที่ใช้ส่งต่อ correlation id ระหว่าง service
const RequestIDHeader = "X-Request-Id"

// RequestIDFrom ดึง request id ออกจาก context (คืน "" ถ้าไม่มี)
//
// ทำเป็น exported function ไม่ให้ใครเข้าถึง context key ตรงๆ
func RequestIDFrom(ctx context.Context) string {
	id, _ := ctx.Value(requestIDKey).(string)
	return id
}

// newRequestID สร้าง id สั้นๆ แบบสุ่ม
func newRequestID() string {
	var b [12]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "req-unknown"
	}
	return hex.EncodeToString(b[:])
}

// ───────────────────────── middleware ─────────────────────────
//
// เขียนเป็น gin.HandlerFunc แทน func(http.Handler) http.Handler
// ลำดับการทำงานเหมือนเดิมทุกประการ: recoverer → requestID → cors → requestLog → handler
//
// ต่างกันตรงที่ gin เรียงตามลำดับที่ Use() ตรงๆ — ไม่ต้องอ่านกลับหัวเหมือนตอนห่อ stdlib

// requestID ให้ทุก request มีรหัสติดตัว
//
// ถ้า client (หรือ gateway ต้นทาง) ส่ง X-Request-Id มาแล้วให้ใช้ของเดิม
// เพื่อให้ตาม trace ข้ามหลาย service ได้ · ถ้าไม่มีก็สร้างใหม่
// และตอบกลับไปใน response header เสมอ → ลูกค้าแจ้งปัญหาพร้อม id = หา log เจอทันที
func requestID() gin.HandlerFunc {
	return func(c *gin.Context) {
		id := c.GetHeader(RequestIDHeader)
		if id == "" || len(id) > 128 {
			id = newRequestID()
		}
		c.Header(RequestIDHeader, id)
		c.Request = c.Request.WithContext(context.WithValue(c.Request.Context(), requestIDKey, id))
		c.Next()
	}
}

// requestLog เขียน access log หนึ่งบรรทัดต่อ request
//
// ระดับ log เลือกตาม status: 5xx = Error, 4xx = Warn, ที่เหลือ = Info
// → กรอง log ตอนมีปัญหาได้โดยไม่ต้องอ่านทุกบรรทัด
//
// ไม่ต้องเขียน statusRecorder เองแล้ว — gin.ResponseWriter จำ status/size ให้ในตัว
func requestLog(log *slog.Logger) gin.HandlerFunc {
	return func(c *gin.Context) {
		start := time.Now()
		c.Next()

		written := c.Writer.Size()
		if written < 0 {
			written = 0
		}
		status := c.Writer.Status()
		attrs := []any{
			"request_id", RequestIDFrom(c.Request.Context()),
			"method", c.Request.Method,
			"path", c.Request.URL.Path,
			"status", status,
			"bytes", written,
			"ms", time.Since(start).Milliseconds(),
		}
		switch {
		case status >= 500:
			log.Error("http", attrs...)
		case status >= 400:
			log.Warn("http", attrs...)
		default:
			log.Info("http", attrs...)
		}
	}
}

// recoverer กัน panic ใน handler ไม่ให้ล้มทั้งเซิร์ฟเวอร์
//
// เขียนเองแทน gin.Recovery() เพราะอยาก log ผ่าน slog ตัวเดียวกับที่เหลือ พร้อม request_id
// ไม่ใช่พิมพ์ลง stdout เป็นอีกรูปแบบหนึ่ง
//
// เก็บ stack trace ไว้ใน log แต่ไม่ส่งออกไปให้ client (กันข้อมูลภายในรั่ว)
func recoverer(log *slog.Logger) gin.HandlerFunc {
	return func(c *gin.Context) {
		defer func() {
			if rec := recover(); rec != nil {
				log.Error("panic recovered",
					"request_id", RequestIDFrom(c.Request.Context()),
					"err", rec,
					"method", c.Request.Method,
					"path", c.Request.URL.Path,
					"stack", string(debug.Stack()),
				)
				if c.Writer.Written() {
					c.Abort() // header ออกไปแล้ว เขียนทับไม่ได้
					return
				}
				writeErr(c, http.StatusInternalServerError, "internal error")
			}
		}()
		c.Next()
	}
}

// cors อนุญาตให้เบราว์เซอร์จาก origin ที่กำหนดเรียก API ได้
//
// รายการว่าง = ปิดสนิท (ปลอดภัยเป็นค่าเริ่มต้น) · ["*"] = เปิดหมด (ใช้เฉพาะตอน dev)
func cors(allowed []string) gin.HandlerFunc {
	allowAll := len(allowed) == 1 && allowed[0] == "*"
	set := make(map[string]bool, len(allowed))
	for _, o := range allowed {
		set[o] = true
	}

	return func(c *gin.Context) {
		origin := c.GetHeader("Origin")
		if origin != "" && (allowAll || set[origin]) {
			if allowAll {
				c.Header("Access-Control-Allow-Origin", "*")
			} else {
				c.Header("Access-Control-Allow-Origin", origin)
				c.Writer.Header().Add("Vary", "Origin")
			}
			c.Header("Access-Control-Allow-Methods", "GET, POST, PATCH, DELETE, OPTIONS")
			c.Header("Access-Control-Allow-Headers", "Content-Type, "+RequestIDHeader)
			c.Header("Access-Control-Expose-Headers", RequestIDHeader)
			c.Header("Access-Control-Max-Age", "600")
		}
		if c.Request.Method == http.MethodOptions {
			c.AbortWithStatus(http.StatusNoContent)
			return
		}
		c.Next()
	}
}
