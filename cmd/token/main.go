// Command token ออก JWT สำหรับทดสอบ/ใช้งานหลังบ้าน
//
//	go run ./cmd/token -secret "$JWT_SECRET" -role admin -sub adm_1
//	go run ./cmd/token -secret "$JWT_SECRET" -role customer -sub cus_9 -ttl 24h
//
// 🔑 ทำไมเป็น CLI แยก ไม่ใช่ endpoint POST /auth/token:
// endpoint ที่แจก admin token ให้ใครก็ได้ = ช่องโหว่ที่ใหญ่กว่าการไม่มี auth เสียอีก
// ในระบบจริง token ควรมาจาก identity provider (Keycloak / Auth0 / IdP ขององค์กร)
// เครื่องมือนี้มีไว้ให้ dev ทดสอบ และให้ ops ออก token ชั่วคราวตอนแก้ปัญหาเท่านั้น
package main

import (
	"flag"
	"fmt"
	"os"
	"time"

	"github.com/soya196/go-shop/internal/token"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "❌", err)
		os.Exit(1)
	}
}

func run() error {
	var (
		secret = flag.String("secret", os.Getenv("JWT_SECRET"), "secret สำหรับเซ็น (หรือ env JWT_SECRET)")
		role   = flag.String("role", "customer", "สิทธิ์: admin | customer")
		sub    = flag.String("sub", "", "subject — เช่น customer id")
		ttl    = flag.Duration("ttl", time.Hour, "อายุของ token")
	)
	flag.Parse()

	if *secret == "" {
		return fmt.Errorf("ต้องระบุ -secret หรือ env JWT_SECRET")
	}
	if *sub == "" {
		return fmt.Errorf("ต้องระบุ -sub (เจ้าของ token)")
	}
	r := token.Role(*role)
	if r != token.RoleAdmin && r != token.RoleCustomer {
		return fmt.Errorf("-role ต้องเป็น admin หรือ customer (ได้ %q)", *role)
	}

	issuer, err := token.New(*secret, *ttl)
	if err != nil {
		return err
	}
	signed, err := issuer.Issue(*sub, r, time.Now())
	if err != nil {
		return err
	}
	fmt.Println(signed)
	return nil
}
