// Package uid เป็น adapter สำหรับ "สร้าง id"
//
// domain ทุกตัวประกาศ port IDGenerator ไว้เอง — package นี้คือคนทำงานจริง
// แยกออกมาเพราะการสุ่มคือ side effect: ถ้า domain เรียก rand เองจะเทสยาก
package uid

import (
	"crypto/rand"
	"encoding/binary"
	"fmt"
	"sync/atomic"
	"time"
)

// Random สร้าง id แบบเรียงตามเวลา + สุ่มท้าย เช่น "prd_1t8k2n9q4w7x"
//
// เรียงตามเวลาเพื่อให้ index ของฐานข้อมูลจริงไม่กระจาย (เหตุผลเดียวกับที่คนใช้ ULID)
type Random struct{ Prefix string }

func (r Random) NewID() string {
	var b [8]byte
	if _, err := rand.Read(b[:]); err != nil {
		// crypto/rand พังแทบเป็นไปไม่ได้ แต่ถ้าพังจริงต้องดังไม่ใช่เงียบ
		panic(fmt.Sprintf("uid: crypto/rand failed: %v", err))
	}
	ts := uint64(time.Now().UTC().UnixMilli())
	tail := binary.BigEndian.Uint64(b[:])
	return fmt.Sprintf("%s_%010s%06s", r.Prefix, base36(ts), base36(tail%36_000_000))
}

// Sequential สร้าง id เรียงเลข — ใช้ตอน seed หรือเทสที่อยากได้ค่าเดาได้
type Sequential struct {
	Prefix string
	n      atomic.Int64
}

func (s *Sequential) NewID() string {
	return fmt.Sprintf("%s_%04d", s.Prefix, s.n.Add(1))
}

const digits = "0123456789abcdefghijklmnopqrstuvwxyz"

func base36(n uint64) string {
	if n == 0 {
		return "0"
	}
	var buf [16]byte
	i := len(buf)
	for n > 0 {
		i--
		buf[i] = digits[n%36]
		n /= 36
	}
	return string(buf[i:])
}
