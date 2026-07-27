package httpapi_test

import (
	"encoding/json"
	"io"
	"net/http"
	"slices"
	"strings"
	"testing"
)

// 🔑 test ชุดนี้แก้ปัญหาที่เอกสาร API ทุกที่เจอ: **spec หลุดจากโค้ด**
//
// แนวคิดเดียวกับ archlint — ถ้าตรวจได้ ก็อย่าปล่อยให้เป็นแค่ความหวังว่าคนจะจำอัปเดต
// เพิ่ม endpoint แล้วลืมเขียน spec → test แดงทันที

func TestSpecCoversEveryRoute(t *testing.T) {
	api := newAPI(t)

	inSpec, err := api.SpecPaths()
	if err != nil {
		t.Fatal(err)
	}
	inCode := api.RoutePaths()

	var missing []string
	for _, r := range inCode {
		if !slices.Contains(inSpec, r) {
			missing = append(missing, r)
		}
	}
	if len(missing) > 0 {
		t.Errorf("endpoint เหล่านี้มีในโค้ดแต่ไม่มีใน openapi.json:\n  %s",
			strings.Join(missing, "\n  "))
	}

	var extra []string
	for _, s := range inSpec {
		if !slices.Contains(inCode, s) {
			extra = append(extra, s)
		}
	}
	if len(extra) > 0 {
		t.Errorf("endpoint เหล่านี้มีใน openapi.json แต่ไม่มีในโค้ด (เอกสารโกหก):\n  %s",
			strings.Join(extra, "\n  "))
	}
}

func TestSpecIsValidEnoughToLoad(t *testing.T) {
	api := newAPI(t)
	raw := fetchSpec(t)

	var doc map[string]any
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatalf("openapi.json parse ไม่ผ่าน: %v", err)
	}
	if v, _ := doc["openapi"].(string); !strings.HasPrefix(v, "3.") {
		t.Errorf("openapi version = %q, want 3.x", v)
	}
	info, ok := doc["info"].(map[string]any)
	if !ok || info["title"] == "" || info["version"] == "" {
		t.Errorf("info ต้องมี title + version, got %v", doc["info"])
	}
	paths, ok := doc["paths"].(map[string]any)
	if !ok || len(paths) == 0 {
		t.Fatal("ไม่มี paths ใน spec")
	}
	if n := len(api.RoutePaths()); len(paths) < 10 {
		t.Errorf("paths = %d เส้น ดูน้อยเกินไปเทียบกับ %d route", len(paths), n)
	}
}

// ทุก $ref ที่อ้างถึงต้องมีอยู่จริง — กัน typo ที่ทำให้ Swagger UI พังตอนเปิด
func TestAllRefsResolve(t *testing.T) {
	raw := fetchSpec(t)
	var doc map[string]any
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatal(err)
	}

	var refs []string
	collectRefs(doc, &refs)
	if len(refs) == 0 {
		t.Fatal("ไม่เจอ $ref เลย — ผิดปกติ")
	}

	seen := map[string]bool{}
	for _, ref := range refs {
		if seen[ref] {
			continue
		}
		seen[ref] = true
		if !strings.HasPrefix(ref, "#/") {
			t.Errorf("$ref ภายนอกไม่รองรับ: %s", ref)
			continue
		}
		if resolve(doc, strings.Split(strings.TrimPrefix(ref, "#/"), "/")) == nil {
			t.Errorf("$ref ชี้ไปที่ไม่มีอยู่จริง: %s", ref)
		}
	}
	t.Logf("ตรวจ $ref แล้ว %d รายการ (ไม่ซ้ำ)", len(seen))
}

func collectRefs(v any, out *[]string) {
	switch x := v.(type) {
	case map[string]any:
		for k, val := range x {
			if k == "$ref" {
				if s, ok := val.(string); ok {
					*out = append(*out, s)
				}
				continue
			}
			collectRefs(val, out)
		}
	case []any:
		for _, item := range x {
			collectRefs(item, out)
		}
	}
}

func resolve(doc any, parts []string) any {
	cur := doc
	for _, p := range parts {
		m, ok := cur.(map[string]any)
		if !ok {
			return nil
		}
		cur, ok = m[p]
		if !ok {
			return nil
		}
	}
	return cur
}

// ───────────────────── docs endpoints ─────────────────────

func TestDocsEndpointsServe(t *testing.T) {
	srv := newServer(t)

	resp, err := srv.Client().Get(srv.URL + "/openapi.json")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("/openapi.json status = %d", resp.StatusCode)
	}
	if ct := resp.Header.Get("Content-Type"); !strings.Contains(ct, "json") {
		t.Errorf("content-type = %q", ct)
	}

	resp2, err := srv.Client().Get(srv.URL + "/docs")
	if err != nil {
		t.Fatal(err)
	}
	defer resp2.Body.Close()
	if resp2.StatusCode != http.StatusOK {
		t.Fatalf("/docs status = %d", resp2.StatusCode)
	}
	body, _ := io.ReadAll(resp2.Body)
	if !strings.Contains(string(body), "swagger-ui") {
		t.Error("/docs ไม่มี swagger-ui อยู่ในหน้า")
	}
}

func TestRootRedirectsToDocs(t *testing.T) {
	srv := newServer(t)
	client := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error {
		return http.ErrUseLastResponse
	}}
	resp, err := client.Get(srv.URL + "/")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusFound {
		t.Fatalf("/ status = %d, want 302", resp.StatusCode)
	}
	if loc := resp.Header.Get("Location"); loc != "/docs" {
		t.Fatalf("redirect ไป %q, want /docs", loc)
	}
}

func TestDocsCanBeDisabled(t *testing.T) {
	srv := newServerWith(t, func(c *cfgOverride) { c.docsEnabled = false })

	resp, err := srv.Client().Get(srv.URL + "/openapi.json")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("ปิด docs แล้ว /openapi.json ควรได้ 404, ได้ %d", resp.StatusCode)
	}
}

func fetchSpec(t *testing.T) []byte {
	t.Helper()
	srv := newServer(t)
	resp, err := srv.Client().Get(srv.URL + "/openapi.json")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}
