# ⬡ go-shop — Shopping API ตามแนวคิด Clean Architecture (Go)

[![CI](https://github.com/soya196/go-shop/actions/workflows/ci.yml/badge.svg)](https://github.com/soya196/go-shop/actions/workflows/ci.yml)
[![License: MIT](https://img.shields.io/badge/License-MIT-yellow.svg)](LICENSE)
[![Go](https://img.shields.io/badge/Go-1.26-00ADD8?logo=go&logoColor=white)](https://go.dev)

> **A production-ready shopping API in Go** — Clean Architecture where packages split by
> **domain**, not by **layer**, with `archlint` making those rules fail the build.
> Gin + PostgreSQL (sqlc/pgx) + JWT + OpenTelemetry — and a domain layer that
> **has not changed a single line** across every one of those additions.
> *(Documentation below is in Thai.)*

## 🌿 branch นี้คืออะไร

| branch | เป้าหมาย | dependency |
|---|---|---|
| `main` | **พิสูจน์ว่าไม่ต้องมี framework ก็ได้** — stdlib ล้วน | **0** |
| `feat/production-stack` ← *อยู่ตรงนี้* | **เอาไปขึ้น prod ได้จริง** | ~79 module |

ทั้งสอง branch ใช้ **`internal/<domain>/` ชุดเดียวกัน** — เปลี่ยนทั้ง web framework และ
ฐานข้อมูลแล้ว **domain ไม่ถูกแก้เลยแม้แต่บรรทัดเดียว** นั่นคือหลักฐานว่า boundary ใช้ได้จริง
ไม่ใช่แค่คำพูดในสไลด์

### stack ของ branch นี้

| ชั้น | ใช้อะไร | ทำไม |
|---|---|---|
| Web | **gin** v1.12 | binding + validator ติดมาด้วย · ทีมส่วนใหญ่คุ้นเคย |
| DB | **PostgreSQL** + pgx/v5 | transaction จริง · `FOR UPDATE`-style atomic ที่แก้ oversell ได้ |
| Query | **sqlc** v1.31 | เขียน SQL เอง → generate Go ที่ type-safe · **เช็ค SQL กับ schema ตอน generate** |
| Migration | **goose** + `go:embed` | binary กับ schema เดินทางไปด้วยกัน |
| Auth | **JWT** (HS256) | 401/403 แยกชัด · ตาราง route บอกสิทธิ์ในคอลัมน์แรก |
| Observability | **OpenTelemetry** | trace เดียวเห็นตั้งแต่ HTTP ถึง SQL |
| ยาม | **archlint** + golangci-lint + govulncheck | กฎที่ตรวจได้ ต้องทำให้ build แดง |

> 🔑 **สิ่งที่ยังไม่เปลี่ยนเลยคือ `internal/<domain>/`** — gin อยู่ได้เฉพาะใน `httpapi`
> pgx อยู่ได้เฉพาะใน `pgstore` · **archlint บังคับไว้** ลองแทรก `import gin` เข้า
> `internal/order` แล้วรัน `make arch` ดูได้

```bash
make tools      # ติดตั้ง sqlc, goose, golangci-lint, govulncheck (ครั้งเดียว)
make db-up      # เปิด PostgreSQL ด้วย docker compose (พอร์ต 5433)
make migrate    # สร้างตาราง
make run-pg     # รันที่ :8080 ต่อ PostgreSQL
                # → เปิด http://localhost:8080/docs = Swagger UI

make run        # หรือรันแบบ in-memory ไม่ต้องมี DB
make run-trace  # รันพร้อมพิมพ์ trace ลงจอ (เห็น HTTP → SQL ในต้นเดียว)
make check      # ประตูก่อน commit: arch + vet + lint + vuln + test + fmt
make arch       # 💎 ตรวจกฎสถาปัตยกรรมอย่างเดียว
make docker     # build image (archlint + test รันใน image build ด้วย)
```

---

## 🎯 แก่นของโปรเจกต์นี้

> **Clean Architecture บอก *boundary* — ไม่ได้บอก *ชื่อโฟลเดอร์***

ไดอะแกรมวงกลมของ Uncle Bob บอก "ขอบเขต" กับ "ทิศทางการพึ่งพา"
แต่คนจำนวนมากอ่านรูปแล้วตีความว่า **1 วง = 1 โฟลเดอร์** → เลยได้โครงแบบ
`domain/` `usecase/` `port/` `controller/` ซึ่งเป็นการแบ่งตาม **layer**

ปัญหาคือ **ใน Go หน่วยห่อหุ้มคือ `package` ไม่ใช่ `class`**

```go
// order_service.go
package service
func (s *OrderService) validateOrder(o Order) error { /* ... */ }

// user_service.go — package เดียวกัน!
func (s *UserService) doSomething() {
    (&OrderService{}).validateOrder(Order{})   // ← เรียกได้ compiler ไม่ block
}
```

`lowercase` ใน Go = unexported = **package-level ไม่ใช่ file-level**
→ ยัดทุก domain ไว้ package เดียวเมื่อไหร่ กฎธุรกิจของ `order` ก็รั่วให้ `user` ใช้ทันที
โดยไม่มีบรรทัด `import` ให้ใครเห็น

**ทางออก: แบ่ง package ตาม domain** — แล้ว Go compiler จะบังคับ Dependency Rule ให้เอง
(ละเมิด = import cycle = build ไม่ผ่าน)

โปรเจกต์นี้ทำแบบนั้น แล้วเพิ่มสิ่งที่ compiler ทำให้ไม่ได้:
**`cmd/archlint` — เครื่องมือที่ทำให้กฎที่เหลือ fail build เหมือนกัน**

---

## 📁 โครงสร้าง — เปิดมาแล้วรู้ว่าเป็นระบบอะไร (Screaming)

```
internal/
  money/       💰 shared kernel — จำนวนเงินเป็น int64 สตางค์ กัน float rounding
  catalog/     📦 สินค้า + สต็อก (Reserve / Release / Fulfil)
  customer/    👤 ลูกค้า + เพดานออเดอร์ค้าง
  cart/        🛒 ตะกร้า
  order/       📋 คำสั่งซื้อ — state machine ตัวเอก
  payment/     💳 การรับเงิน
  checkout/    🧾 ขั้นตอนจ่ายตังค์ (process ที่พาดผ่าน cart กับ order)
  ─────────────────────── ข้างบน = domain · ข้างล่าง = adapter ───────
  memory/      เก็บในหน่วยความจำ
  jsonstore/   เก็บลงไฟล์ JSON
  httpapi/     HTTP (net/http ล้วน)
  bridge/      ต่อ port ของ domain หนึ่ง เข้ากับ service ของอีก domain
  clock/ uid/ fakepay/
cmd/
  api/         composition root — จุดเดียวที่รู้จักทุกอย่าง
  archlint/    💎 ตัวบังคับกฎสถาปัตยกรรม
```

**ในแต่ละ domain, layer ไม่ได้หายไป — มันย้ายจาก "โฟลเดอร์" มาเป็น "ชื่อไฟล์"**

```
internal/order/
  order.go       ← entity + state machine + ports ที่ตัวเองต้องการ
  service.go     ← use case
  order_test.go
```

---

## 💎 `archlint` — เหตุผลหลักที่โปรเจกต์นี้มีอยู่

กฎ Clean Architecture ทุกข้อ **ตรวจได้จาก import graph** ซึ่งเป็น static ล้วน
ถ้าตรวจได้ ก็ไม่ควรปล่อยให้เป็นแค่ "ข้อตกลงในหัวคน" — เอามาทำให้ build แดงซะ

```bash
$ make arch
⬡ archlint — ตรวจกฎสถาปัตยกรรมจาก import graph
  module     : github.com/soya196/go-shop
  packages   : 7 domain · 7 adapter · 2 composition

✅ ผ่านทุกกฎ
   · domain ไม่รู้จัก adapter / framework / library ภายนอก
   · ไม่มี domain ไหน import domain อื่นตรงๆ
   · ไม่มีชื่อ package กลวง (util/common/model/...)
```

### พิสูจน์แล้วว่ามันจับได้จริง (ไม่ใช่ lint ที่พูดว่า OK ตลอด)

ทดลองแทรกการละเมิด 4 อย่างที่ **compiler ยอมให้ผ่าน**:

| การละเมิด | `go build` | `archlint` |
|---|---|---|
| `order` → `catalog` (ข้าม domain) | ✅ ผ่าน | ❌ `cross-domain-import` |
| `order` → `clock` (domain → adapter) | ✅ ผ่าน | ❌ `domain-imports-adapter` |
| `order` → `net/http` | ✅ ผ่าน | ❌ `domain-imports-infrastructure` |
| สร้าง package ชื่อ `utils` | ✅ ผ่าน | ❌ `banned-package-name` |

> **หมายเหตุที่น่าสนใจ**: ตอนทดลองให้ `order` import `memory` **Go compiler ดักได้เอง**
> ด้วย `import cycle not allowed` — Go บังคับทิศทางการพึ่งพาให้เองโดยธรรมชาติ
> `archlint` มีไว้ปิดช่องที่เหลือ ซึ่งเป็นช่องที่ compiler ช่วยไม่ได้

กฎทั้งหมดอยู่ใน [`arch.json`](arch.json) — เพิ่ม package ใหม่ต้องประกาศ layer ไม่งั้น fail

---

## 🔗 domain คุยกันยังไงโดยไม่รู้จักกัน

คำถามที่ตำราส่วนใหญ่ข้ามไป คำตอบของโปรเจกต์นี้: **ไม่มี domain ไหน import domain อื่น**

แต่ละ domain ประกาศ **port ของตัวเอง** ว่าต้องการความสามารถอะไร:

```go
// internal/order/order.go — order บอกแค่ว่า "ฉันต้องการคนจองของให้"
type Stock interface {
    Reserve(ctx context.Context, productID string, qty int) error
    Release(ctx context.Context, productID string, qty int) error
    Fulfil(ctx context.Context, productID string, qty int) error
}
```

แล้วมี **package เดียวในระบบ** ที่รู้จักทั้งสองฝั่งและต่อท่อให้:

```go
// internal/bridge/bridge.go
type OrderStock struct{ Catalog *catalog.Service }

var _ order.Stock = OrderStock{}   // compiler ตรวจให้ว่าต่อถูก

func (b OrderStock) Reserve(ctx context.Context, id string, qty int) error {
    return b.Catalog.Reserve(ctx, id, qty)
}
```

**ผลที่ได้**: วันที่อยากแยก `order` ออกไปเป็น microservice → ลบ bridge ตัวนั้น เขียน HTTP client แทน
โดย `internal/order` ไม่ต้องแก้เลยแม้แต่บรรทัดเดียว

ตัวอย่างที่สุดขั้วกว่านั้นคือ `cart` — มันประกาศ **type ของตัวเอง** ไม่ใช้ `catalog.Product` ด้วยซ้ำ:

```go
// internal/cart/cart.go
type ProductInfo struct {           // มุมมองของ cart ต่อสินค้า
    ID, Name string
    Price    money.Satang
    Sellable bool                   // cart ไม่สนใจ stock/reserved/sku
}
type Catalog interface {
    Lookup(ctx context.Context, productID string) (ProductInfo, error)
}
```

พูดอีกแบบ: **มันคุยผ่าน interface — มันไม่รู้จักของจริงด้วยซ้ำ**

---

## ✅ เทส 3 ข้อ — ผ่านจริง ไม่ใช่แค่อ้าง

เกณฑ์ง่ายๆ ที่ใช้พิสูจน์ว่าโค้ด "เป็น Clean Architecture จริงหรือยัง"

### 1. เปลี่ยนชนิด database แล้วต้องแก้ use case ไหม?

```bash
go run ./cmd/api                  # memory
go run ./cmd/api -store=json      # ไฟล์ JSON
```

สลับที่เก็บข้อมูลทั้งระบบ **ที่ `cmd/api/main.go` จุดเดียว** — ทดสอบจริงแล้ว:
สั่งซื้อผ่าน `-store=json` → ปิดเซิร์ฟเวอร์ → เปิดใหม่ → ออเดอร์ยังอยู่ ✅
`internal/catalog`, `internal/order` ฯลฯ **ไม่ถูกแก้แม้แต่ตัวอักษรเดียว**

### 2. เปลี่ยน framework แล้วต้องแก้ use case ไหม?

`internal/httpapi` เป็น package เดียวที่รู้จัก HTTP · domain ไม่มีคำว่า `net/http` เลย
(และ `archlint` บังคับข้อนี้อยู่ — ลอง import ดูแล้ว build แดง)

จะย้ายไป gin/echo/gRPC = เขียน adapter ใหม่ 1 package จบ

### 3. แก้ business rule แล้วแก้ที่เดียวจบไหม?

อยากเปลี่ยนกฎ "ยกเลิกออเดอร์ได้ถึงตอนไหน" → แก้ที่ **ตารางเดียว**:

```go
// internal/order/order.go
var transitions = map[Status][]Status{
    Placed:    {Paid, Cancelled},
    Paid:      {Preparing, Cancelled},
    Preparing: {Shipped, Cancelled},
    Shipped:   {Delivered},        // ← ส่งของแล้วยกเลิกไม่ได้
    Delivered: {},
    Cancelled: {},
}
```

ไม่มี `if status == ...` กระจายอยู่ใน handler หรือ service เลย

---

## 🧪 Test

```
ok  internal/cart       ok  internal/catalog    ok  internal/checkout
ok  internal/customer   ok  internal/httpapi    ok  internal/money
ok  internal/order      ok  internal/payment
```

**Domain test ไม่ต้องมี DB ไม่ต้องมี mock library** — fake เขียนเองใน `_test.go` ไม่กี่บรรทัด
เพราะ domain รับแต่ interface ที่ตัวเองประกาศ นี่คือเสา "Easy to Test" ที่จับต้องได้

`internal/httpapi/httpapi_test.go` เป็น integration test ที่เดินครบ flow จริงผ่าน `httptest`:
เพิ่มสินค้า → สมัครลูกค้า → หยิบใส่ตะกร้า → checkout → จ่ายเงิน → prepare → ship → deliver
พร้อมตรวจว่าสต็อกถูก **จอง** ตอนสั่ง และถูก **ตัดจริง** ตอนส่ง

---

## 🏗️ Infrastructure — มีอะไรแล้ว ยังขาดอะไร

| ด้าน | สถานะ | รายละเอียด |
|---|---|---|
| **Swagger / OpenAPI** | ✅ | `/docs` (Swagger UI) + `/openapi.json` (3.0.3, 21 path, 11 schema) ฝังในไบนารีด้วย `go:embed` · ปิดได้ด้วย `-docs=false` |
| **กัน spec หลุดจากโค้ด** | ✅ | test เทียบ route table กับ spec — เพิ่ม endpoint แล้วลืมเขียน spec = **test แดง** |
| **Structured logging** | ✅ | `log/slog` · `-log-format=text\|json` · `-log-level` · level แยกตาม status (5xx=Error, 4xx=Warn) |
| **Request ID / correlation** | ✅ | `X-Request-Id` — รับต่อจาก gateway ถ้ามี, ไม่มีก็สร้าง · ตอบกลับใน header · ผูกทุกบรรทัด log |
| **Health / readiness** | ✅ | `/healthz` (liveness, ไม่เช็ค dependency โดยตั้งใจ) · `/readyz` (พร้อมรับ traffic + version) |
| **Graceful shutdown** | ✅ | SIGTERM → `/readyz` ตอบ 503 → รอ `-drain` (3s) ให้ LB ถอดออก → ค่อยปิด |
| **Panic recovery** | ✅ | เก็บ stack trace ลง log · ไม่ส่งออกให้ client |
| **CORS** | ✅ | `-cors-origins` · **ปิดสนิทเป็นค่าเริ่มต้น** · รองรับ preflight + `Vary: Origin` |
| **Config ผ่าน env** | ✅ | 12-factor — `SHOP_ADDR` `SHOP_STORE` `SHOP_LOG_FORMAT` `SHOP_LOG_LEVEL` `SHOP_DOCS` `SHOP_CORS_ORIGINS` (flag ทับ env ได้) |
| **Timeouts** | ✅ | ReadHeader 5s · Read/Write 15s · Idle 60s · body cap 1 MiB |
| **Container** | ✅ | Dockerfile 2 stage → distroless nonroot · **archlint + test รันตอน build image** |
| **CI** | ✅ | GitHub Actions: fmt → vet → **archlint** → test `-race` → build |
| **Version stamping** | ✅ | `-ldflags -X main.version=$(git rev-parse --short HEAD)` โผล่ใน `/readyz` |
| **Auth / RBAC** | ❌ | endpoint หลังร้าน (เพิ่มสินค้า, ship) ยังใครก็เรียกได้ |
| **Rate limiting** | ❌ | ยังไม่มี |
| **Metrics (Prometheus)** | ❌ | ยังไม่มี `/metrics` — จะทำต้องรับ dependency ตัวแรกของโปรเจกต์ |
| **Tracing (OTel)** | ❌ | มี request id แล้ว แต่ยังไม่มี distributed trace |
| **Pagination** | ❌ | list endpoint คืนทั้งหมด |
| **Concurrency บนสต็อก** | ⚠️ | `Reserve` เป็น read-modify-write ยังไม่มี lock/version → ยิงพร้อมกันแรงๆ มีโอกาส oversell |

### ⚠️ ที่ยังไม่ได้ verify บนเครื่อง Windows
- `go test -race` — ต้องมี gcc (`CGO_ENABLED=1`) เครื่อง dev ไม่มี · **CI บน ubuntu รันให้**
- **SIGTERM → graceful drain** — Git Bash ส่งสัญญาณให้ native Windows process ไม่ได้
  · ส่วนที่ verify แล้วคือ state transition (`SetReady(false)` → `/readyz` = 503) ผ่าน unit test

---

## 📖 Swagger

```bash
make run
# แล้วเปิด http://localhost:8080/docs   (เข้า / เฉยๆ ก็ redirect มาที่นี่)
```

**spec ตรงกับโค้ดเสมอ** — ไม่ได้ใช้ swaggo/annotation แต่ใช้วิธีที่ตรวจสอบได้:

```go
// internal/httpapi/httpapi.go — route table เป็นความจริงชุดเดียว
{"POST", "/orders/{id}/ship", "ส่งของ (PREPARING → SHIPPED)", a.shipOrder},
```
```go
// internal/httpapi/openapi_test.go — เทียบ route table กับ openapi.json
// เพิ่ม endpoint แล้วลืมเขียน spec → test แดง
// เขียน spec ให้ endpoint ที่ไม่มีจริง → test แดงเหมือนกัน ("เอกสารโกหก")
```

ทดลองแล้ว: เพิ่ม `GET /orders/{id}/invoice` เข้า route table โดยไม่แตะ spec →
```
--- FAIL: TestSpecCoversEveryRoute
    endpoint เหล่านี้มีในโค้ดแต่ไม่มีใน openapi.json:
      GET /orders/{id}/invoice
```

มี test ตรวจ `$ref` ทุกตัวว่า resolve ได้จริงด้วย — กัน typo ที่ทำให้ Swagger UI พังตอนเปิด

> ⚠️ **Swagger UI โหลด asset จาก CDN (unpkg)** — ถ้าอยู่หลัง firewall หน้าจะขึ้นคำแนะนำให้เอา
> `/openapi.json` ไปเปิดใน `editor.swagger.io` หรือ import เข้า Postman แทน · ตัว API ไม่กระทบ

---

## 🌐 API

> รายละเอียดครบ (request/response schema, ตัวอย่าง, รหัส error) ดูที่ **`/docs`**

| Method | Path | ทำอะไร |
|---|---|---|
| GET | `/healthz` `/readyz` | liveness / readiness |
| GET | `/docs` `/openapi.json` | Swagger UI / OpenAPI spec |
| GET | `/products` | ดูสินค้า (`?all=true` = รวมที่ปิดขาย) |
| POST | `/products` | เพิ่มสินค้า |
| GET | `/products/{id}` | ดูสินค้ารายตัว |
| POST | `/products/{id}/restock` | เติมสต็อก |
| DELETE | `/products/{id}` | ปิดการขาย |
| GET/POST | `/customers` | ดู/สมัครลูกค้า |
| GET | `/customers/{id}/orders` | ออเดอร์ของลูกค้า |
| POST | `/carts` | เปิดตะกร้า |
| GET | `/carts/{id}` | ดูตะกร้า |
| POST | `/carts/{id}/items` | หยิบของใส่ |
| PATCH/DELETE | `/carts/{id}/items/{productID}` | ปรับจำนวน / เอาออก |
| POST | `/carts/{id}/checkout` | 🧾 ตะกร้า → ออเดอร์ |
| GET | `/orders` | รายการออเดอร์ (`?status=PAID`) |
| POST | `/orders/{id}/pay` `prepare` `ship` `deliver` `cancel` | เดินสถานะ |
| GET | `/orders/{id}/payments` | ประวัติการชำระเงิน |

### ตัวอย่างเดินครบ flow

```bash
make run &

CUS=$(curl -s -XPOST localhost:8080/customers \
      -d '{"name":"สนธยา","email":"s@example.com"}' | jq -r .id)
PID=$(curl -s localhost:8080/products | jq -r '.products[0].id')
CART=$(curl -s -XPOST localhost:8080/carts -d "{\"customer_id\":\"$CUS\"}" | jq -r .id)

curl -s -XPOST localhost:8080/carts/$CART/items -d "{\"product_id\":\"$PID\",\"qty\":2}"
ORD=$(curl -s -XPOST localhost:8080/carts/$CART/checkout -d '{"pay_now":true}' | jq -r .order_id)

curl -s -XPOST localhost:8080/orders/$ORD/prepare
curl -s -XPOST localhost:8080/orders/$ORD/ship -d '{"tracking":"TH-0001"}'
curl -s -XPOST localhost:8080/orders/$ORD/deliver
```

ลองข้ามขั้นดู — จะได้ `409` พร้อมเหตุผลจาก domain ตรงๆ:

```
HTTP 409  order: invalid status transition: PAID → SHIPPED
```

---

## 📐 กติกาที่ยึดตลอดโปรเจกต์

| กฎ | ทำไม |
|---|---|
| **entity ถือกฎธุรกิจของตัวเอง** | `Product.Reserve()` ตรวจสต็อกเอง · service แค่จัดลำดับ ไม่ตัดสินใจแทน |
| **interface อยู่ที่ผู้ใช้ (caller)** | ไม่มี package `port/` กลาง · `order.Repository` อยู่ในไฟล์เดียวกับ entity |
| **`catalog.New()` ไม่ใช่ `NewProduct()`** | กัน stutter ตาม Effective Go |
| **เงินเป็น `int64` สตางค์** | `0.1 + 0.2 != 0.3` ในโลก float — มี test พิสูจน์ |
| **`time.Now()` / `rand` เป็น port** | side effect ต้องฉีดเข้ามา ไม่งั้น test ไม่ deterministic |
| **DI = เรียก constructor ธรรมดา** | Go ไม่มี annotation และเราไม่เลียนแบบ Spring · ไม่มี reflection ไม่มี magic |
| **adapter ห้ามมีกฎธุรกิจ** | handler มีหน้าที่ decode → เรียก service → encode เท่านั้น |
| **error mapping อยู่ที่ adapter** | domain ไม่รู้จักเลข 404/409 |

---

## 🚧 ยังไม่ได้ทำ (จงใจ — ไม่ใช่ลืม)

branch นี้ปิดหนี้ก้อนใหญ่ไปหลายอันแล้ว สิ่งที่เหลือคือ:

| ยังไม่มี | ทำไมถึงยังไม่ทำ |
|---|---|
| **Rate limiting** | ปกติทำที่ gateway/ingress ไม่ใช่ในแอป · ถ้าต้องทำในแอปควรใช้ redis ไม่ใช่ in-memory (หลาย pod) |
| **Idempotency key** | จำเป็นจริงตอนมี client retry อัตโนมัติ · ต้องออกแบบ storage + TTL ให้ดีก่อน |
| **Outbox / event** | ยังไม่มี service อื่นที่ต้องรู้ว่าเกิดออเดอร์ · ทำก่อนมีคนใช้ = เดา |
| **Metrics (Prometheus)** | มี trace แล้ว · metric ควรมาพร้อมกับที่ที่จะเอาไปดู (Grafana) |
| **`-store=memory/json` ไม่มี transaction** | ตั้งใจ — เป็น store สำหรับ dev เท่านั้น · `noTx` มี comment อธิบายไว้ชัด และมีเทสยืนยันว่าเกิดอะไรขึ้น |
| **refresh token / logout** | token อายุสั้น + IdP ภายนอกจัดการเรื่องนี้ดีกว่าที่เราจะเขียนเอง |

### ✅ หนี้ที่ปิดไปแล้วใน branch นี้

| เคยเป็นปัญหา | ปิดยังไง |
|---|---|
| **oversell** — `Reserve` เป็น read-modify-write | ย้ายเงื่อนไขเข้าไปใน `UPDATE` เดียว · เทสยิง 100 goroutine พร้อมกันบนสต็อก 10 → สำเร็จ 10 พอดี |
| **ไม่มี transaction** — `Place` ใช้ compensating action | port `order.TxManager` · เทสพิสูจน์ว่าถ้าไม่มี transaction จะเหลือ "ออเดอร์ผี" |
| **ไม่มี auth** | JWT + สิทธิ์ตาม role · 11 เทสคุมไว้ (รวมช่องโหว่ `alg=none`) |
| **ไม่มี distributed trace** | OpenTelemetry ที่ขอบทั้งสองด้าน |
| **ไม่มี linter / CVE check** | golangci-lint 17 ตัว + govulncheck ใน CI (ครั้งแรกเจอ CVE ที่โค้ดเราเรียกถึงจริง 12 ตัว) |
| **ไฟล์ข้อมูลเป็น 0644** | → `0600` (gosec จับได้) |

## 📄 แผนที่โค้ดแบบ interactive

เปิด [`docs/architecture.html`](docs/architecture.html) ด้วย browser ได้เลย — ไฟล์เดียว ไม่ต้อง build

| ส่วน | ตอบคำถามอะไร |
|---|---|
| 🔄 อะไรเปลี่ยน อะไรไม่เปลี่ยน | เปลี่ยนทั้ง framework และ DB แล้ว domain ต้องแก้กี่บรรทัด |
| 📁 แผนที่ไฟล์ | คลิกดูทีละไฟล์ว่าอยู่ชั้นไหน ทำอะไร **ทำไมต้องอยู่ตรงนั้น** (41 รายการ) |
| 🚦 เส้นทาง request | กดเดินทีละขั้น HTTP → ตรวจสิทธิ์ → use case → entity → SQL |
| 🧅 ชั้น middleware | request ผ่านอะไรก่อนถึง handler |
| 🔐 สิทธิ์ 3 ระดับ | endpoint ไหนเปิดสาธารณะ ไหนต้องล็อกอิน ไหนเฉพาะแอดมิน |
| 🚨 error → HTTP status | domain error กลายเป็นเลขอะไร และทำไม |
| 🛡️ กฎที่ archlint บังคับ | กฎไหนที่ `go build` จับไม่ได้ |

> เอกสารเวอร์ชัน zero-dependency อยู่บน branch `main`

## 🔗 อ่านต่อ

- **Clean Architecture** · **Screaming Architecture** — Robert C. Martin (Uncle Bob)
- **Hexagonal Architecture (Ports & Adapters)** — Alistair Cockburn
- **Onion Architecture** — Jeffrey Palermo
- [Organizing a Go Module](https://go.dev/doc/modules/layout) — คำแนะนำทางการของ Go เอง (เริ่มจากไฟล์เดียว อย่าแยก package พร่ำเพรื่อ)
- [Effective Go](https://go.dev/doc/effective_go) · [Go Code Review Comments](https://go.dev/wiki/CodeReviewComments) — โดยเฉพาะหัวข้อ *interface อยู่ที่ผู้ใช้ ไม่ใช่ผู้สร้าง*

---

เขียนร่วมกับ **Claude (AI)** · 2026

PRs / issues ยินดีครับ — โดยเฉพาะเรื่อง concurrency บนสต็อกที่ยังเป็นหนี้อยู่
