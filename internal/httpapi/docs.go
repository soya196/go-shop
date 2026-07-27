package httpapi

import (
	_ "embed"
	"encoding/json"
	"fmt"
	"net/http"
	"sort"
	"strings"
)

// openapiSpec ถูกฝังลงไบนารีตอน build
//
// ทำไมฝัง ไม่อ่านจากดิสก์: deploy ไฟล์เดียวจบ ไม่มีเคส "ลืม copy spec ขึ้น server"
// และ spec ที่เสิร์ฟออกไปตรงกับโค้ดที่กำลังรันเสมอ
//
//go:embed openapi.json
var openapiSpec []byte

// swaggerUI คือหน้า Swagger UI ที่โหลด asset จาก CDN
//
// ⚠️ ต้องต่อเน็ตได้ถึงจะเห็นหน้านี้ · ถ้าอยู่หลัง firewall ให้ใช้ /openapi.json
// ไปเปิดใน editor.swagger.io หรือ import เข้า Postman/Insomnia แทน
const swaggerUI = `<!DOCTYPE html>
<html lang="th">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<title>go-shop API — Swagger UI</title>
<link rel="stylesheet" href="https://unpkg.com/swagger-ui-dist@5.17.14/swagger-ui.css">
<style>
  body{margin:0;background:#fafafa}
  .topbar{display:none}
  #offline{display:none;font:15px/1.7 system-ui,sans-serif;max-width:640px;margin:60px auto;padding:24px;
           border:1px solid #e5e7eb;border-radius:12px;background:#fff}
  #offline code{background:#f1f5f9;padding:2px 6px;border-radius:4px}
</style>
</head>
<body>
<div id="swagger-ui"></div>
<div id="offline">
  <h2>⚠️ โหลด Swagger UI จาก CDN ไม่ได้</h2>
  <p>เครื่องนี้น่าจะออกเน็ตไม่ได้ (เช่นอยู่หลัง firewall) — ตัว API ยังทำงานปกติ</p>
  <p>ใช้ spec ตรงๆ ได้ที่ <a href="/openapi.json"><code>/openapi.json</code></a> แล้วเปิดด้วย:</p>
  <ul>
    <li><code>editor.swagger.io</code> → File → Import URL</li>
    <li>Postman / Insomnia → Import → OpenAPI</li>
    <li><code>curl localhost:8080/openapi.json &gt; api.json</code></li>
  </ul>
</div>
<script src="https://unpkg.com/swagger-ui-dist@5.17.14/swagger-ui-bundle.js"
        onerror="document.getElementById('offline').style.display='block'"></script>
<script>
window.addEventListener('load', function () {
  if (!window.SwaggerUIBundle) { document.getElementById('offline').style.display = 'block'; return; }
  SwaggerUIBundle({
    url: 'openapi.json',
    dom_id: '#swagger-ui',
    deepLinking: true,
    displayRequestDuration: true,
    tryItOutEnabled: true,
    defaultModelsExpandDepth: 1,
    docExpansion: 'list'
  });
});
</script>
</body>
</html>`

// mountDocs ติดตั้ง endpoint เอกสาร
//
// แยกออกจากตาราง routes เพราะไม่ใช่ API ของธุรกิจ — และปิดได้ด้วย config
func (a *API) mountDocs(mux *http.ServeMux) {
	if !a.cfg.DocsEnabled {
		return
	}
	mux.HandleFunc("GET /openapi.json", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		w.Header().Set("Cache-Control", "no-store")
		_, _ = w.Write(openapiSpec)
	})
	mux.HandleFunc("GET /docs", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = w.Write([]byte(swaggerUI))
	})
	// เผื่อคนพิมพ์ /docs/ ติดสแลช
	mux.HandleFunc("GET /docs/", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "/docs", http.StatusMovedPermanently)
	})
	// เปิดหน้าแรกแล้วเจอเอกสารเลย ดีกว่าเจอ 404
	mux.HandleFunc("GET /{$}", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "/docs", http.StatusFound)
	})
}

// SpecPaths คืนรายการ "METHOD /path" ที่มีอยู่ใน openapi.json
//
// ใช้ใน test เพื่อกันเอกสารหลุดจากโค้ด (ดู openapi_test.go)
func (a *API) SpecPaths() ([]string, error) {
	return specPaths(openapiSpec)
}

// RoutePaths คืนรายการ "METHOD /path" ที่ register จริงใน router
func (a *API) RoutePaths() []string {
	rs := a.routes()
	out := make([]string, 0, len(rs))
	for _, r := range rs {
		out = append(out, strings.ToUpper(r.Method)+" "+r.Pattern)
	}
	sort.Strings(out)
	return out
}

// specPaths แกะ "METHOD /path" ออกจาก OpenAPI document
func specPaths(raw []byte) ([]string, error) {
	var doc struct {
		Paths map[string]map[string]json.RawMessage `json:"paths"`
	}
	if err := json.Unmarshal(raw, &doc); err != nil {
		return nil, fmt.Errorf("openapi.json ไม่ใช่ JSON ที่ถูกต้อง: %w", err)
	}
	verbs := map[string]bool{
		"get": true, "post": true, "put": true, "patch": true,
		"delete": true, "head": true, "options": true,
	}
	var out []string
	for p, ops := range doc.Paths {
		for verb := range ops {
			if verbs[strings.ToLower(verb)] {
				out = append(out, strings.ToUpper(verb)+" "+p)
			}
		}
	}
	sort.Strings(out)
	return out, nil
}
