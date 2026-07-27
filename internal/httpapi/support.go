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

	"github.com/soya196/go-shop/internal/money"
)

// contextT ตั้งชื่อย่อไว้ให้ signature ของ transition อ่านง่าย
type contextT = context.Context

// maxBody จำกัดขนาด request body — กัน client ยัด payload มหึมาเข้ามา
const maxBody = 1 << 20 // 1 MiB

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	if v == nil {
		return
	}
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	_ = enc.Encode(v)
}

func writeErr(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, map[string]string{"error": msg})
}

// decode อ่าน JSON body พร้อมกันเคสพื้นฐาน — คืน false ถ้าตอบ error ไปแล้ว
func decode(w http.ResponseWriter, r *http.Request, dst any) bool {
	r.Body = http.MaxBytesReader(w, r.Body, maxBody)
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()

	if err := dec.Decode(dst); err != nil {
		var maxErr *http.MaxBytesError
		switch {
		case errors.Is(err, io.EOF):
			writeErr(w, http.StatusBadRequest, "request body is empty")
		case errors.As(err, &maxErr):
			writeErr(w, http.StatusRequestEntityTooLarge, "request body too large")
		default:
			writeErr(w, http.StatusBadRequest, "invalid JSON: "+err.Error())
		}
		return false
	}
	// ห้ามมี JSON ก้อนที่สองต่อท้าย
	if dec.More() {
		writeErr(w, http.StatusBadRequest, "request body must contain a single JSON object")
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

type middleware func(http.Handler) http.Handler

// statusRecorder จำ status code + จำนวน byte ไว้ให้ log
type statusRecorder struct {
	http.ResponseWriter
	status  int
	written int
}

func (s *statusRecorder) WriteHeader(code int) {
	s.status = code
	s.ResponseWriter.WriteHeader(code)
}

func (s *statusRecorder) Write(b []byte) (int, error) {
	n, err := s.ResponseWriter.Write(b)
	s.written += n
	return n, err
}

// requestID ให้ทุก request มีรหัสติดตัว
//
// ถ้า client (หรือ gateway ต้นทาง) ส่ง X-Request-Id มาแล้วให้ใช้ของเดิม
// เพื่อให้ตาม trace ข้ามหลาย service ได้ · ถ้าไม่มีก็สร้างใหม่
// และตอบกลับไปใน response header เสมอ → ลูกค้าแจ้งปัญหาพร้อม id = หา log เจอทันที
func requestID(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id := r.Header.Get(RequestIDHeader)
		if id == "" || len(id) > 128 {
			id = newRequestID()
		}
		w.Header().Set(RequestIDHeader, id)
		next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), requestIDKey, id)))
	})
}

// requestLog เขียน access log หนึ่งบรรทัดต่อ request
//
// ระดับ log เลือกตาม status: 5xx = Error, 4xx = Warn, ที่เหลือ = Info
// → กรอง log ตอนมีปัญหาได้โดยไม่ต้องอ่านทุกบรรทัด
func requestLog(log *slog.Logger) middleware {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			start := time.Now()
			rec := &statusRecorder{ResponseWriter: w, status: http.StatusOK}
			next.ServeHTTP(rec, r)

			attrs := []any{
				"request_id", RequestIDFrom(r.Context()),
				"method", r.Method,
				"path", r.URL.Path,
				"status", rec.status,
				"bytes", rec.written,
				"ms", time.Since(start).Milliseconds(),
			}
			switch {
			case rec.status >= 500:
				log.Error("http", attrs...)
			case rec.status >= 400:
				log.Warn("http", attrs...)
			default:
				log.Info("http", attrs...)
			}
		})
	}
}

// recoverer กัน panic ใน handler ไม่ให้ล้มทั้งเซิร์ฟเวอร์
//
// เก็บ stack trace ไว้ใน log แต่ไม่ส่งออกไปให้ client (กันข้อมูลภายในรั่ว)
func recoverer(log *slog.Logger) middleware {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			defer func() {
				if rec := recover(); rec != nil {
					log.Error("panic recovered",
						"request_id", RequestIDFrom(r.Context()),
						"err", rec,
						"method", r.Method,
						"path", r.URL.Path,
						"stack", string(debug.Stack()),
					)
					writeErr(w, http.StatusInternalServerError, "internal error")
				}
			}()
			next.ServeHTTP(w, r)
		})
	}
}

// cors อนุญาตให้เบราว์เซอร์จาก origin ที่กำหนดเรียก API ได้
//
// รายการว่าง = ปิดสนิท (ปลอดภัยเป็นค่าเริ่มต้น) · ["*"] = เปิดหมด (ใช้เฉพาะตอน dev)
func cors(allowed []string) middleware {
	allowAll := len(allowed) == 1 && allowed[0] == "*"
	set := make(map[string]bool, len(allowed))
	for _, o := range allowed {
		set[o] = true
	}

	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			origin := r.Header.Get("Origin")
			if origin != "" && (allowAll || set[origin]) {
				if allowAll {
					w.Header().Set("Access-Control-Allow-Origin", "*")
				} else {
					w.Header().Set("Access-Control-Allow-Origin", origin)
					w.Header().Add("Vary", "Origin")
				}
				w.Header().Set("Access-Control-Allow-Methods", "GET, POST, PATCH, DELETE, OPTIONS")
				w.Header().Set("Access-Control-Allow-Headers", "Content-Type, "+RequestIDHeader)
				w.Header().Set("Access-Control-Expose-Headers", RequestIDHeader)
				w.Header().Set("Access-Control-Max-Age", "600")
			}
			if r.Method == http.MethodOptions {
				w.WriteHeader(http.StatusNoContent)
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}
