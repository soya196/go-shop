// Package clock เป็น adapter สำหรับ "เวลา"
//
// ทำไมต้องห่อ time.Now(): เพราะ time.Now() คือ side effect ที่ทำให้ test ไม่ deterministic
// domain ประกาศ port Clock ไว้ → prod ใช้ System{} → test ใช้ Fixed{} ที่ควบคุมเวลาได้
package clock

import "time"

// System คือนาฬิกาจริงของเครื่อง — ใช้ตอนรันจริง
type System struct{}

func (System) Now() time.Time { return time.Now().UTC() }

// Fixed คือนาฬิกาที่หยุดนิ่ง — ใช้ตอนเทส
type Fixed struct{ At time.Time }

func (f Fixed) Now() time.Time { return f.At }

// Ticking เดินหน้าทีละ Step ทุกครั้งที่ถูกถาม — ใช้เทสลำดับเหตุการณ์
type Ticking struct {
	At   time.Time
	Step time.Duration
}

func (t *Ticking) Now() time.Time {
	step := t.Step
	if step == 0 {
		step = time.Second
	}
	t.At = t.At.Add(step)
	return t.At
}
