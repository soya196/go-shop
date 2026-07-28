.PHONY: help run run-json run-pg docs test test-v cover race arch check build docker docker-run clean \
        tools db-up db-down db-logs migrate migrate-status migrate-reset generate lint vuln run-trace token

BIN     := bin/api
VERSION := $(shell git rev-parse --short HEAD 2>/dev/null || echo dev)
IMAGE   := go-shop:$(VERSION)
DSN     ?= postgres://shop:shop@localhost:5433/shop?sslmode=disable

help:                       ## แสดงคำสั่งทั้งหมด
	@grep -E '^[a-zA-Z_-]+:.*?## .*$$' $(MAKEFILE_LIST) | awk 'BEGIN{FS=":.*?## "};{printf "  \033[36m%-14s\033[0m %s\n",$$1,$$2}'

# ─────────────────────── ฐานข้อมูล ───────────────────────

tools:                      ## ติดตั้งเครื่องมือที่ต้องใช้ (sqlc, goose, golangci-lint, govulncheck)
	go install github.com/sqlc-dev/sqlc/cmd/sqlc@latest
	go install github.com/pressly/goose/v3/cmd/goose@latest
	go install github.com/golangci/golangci-lint/v2/cmd/golangci-lint@latest
	go install golang.org/x/vuln/cmd/govulncheck@latest

db-up:                      ## เปิด PostgreSQL ด้วย docker compose (พอร์ต 5433)
	docker compose up -d db
	@echo "⏳ รอ postgres พร้อม..."
	@until docker compose exec -T db pg_isready -U shop -d shop >/dev/null 2>&1; do sleep 1; done
	@echo "✅ พร้อมแล้ว — $(DSN)"

db-down:                    ## ปิด PostgreSQL (ข้อมูลยังอยู่ใน volume)
	docker compose down

db-logs:                    ## ดู log ของ postgres
	docker compose logs -f db

migrate:                    ## รัน migration ขึ้นเวอร์ชันล่าสุด
	go run ./cmd/migrate -dsn "$(DSN)" up

migrate-status:             ## ดูว่า migration รันไปถึงไหนแล้ว
	go run ./cmd/migrate -dsn "$(DSN)" status

migrate-reset:              ## ⚠️ ถอย migration ทั้งหมด (dev เท่านั้น)
	go run ./cmd/migrate -dsn "$(DSN)" reset

generate:                   ## 🧬 สร้างโค้ดจาก SQL ด้วย sqlc (ต้องรันทุกครั้งที่แก้ .sql)
	sqlc generate
	@echo "✅ generate เสร็จ → internal/pgstore/gen (ห้ามแก้ไฟล์ในนั้นด้วยมือ)"

# ─────────────────────── รันแอป ───────────────────────

run:                        ## รัน API (เก็บใน memory)
	go run ./cmd/api

run-json:                   ## รัน API (เก็บลงไฟล์ JSON ใน ./data)
	go run ./cmd/api -store=json -data=./data

run-pg:                     ## รัน API ต่อ PostgreSQL (ต้อง make db-up + make migrate ก่อน)
	go run ./cmd/api -store=postgres -dsn "$(DSN)"

run-trace:                  ## รัน API พร้อม tracing แบบพิมพ์ span ลงจอ (ดู HTTP -> SQL ในต้นเดียว)
	go run ./cmd/api -store=postgres -dsn "$(DSN)" -trace=stdout

token:                      ## ออก JWT สำหรับทดสอบ · ใช้: make token ROLE=admin SUB=adm_1
	@go run ./cmd/token -secret "$(JWT_SECRET)" -role "$(or $(ROLE),customer)" -sub "$(or $(SUB),test_1)"

docs:                       ## เปิด Swagger UI ในเบราว์เซอร์ (ต้องรัน make run ค้างไว้ก่อน)
	@echo "→ http://localhost:8080/docs"
	@command -v xdg-open >/dev/null && xdg-open http://localhost:8080/docs || \
	 command -v start    >/dev/null && start   http://localhost:8080/docs || true

test:                       ## รัน test ทั้งหมด
	go test ./...

test-v:                     ## รัน test แบบละเอียด
	go test -v ./...

cover:                      ## รัน test พร้อมวัด coverage
	go test -cover ./...

race:                       ## รัน test พร้อม race detector (ต้องมี gcc — Windows ส่วนใหญ่ไม่มี)
	CGO_ENABLED=1 go test -race ./...

arch:                       ## 💎 ตรวจกฎสถาปัตยกรรมจาก import graph
	go run ./cmd/archlint

lint:                       ## golangci-lint (ต้อง make tools ก่อน)
	golangci-lint run ./...

vuln:                       ## govulncheck — เช็ค CVE ของ dependency + stdlib
	govulncheck ./...

check: arch                 ## ประตูก่อน commit: arch + vet + lint + vuln + test + fmt
	go vet ./...
	golangci-lint run ./...
	govulncheck ./...
	go test ./...
	@test -z "$$(gofmt -l . )" || (echo "gofmt: ไฟล์ต่อไปนี้ยังไม่ format:"; gofmt -l .; exit 1)
	@echo "✅ ผ่านทุกด่าน"

build:                      ## build binary (ฝัง version จาก git)
	go build -ldflags "-s -w -X main.version=$(VERSION)" -o $(BIN) ./cmd/api

docker:                     ## build docker image (archlint+test รันใน image build ด้วย)
	docker build --build-arg VERSION=$(VERSION) -t $(IMAGE) .

docker-run:                 ## รัน container ที่ build ไว้
	docker run --rm -p 8080:8080 $(IMAGE)

clean:                      ## ลบของที่ build + ข้อมูลตัวอย่าง
	rm -rf bin data
