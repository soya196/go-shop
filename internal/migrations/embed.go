// Package migrations ฝังไฟล์ SQL ของ goose เข้าไปใน binary
//
// ทำไมต้องฝัง: deploy ไฟล์เดียวจบ ไม่ต้องแบกโฟลเดอร์ migration ไปด้วย
// และ binary กับ schema ที่มันคาดหวังจะ "เดินทางไปด้วยกัน" เสมอ
// — กันเคสคลาสสิกที่ deploy โค้ดใหม่แต่ลืมรัน migration
package migrations

import "embed"

//go:embed *.sql
var FS embed.FS
