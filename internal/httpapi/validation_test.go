package httpapi_test

import (
	"net/http"
	"strings"
	"testing"
)

// เทสชุดนี้คุมสัญญาของชั้น HTTP: "input ที่ผิดรูป ต้องถูกปฏิเสธก่อนถึง domain"
//
// 🔑 ทำไมต้องมีทั้ง binding tag และกฎใน domain:
//   - binding tag  = กันรูปแบบ (ขาด field, ติดลบ, ยาวเกิน) → 400 ตั้งแต่ขอบ
//   - กฎใน domain  = กันความหมายทางธุรกิจ (ของหมด, ลูกค้าถูกระงับ) → 422
//
// ไม่ใช่ของซ้ำซ้อน — ถ้าเอา binding tag ออก domain ก็ยังกันได้ แต่จะกันช้ากว่า
// และ error ที่ได้จะไม่บอกว่า "field ไหน" ผิด
func TestValidationRejectsBadInput(t *testing.T) {
	srv := newServer(t)

	cases := []struct {
		name   string
		method string
		path   string
		body   map[string]any
		want   int
		hint   string // คำที่ควรโผล่ในข้อความ error
	}{
		{"สินค้าไม่มี sku", "POST", "/products",
			map[string]any{"name": "ของ", "price_thb": "10.00", "stock": 1}, http.StatusBadRequest, "SKU"},
		{"สินค้า stock ติดลบ", "POST", "/products",
			map[string]any{"sku": "S1", "name": "ของ", "price_thb": "10.00", "stock": -5}, http.StatusBadRequest, "Stock"},
		{"ลูกค้าอีเมลผิดรูป", "POST", "/customers",
			map[string]any{"name": "A", "email": "ไม่ใช่อีเมล"}, http.StatusBadRequest, "Email"},
		{"ลูกค้าไม่มีชื่อ", "POST", "/customers",
			map[string]any{"email": "a@x.com"}, http.StatusBadRequest, "Name"},
		{"เปิดตะกร้าไม่ระบุลูกค้า", "POST", "/carts",
			map[string]any{}, http.StatusBadRequest, "CustomerID"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			code, out := call(t, srv, tc.method, tc.path, tc.body)
			if code != tc.want {
				t.Fatalf("status = %d, want %d (body: %v)", code, tc.want, out)
			}
			msg, _ := out["error"].(string)
			if !strings.Contains(msg, "validation failed") {
				t.Fatalf("ควรถูกปฏิเสธที่ชั้น validation แต่ได้: %q", msg)
			}
			if tc.hint != "" && !strings.Contains(msg, tc.hint) {
				t.Fatalf("ข้อความ error ควรบอกว่า field ไหนผิด (%q) แต่ได้: %q", tc.hint, msg)
			}
		})
	}
}

// qty = 0 บนตะกร้าคือ "เอาสินค้าออก" ไม่ใช่ input ผิด
//
// เคสนี้คือกับดักของการใส่ binding:"required" มั่วๆ กับ int
// — required บน int จะปฏิเสธค่า 0 ซึ่งที่นี่เป็นการใช้งานที่ถูกต้อง
func TestSetQtyZeroIsValidAndRemovesLine(t *testing.T) {
	srv := newServer(t)

	_, prod := call(t, srv, "POST", "/products", map[string]any{
		"sku": "Z1", "name": "ของ", "price_thb": "10.00", "stock": 5,
	})
	_, cus := call(t, srv, "POST", "/customers", map[string]any{"name": "A", "email": "z@x.com"})
	_, ct := call(t, srv, "POST", "/carts", map[string]any{"customer_id": str(t, cus, "id")})
	cartID := str(t, ct, "id")
	prodID := str(t, prod, "id")

	code, _ := call(t, srv, "POST", "/carts/"+cartID+"/items",
		map[string]any{"product_id": prodID, "qty": 2})
	if code != http.StatusOK {
		t.Fatalf("หยิบของใส่ตะกร้าไม่ผ่าน: %d", code)
	}

	code, out := call(t, srv, "PATCH", "/carts/"+cartID+"/items/"+prodID,
		map[string]any{"qty": 0})
	if code != http.StatusOK {
		t.Fatalf("qty=0 ต้องผ่าน (แปลว่าเอาออก) แต่ได้ %d: %v", code, out)
	}
	lines, _ := out["lines"].([]any)
	if len(lines) != 0 {
		t.Fatalf("qty=0 ต้องเอาสินค้าออกจากตะกร้า แต่ยังเหลือ %d รายการ", len(lines))
	}
}

// หยิบของ 0 ชิ้นไม่มีความหมาย — ต่างจาก setQty
func TestAddItemZeroQtyRejected(t *testing.T) {
	srv := newServer(t)

	_, prod := call(t, srv, "POST", "/products", map[string]any{
		"sku": "Z2", "name": "ของ", "price_thb": "10.00", "stock": 5,
	})
	_, cus := call(t, srv, "POST", "/customers", map[string]any{"name": "A", "email": "z2@x.com"})
	_, ct := call(t, srv, "POST", "/carts", map[string]any{"customer_id": str(t, cus, "id")})

	code, out := call(t, srv, "POST", "/carts/"+str(t, ct, "id")+"/items",
		map[string]any{"product_id": str(t, prod, "id"), "qty": 0})
	if code != http.StatusBadRequest {
		t.Fatalf("หยิบ 0 ชิ้นต้องได้ 400 แต่ได้ %d: %v", code, out)
	}
}
