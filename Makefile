.PHONY: help run run-json docs test test-v cover race arch check build docker docker-run clean

BIN     := bin/api
VERSION := $(shell git rev-parse --short HEAD 2>/dev/null || echo dev)
IMAGE   := go-shop:$(VERSION)

help:                       ## แสดงคำสั่งทั้งหมด
	@grep -E '^[a-zA-Z_-]+:.*?## .*$$' $(MAKEFILE_LIST) | awk 'BEGIN{FS=":.*?## "};{printf "  \033[36m%-12s\033[0m %s\n",$$1,$$2}'

run:                        ## รัน API (เก็บใน memory)
	go run ./cmd/api

run-json:                   ## รัน API (เก็บลงไฟล์ JSON ใน ./data)
	go run ./cmd/api -store=json -data=./data

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

check: arch                 ## ประตูก่อน commit: arch + vet + test + fmt
	go vet ./...
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
