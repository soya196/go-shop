# ─────────────────────────── build ───────────────────────────
FROM golang:1.26-alpine AS build

WORKDIR /src

# ไม่มี dependency ภายนอกเลย แต่ยัง copy go.mod ก่อนเพื่อรักษา layer cache
COPY go.mod ./
RUN go mod download

COPY . .

# ประตูคุณภาพอยู่ใน image build ด้วย — archlint + vet + test ต้องผ่านก่อนถึงจะได้ binary
# ถ้าใครแอบ import ผิดชั้น จะ build image ไม่ผ่าน ไม่ใช่แค่ CI แดง
RUN go vet ./... \
 && go run ./cmd/archlint \
 && go test ./...

ARG VERSION=dev
RUN CGO_ENABLED=0 GOOS=linux go build \
      -trimpath \
      -ldflags="-s -w -X main.version=${VERSION}" \
      -o /out/api ./cmd/api

# ─────────────────────────── runtime ───────────────────────────
FROM gcr.io/distroless/static-debian12:nonroot

# distroless = ไม่มี shell ไม่มี package manager → ผิวสัมผัสการโจมตีเล็กที่สุด
# nonroot = ไม่รันด้วย root (uid 65532)

COPY --from=build /out/api /api

# โฟลเดอร์ข้อมูลตอนใช้ -store=json (production จริงควรใช้ DB แทน)
WORKDIR /data

EXPOSE 8080
USER nonroot:nonroot

ENV SHOP_ADDR=:8080 \
    SHOP_LOG_FORMAT=json \
    SHOP_LOG_LEVEL=info \
    SHOP_STORE=memory \
    SHOP_DOCS=true

ENTRYPOINT ["/api"]
