// Package token ออกและตรวจ JWT
//
// เป็น adapter ล้วน — domain ไม่รู้จัก package นี้เลย และไม่ควรรู้
// "ใครเป็นคนยิง request" เป็นเรื่องของขอบระบบ ไม่ใช่กฎธุรกิจของ order/catalog
//
// # ทำไมแยกเป็น package ไม่ยัดใน httpapi
//
// การออก/ตรวจ token ไม่ผูกกับ HTTP — วันหนึ่งมี gRPC หรือ worker ที่ต้องตรวจ token
// เดียวกันก็เรียก package นี้ได้ · ส่วน httpapi มีหน้าที่แค่ "แกะจาก header แล้วส่งมาตรวจ"
package token

import (
	"errors"
	"fmt"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

var (
	ErrMissing = errors.New("token: ไม่มี token")
	ErrInvalid = errors.New("token: token ไม่ถูกต้องหรือหมดอายุ")
)

// Role คือสิทธิ์ที่ token ตัวนั้นถืออยู่
type Role string

const (
	// RoleCustomer ทำได้เฉพาะเรื่องของตัวเอง (ตะกร้า สั่งซื้อ จ่ายเงิน)
	RoleCustomer Role = "customer"
	// RoleAdmin จัดการหลังร้านได้ (เพิ่ม/ปิดสินค้า เติมสต็อก เลื่อนสถานะออเดอร์)
	RoleAdmin Role = "admin"
)

// Claims คือสิ่งที่เราฝากไว้ใน token
type Claims struct {
	Role Role `json:"role"`
	jwt.RegisteredClaims
}

// Issuer ออกและตรวจ token ด้วย HMAC
//
// เลือก HS256 (symmetric) เพราะระบบนี้ออกและตรวจ token ด้วยตัวเอง
// ถ้าวันหนึ่งมี service อื่นต้องตรวจด้วย ควรย้ายไป RS256/ES256
// (asymmetric) เพื่อไม่ต้องแจก secret ให้ทุกคน
type Issuer struct {
	secret []byte
	ttl    time.Duration
}

// New สร้าง Issuer · คืน error ถ้า secret สั้นเกินไป
func New(secret string, ttl time.Duration) (*Issuer, error) {
	// 32 ไบต์ = ความยาวขั้นต่ำที่สมเหตุสมผลสำหรับ HMAC-SHA256
	// secret สั้นๆ เดาได้ = token ปลอมได้ = ระบบ auth ไม่มีความหมาย
	if len(secret) < 32 {
		return nil, fmt.Errorf("token: secret ต้องยาวอย่างน้อย 32 ตัวอักษร (ได้ %d)", len(secret))
	}
	if ttl <= 0 {
		ttl = time.Hour
	}
	return &Issuer{secret: []byte(secret), ttl: ttl}, nil
}

// Issue ออก token ให้ subject (เช่น customer id) พร้อมสิทธิ์
func (i *Issuer) Issue(subject string, role Role, now time.Time) (string, error) {
	c := Claims{
		Role: role,
		RegisteredClaims: jwt.RegisteredClaims{
			Subject:   subject,
			IssuedAt:  jwt.NewNumericDate(now),
			ExpiresAt: jwt.NewNumericDate(now.Add(i.ttl)),
			Issuer:    "go-shop",
		},
	}
	signed, err := jwt.NewWithClaims(jwt.SigningMethodHS256, c).SignedString(i.secret)
	if err != nil {
		return "", fmt.Errorf("token: เซ็นไม่สำเร็จ: %w", err)
	}
	return signed, nil
}

// Verify ตรวจ token แล้วคืน claims
//
// 🔑 ระบุ WithValidMethods ไว้ชัดเจน — กันช่องโหว่คลาสสิกที่ผู้โจมตีส่ง
// token ที่ alg="none" หรือสลับไปใช้ alg อื่นเพื่อเลี่ยงการตรวจลายเซ็น
func (i *Issuer) Verify(raw string) (*Claims, error) {
	if raw == "" {
		return nil, ErrMissing
	}
	var c Claims
	_, err := jwt.ParseWithClaims(raw, &c,
		func(*jwt.Token) (any, error) { return i.secret, nil },
		jwt.WithValidMethods([]string{jwt.SigningMethodHS256.Alg()}),
		jwt.WithIssuer("go-shop"),
		jwt.WithExpirationRequired(),
	)
	if err != nil {
		// ไม่ส่งรายละเอียดของ jwt ออกไป — บอกแค่ว่าใช้ไม่ได้
		// (รายละเอียดว่า "หมดอายุ" หรือ "ลายเซ็นผิด" เป็นข้อมูลที่ช่วยผู้โจมตี)
		return nil, fmt.Errorf("%w: %w", ErrInvalid, err)
	}
	if c.Role != RoleAdmin && c.Role != RoleCustomer {
		return nil, fmt.Errorf("%w: role ไม่รู้จัก %q", ErrInvalid, c.Role)
	}
	return &c, nil
}
