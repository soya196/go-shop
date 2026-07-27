// Package money เป็น shared kernel — value object สำหรับจำนวนเงิน
//
// ทำไมต้องมี package แยก: เงินไม่ใช่ตัวเลขธรรมดา มันมีกฎของตัวเอง
// (ห้ามติดลบในบางบริบท, บวกกันได้, คูณจำนวนได้, แสดงผลมีรูปแบบ)
// การเก็บเป็น float64 คือ bug รอวันเกิด — เก็บเป็น "สตางค์" (int64) เสมอ
package money

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
)

// ErrNegative เกิดเมื่อพยายามสร้างจำนวนเงินติดลบในบริบทที่ไม่อนุญาต
var ErrNegative = errors.New("money: amount must not be negative")

// Satang คือจำนวนเงินหน่วยสตางค์ (1 บาท = 100 สตางค์)
//
// เก็บเป็น int64 เพื่อกัน float rounding — 0.1 + 0.2 != 0.3 ในโลก float
type Satang int64

// FromSatang สร้างจากสตางค์ตรงๆ
func FromSatang(s int64) Satang { return Satang(s) }

// FromBaht สร้างจากบาท (ปัดเป็นสตางค์ที่ใกล้ที่สุด)
func FromBaht(b float64) Satang {
	if b < 0 {
		return Satang(int64(b*100 - 0.5))
	}
	return Satang(int64(b*100 + 0.5))
}

// MustPositive คืน error ถ้าเป็นลบ — ใช้ตอน validate input
func (s Satang) MustPositive() error {
	if s < 0 {
		return ErrNegative
	}
	return nil
}

func (s Satang) Add(o Satang) Satang    { return s + o }
func (s Satang) Sub(o Satang) Satang    { return s - o }
func (s Satang) Mul(n int) Satang       { return s * Satang(n) }
func (s Satang) IsZero() bool           { return s == 0 }
func (s Satang) LessThan(o Satang) bool { return s < o }
func (s Satang) Int64() int64           { return int64(s) }

// String แสดงผลแบบไทย: ฿1,234.50
func (s Satang) String() string {
	neg := s < 0
	v := int64(s)
	if neg {
		v = -v
	}
	baht, st := v/100, v%100
	var sb strings.Builder
	if neg {
		sb.WriteByte('-')
	}
	sb.WriteString("฿")
	sb.WriteString(group(baht))
	sb.WriteString(fmt.Sprintf(".%02d", st))
	return sb.String()
}

// group ใส่ comma คั่นหลักพัน
func group(n int64) string {
	s := fmt.Sprintf("%d", n)
	if len(s) <= 3 {
		return s
	}
	var out []byte
	for i, c := range []byte(s) {
		if i > 0 && (len(s)-i)%3 == 0 {
			out = append(out, ',')
		}
		out = append(out, c)
	}
	return string(out)
}

// MarshalJSON ส่งออกทั้งค่าดิบและข้อความ — client ไม่ต้องเดาหน่วย
//
//	{"satang": 123450, "text": "฿1,234.50"}
func (s Satang) MarshalJSON() ([]byte, error) {
	return json.Marshal(struct {
		Satang int64  `json:"satang"`
		Text   string `json:"text"`
	}{int64(s), s.String()})
}

// UnmarshalJSON รับได้ทั้งตัวเลขสตางค์ล้วน และ object {"satang": n}
func (s *Satang) UnmarshalJSON(b []byte) error {
	var n int64
	if err := json.Unmarshal(b, &n); err == nil {
		*s = Satang(n)
		return nil
	}
	var o struct {
		Satang int64 `json:"satang"`
	}
	if err := json.Unmarshal(b, &o); err != nil {
		return fmt.Errorf("money: cannot parse %s", b)
	}
	*s = Satang(o.Satang)
	return nil
}
