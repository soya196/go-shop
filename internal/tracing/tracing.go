// Package tracing ตั้งค่า OpenTelemetry ให้ทั้งระบบ
//
// เป็น adapter ล้วน — ไม่มี domain ไหนรู้จัก package นี้
//
// # ทำไม trace ถึงสำคัญกว่า log ตอนระบบโต
//
// log บอกว่า "ตรงนี้เกิดอะไร" แต่ตอบไม่ได้ว่า "request เดียวกันไปไหนมาบ้าง
// และเสียเวลาตรงไหนมากที่สุด" · trace ผูกทุกขั้นของ request หนึ่งเข้าด้วยกัน
// เป็นต้นไม้ที่มองเห็นทั้งเส้นทาง HTTP → use case → SQL พร้อมเวลาแต่ละช่วง
//
// เราไม่ได้ใส่ span เองในโค้ด domain แม้แต่ที่เดียว — ได้มาจาก instrumentation
// ที่ขอบทั้งสองด้าน (otelgin ฝั่งขาเข้า, otelpgx ฝั่งขาออก) ซึ่งเป็นเรื่องของ adapter
package tracing

import (
	"context"
	"fmt"
	"os"
	"time"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracehttp"
	"go.opentelemetry.io/otel/exporters/stdout/stdouttrace"
	"go.opentelemetry.io/otel/propagation"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	semconv "go.opentelemetry.io/otel/semconv/v1.41.0"
)

// Config คือค่าตั้งของ tracing
type Config struct {
	// Exporter: "none" (ปิด) | "stdout" (พิมพ์ลงจอ — ใช้ตอน dev) | "otlp" (ส่งเข้า collector)
	Exporter string
	// Endpoint ของ OTLP collector เช่น "localhost:4318" (ใช้เมื่อ Exporter=otlp)
	Endpoint string
	// ServiceName ชื่อที่จะโผล่ในระบบ trace
	ServiceName string
	// Version ของ build
	Version string
	// SampleRatio 0.0–1.0 · production ที่ traffic สูงควรต่ำกว่า 1
	SampleRatio float64
}

// Setup ตั้งค่า global tracer แล้วคืนฟังก์ชัน shutdown
//
// คืน no-op shutdown เมื่อปิด tracing — ผู้เรียกไม่ต้องเช็ค nil
func Setup(ctx context.Context, cfg Config) (shutdown func(context.Context) error, err error) {
	noop := func(context.Context) error { return nil }

	if cfg.Exporter == "" || cfg.Exporter == "none" {
		return noop, nil
	}

	exp, err := newExporter(ctx, cfg)
	if err != nil {
		return noop, err
	}

	res, err := resource.Merge(resource.Default(), resource.NewWithAttributes(
		semconv.SchemaURL,
		semconv.ServiceName(cfg.ServiceName),
		semconv.ServiceVersion(cfg.Version),
	))
	if err != nil {
		return noop, fmt.Errorf("tracing: สร้าง resource ไม่สำเร็จ: %w", err)
	}

	ratio := cfg.SampleRatio
	if ratio <= 0 || ratio > 1 {
		ratio = 1
	}

	tp := sdktrace.NewTracerProvider(
		sdktrace.WithBatcher(exp, sdktrace.WithBatchTimeout(2*time.Second)),
		sdktrace.WithResource(res),
		// ParentBased: ถ้า request ต้นทางตัดสินใจ sample มาแล้ว เราตามเขา
		// ไม่งั้น trace จะขาดตอนกลางทางเมื่อมีหลาย service
		sdktrace.WithSampler(sdktrace.ParentBased(sdktrace.TraceIDRatioBased(ratio))),
	)
	otel.SetTracerProvider(tp)

	// propagator บอกวิธีส่ง trace context ข้าม service ผ่าน HTTP header
	// (traceparent ตามมาตรฐาน W3C + baggage)
	otel.SetTextMapPropagator(propagation.NewCompositeTextMapPropagator(
		propagation.TraceContext{}, propagation.Baggage{},
	))

	return tp.Shutdown, nil
}

func newExporter(ctx context.Context, cfg Config) (sdktrace.SpanExporter, error) {
	switch cfg.Exporter {
	case "stdout":
		// ใช้ตอน dev — เห็น span ทันทีโดยไม่ต้องตั้ง collector
		return stdouttrace.New(stdouttrace.WithWriter(os.Stdout), stdouttrace.WithPrettyPrint())
	case "otlp":
		endpoint := cfg.Endpoint
		if endpoint == "" {
			endpoint = "localhost:4318"
		}
		// insecure เพราะปกติ collector อยู่ใน cluster เดียวกัน
		// ถ้าส่งข้ามเน็ตต้องเปลี่ยนเป็น TLS
		return otlptracehttp.New(ctx,
			otlptracehttp.WithEndpoint(endpoint),
			otlptracehttp.WithInsecure(),
		)
	default:
		return nil, fmt.Errorf("tracing: ไม่รู้จัก exporter %q (none|stdout|otlp)", cfg.Exporter)
	}
}
