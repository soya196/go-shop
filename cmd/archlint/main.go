// Command archlint บังคับกฎสถาปัตยกรรมด้วย import graph
//
// 🔑 นี่คือหัวใจของแนวคิด "framework = ตัวบังคับกฎ ไม่ใช่ตัว gen ไฟล์"
//
// ที่มา: คลาส Go Part 4 ชี้ว่ากฎ Clean Architecture ทุกข้อ **ตรวจได้จาก import**
//
//	Hexagonal → domain ต้องไม่ import adapter
//	Onion     → adapter import domain ได้ (ทิศทางเดียว)
//	Screaming → ชื่อ package ต้องเป็นคำธุรกิจ ไม่ใช่คำเทคนิค
//
// ถ้ามันตรวจได้ ก็ไม่มีเหตุผลที่จะปล่อยให้เป็นแค่ "ข้อตกลงในหัวคน"
// เอามาทำให้ build แดงซะ แล้วกฎจะอยู่ได้เองโดยไม่ต้องพึ่งวินัยใคร
//
// ใช้: go run ./cmd/archlint [-config arch.json] [-v]
// คืน exit code 1 เมื่อพบการละเมิด → ใส่ใน CI ได้ทันที
package main

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path"
	"sort"
	"strings"
)

// config คือกฎที่อ่านจาก arch.json
type config struct {
	Module string              `json:"module"`
	Layers map[string][]string `json:"layers"`
	Rules  struct {
		DomainMustNotImportLayers  []string `json:"domainMustNotImportLayers"`
		DomainMustNotImportThird   bool     `json:"domainMustNotImportThirdParty"`
		DomainStdlibDenyList       []string `json:"domainStdlibDenyList"`
		SharedKernel               []string `json:"sharedKernel"`
		AllowedCrossDomain         []string `json:"allowedCrossDomain"`
		BannedPackageNames         []string `json:"bannedPackageNames"`
		AdapterMustNotImportLayers []string `json:"adapterMustNotImportLayers"`
	} `json:"rules"`
}

// pkg คือข้อมูลหนึ่ง package จาก `go list -json`
type pkg struct {
	ImportPath string   `json:"ImportPath"`
	Name       string   `json:"Name"`
	Standard   bool     `json:"Standard"`
	Imports    []string `json:"Imports"`
	TestImport []string `json:"TestImports"`
}

type violation struct {
	Rule string
	From string
	To   string
	Why  string
}

func main() {
	cfgPath := flag.String("config", "arch.json", "ไฟล์กฎ")
	verbose := flag.Bool("v", false, "แสดง package ที่ตรวจทั้งหมด")
	flag.Parse()

	cfg, err := loadConfig(*cfgPath)
	if err != nil {
		fatal(err)
	}
	pkgs, err := listPackages()
	if err != nil {
		fatal(err)
	}

	layerOf := buildLayerIndex(cfg)
	violations := check(cfg, pkgs, layerOf, *verbose)

	report(cfg, pkgs, layerOf, violations)
	if len(violations) > 0 {
		os.Exit(1)
	}
}

func fatal(err error) {
	fmt.Fprintln(os.Stderr, "archlint:", err)
	os.Exit(2)
}

func loadConfig(p string) (*config, error) {
	raw, err := os.ReadFile(p)
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", p, err)
	}
	var c config
	if err := json.Unmarshal(raw, &c); err != nil {
		return nil, fmt.Errorf("parse %s: %w", p, err)
	}
	if c.Module == "" {
		return nil, fmt.Errorf("%s: missing \"module\"", p)
	}
	return &c, nil
}

// listPackages เรียก `go list -json ./...` แล้วอ่านผลเป็น stream
//
// ใช้ go list แทนการ parse AST เอง เพราะ go list เข้าใจ build tag / generated code
// และเป็นความจริงชุดเดียวกับที่ compiler เห็น
func listPackages() ([]pkg, error) {
	cmd := exec.Command("go", "list", "-json", "./...")
	out, err := cmd.Output()
	if err != nil {
		var ee *exec.ExitError
		if ok := asExitError(err, &ee); ok {
			return nil, fmt.Errorf("go list failed: %s", strings.TrimSpace(string(ee.Stderr)))
		}
		return nil, fmt.Errorf("go list: %w", err)
	}
	dec := json.NewDecoder(strings.NewReader(string(out)))
	var pkgs []pkg
	for dec.More() {
		var p pkg
		if err := dec.Decode(&p); err != nil {
			return nil, fmt.Errorf("decode go list output: %w", err)
		}
		pkgs = append(pkgs, p)
	}
	return pkgs, nil
}

func asExitError(err error, target **exec.ExitError) bool {
	// ใช้ errors.As ไม่ใช่ type assertion — assertion ตรงๆ จะพลาดถ้า error ถูก wrap
	return errors.As(err, target)
}

// buildLayerIndex map: import path เต็ม → ชื่อ layer
func buildLayerIndex(cfg *config) map[string]string {
	idx := map[string]string{}
	for layer, dirs := range cfg.Layers {
		for _, d := range dirs {
			idx[path.Join(cfg.Module, d)] = layer
		}
	}
	return idx
}

func contains(list []string, s string) bool {
	for _, x := range list {
		if x == s {
			return true
		}
	}
	return false
}

// ═══════════════════════════════════════════════════════════════════
// เครื่องยนต์ตรวจกฎ
//
// ออกแบบให้ "เพิ่มกฎใหม่ = เพิ่มฟังก์ชัน 1 ตัว แล้วใส่ในตาราง"
// ไม่ใช่ไปแทรก if ในฟังก์ชันยักษ์ — เพราะ archlint ตั้งใจจะแยกออกไปเป็น
// เครื่องมือของตัวเองที่ใช้กับโปรเจกต์ Go อื่นได้
// ═══════════════════════════════════════════════════════════════════

// ruleCtx คือของที่ทุกกฎต้องใช้ร่วมกัน (คำนวณครั้งเดียวตอนเริ่ม)
type ruleCtx struct {
	cfg          *config
	layerOf      map[string]string
	shared       map[string]bool
	allowedCross map[string]bool
}

func newRuleCtx(cfg *config, layerOf map[string]string) *ruleCtx {
	rc := &ruleCtx{
		cfg:          cfg,
		layerOf:      layerOf,
		shared:       map[string]bool{},
		allowedCross: map[string]bool{},
	}
	for _, s := range cfg.Rules.SharedKernel {
		rc.shared[path.Join(cfg.Module, s)] = true
	}
	for _, s := range cfg.Rules.AllowedCrossDomain {
		rc.allowedCross[s] = true
	}
	return rc
}

// edge คือ "package หนึ่ง import อีก package หนึ่ง" — หน่วยที่กฎส่วนใหญ่ตรวจ
type edge struct {
	from     pkg
	to       string
	fromName string // ชื่อสั้นของ from
	toName   string // ชื่อสั้นของ to
	isOwn    bool   // to อยู่ใน module เดียวกันไหม
	toLayer  string
}

// importRule ตรวจ edge หนึ่งเส้น · คืน nil ถ้าผ่าน
type importRule struct {
	layer string // ใช้กับ package ชั้นไหน
	fn    func(rc *ruleCtx, e edge) *violation
}

// importRules คือทะเบียนกฎทั้งหมด — อ่านตารางนี้ = รู้ว่า archlint บังคับอะไรบ้าง
var importRules = []importRule{
	{"domain", ruleDomainImportsLayer},
	{"domain", ruleCrossDomain},
	{"domain", ruleDomainThirdParty},
	{"domain", ruleDomainStdlibDenyList},
	{"adapter", ruleAdapterImportsLayer},
}

// ลูกศรพึ่งพาต้องชี้เข้าหา domain เท่านั้น
func ruleDomainImportsLayer(rc *ruleCtx, e edge) *violation {
	if !e.isOwn || !contains(rc.cfg.Rules.DomainMustNotImportLayers, e.toLayer) {
		return nil
	}
	return &violation{
		Rule: "domain-imports-" + e.toLayer,
		From: e.fromName, To: e.toName,
		Why: "ลูกศรพึ่งพาต้องชี้เข้าหา domain เท่านั้น (Dependency Rule)",
	}
}

// domain ต้องไม่รู้จัก domain อื่นตรงๆ (ยกเว้น shared kernel)
func ruleCrossDomain(rc *ruleCtx, e edge) *violation {
	if !e.isOwn || e.toLayer != "domain" || e.to == e.from.ImportPath || rc.shared[e.to] {
		return nil
	}
	if rc.allowedCross[e.fromName+" -> "+e.toName] {
		return nil
	}
	return &violation{
		Rule: "cross-domain-import",
		From: e.fromName, To: e.toName,
		Why: "domain ต้องคุยกันผ่าน port ที่ตัวเองประกาศ + bridge ไม่ใช่ import ตรง",
	}
}

// domain ต้องไม่ผูกกับ library ภายนอก
func ruleDomainThirdParty(rc *ruleCtx, e edge) *violation {
	if e.isOwn || !rc.cfg.Rules.DomainMustNotImportThird || !isThirdParty(e.to) {
		return nil
	}
	return &violation{
		Rule: "domain-imports-third-party",
		From: e.fromName, To: e.to,
		Why: "domain ต้องไม่ผูกกับ library ภายนอก (Framework Independent)",
	}
}

// domain ต้องไม่แตะ stdlib ที่เป็นเรื่อง infrastructure
func ruleDomainStdlibDenyList(rc *ruleCtx, e edge) *violation {
	if e.isOwn || !contains(rc.cfg.Rules.DomainStdlibDenyList, e.to) {
		return nil
	}
	return &violation{
		Rule: "domain-imports-infrastructure",
		From: e.fromName, To: e.to,
		Why: "เป็นเรื่องของโลกภายนอก ควรอยู่ใน adapter",
	}
}

// adapter ต้องไม่รู้จักจุดประกอบร่าง
func ruleAdapterImportsLayer(rc *ruleCtx, e edge) *violation {
	if !e.isOwn || !contains(rc.cfg.Rules.AdapterMustNotImportLayers, e.toLayer) {
		return nil
	}
	return &violation{
		Rule: "adapter-imports-" + e.toLayer,
		From: e.fromName, To: e.toName,
		Why: "adapter ต้องไม่รู้จักจุดประกอบร่าง",
	}
}

// ── กฎระดับ package (ไม่ได้ดู import) ────────────────────────────

// ชื่อ package ต้องไม่เป็นคำเทคนิคกลวงๆ
func rulePackageName(rc *ruleCtx, p pkg) *violation {
	if !contains(rc.cfg.Rules.BannedPackageNames, p.Name) {
		return nil
	}
	return &violation{
		Rule: "banned-package-name",
		From: short(rc.cfg.Module, p.ImportPath), To: p.Name,
		Why: "ชื่อ package บอกแค่ 'เทคนิค' ไม่ได้บอกว่าธุรกิจทำอะไร (Screaming Architecture)",
	}
}

// package ที่ไม่ได้อยู่ layer ไหนเลย = ลืมประกาศใน arch.json
func ruleClassified(rc *ruleCtx, p pkg) *violation {
	if rc.layerOf[p.ImportPath] != "" {
		return nil
	}
	return &violation{
		Rule: "unclassified-package",
		From: short(rc.cfg.Module, p.ImportPath), To: "-",
		Why: "ยังไม่ได้ระบุ layer ใน arch.json — package ใหม่ต้องประกาศว่าเป็น domain หรือ adapter",
	}
}

// check เดินทุก package แล้วยิงกฎทุกข้อใส่
func check(cfg *config, pkgs []pkg, layerOf map[string]string, verbose bool) []violation {
	rc := newRuleCtx(cfg, layerOf)
	var vs []violation

	for _, p := range pkgs {
		if p.Standard {
			continue
		}
		vs = appendIf(vs, rulePackageName(rc, p))

		if v := ruleClassified(rc, p); v != nil {
			vs = append(vs, *v)
			continue // ไม่รู้ว่าอยู่ชั้นไหน ก็ตรวจ import ต่อไม่ได้
		}
		vs = append(vs, rc.checkImports(p)...)

		if verbose {
			fmt.Printf("  %-12s %s\n", "["+layerOf[p.ImportPath]+"]", short(cfg.Module, p.ImportPath))
		}
	}

	sort.Slice(vs, func(i, j int) bool {
		if vs[i].Rule != vs[j].Rule {
			return vs[i].Rule < vs[j].Rule
		}
		return vs[i].From < vs[j].From
	})
	return vs
}

// checkImports ยิงกฎระดับ edge ใส่ทุก import ของ package หนึ่ง
func (rc *ruleCtx) checkImports(p pkg) []violation {
	layer := rc.layerOf[p.ImportPath]
	var vs []violation

	for _, imp := range p.Imports {
		e := edge{
			from:     p,
			to:       imp,
			fromName: short(rc.cfg.Module, p.ImportPath),
			toName:   short(rc.cfg.Module, imp),
			isOwn:    strings.HasPrefix(imp, rc.cfg.Module+"/") || imp == rc.cfg.Module,
			toLayer:  rc.layerOf[imp],
		}
		for _, r := range importRules {
			if r.layer == layer {
				vs = appendIf(vs, r.fn(rc, e))
			}
		}
	}
	return vs
}

func appendIf(vs []violation, v *violation) []violation {
	if v == nil {
		return vs
	}
	return append(vs, *v)
}

// isThirdParty เดาว่า import path เป็น library ภายนอกไหม
//
// heuristic เดียวกับที่ Go ใช้: stdlib ไม่มีจุดใน element แรกของ path
func isThirdParty(imp string) bool {
	first, _, _ := strings.Cut(imp, "/")
	return strings.Contains(first, ".")
}

func short(module, p string) string {
	return strings.TrimPrefix(strings.TrimPrefix(p, module), "/")
}

func report(cfg *config, pkgs []pkg, layerOf map[string]string, vs []violation) {
	counts := map[string]int{}
	for _, p := range pkgs {
		if !p.Standard {
			counts[layerOf[p.ImportPath]]++
		}
	}

	fmt.Println("⬡ archlint — ตรวจกฎสถาปัตยกรรมจาก import graph")
	fmt.Printf("  module     : %s\n", cfg.Module)
	fmt.Printf("  packages   : %d domain · %d adapter · %d composition\n",
		counts["domain"], counts["adapter"], counts["composition"])

	if len(vs) == 0 {
		fmt.Println("\n✅ ผ่านทุกกฎ")
		fmt.Println("   · domain ไม่รู้จัก adapter / framework / library ภายนอก")
		fmt.Println("   · ไม่มี domain ไหน import domain อื่นตรงๆ")
		fmt.Println("   · ไม่มีชื่อ package กลวง (util/common/model/...)")
		return
	}

	fmt.Printf("\n❌ พบการละเมิด %d จุด\n", len(vs))
	lastRule := ""
	for _, v := range vs {
		if v.Rule != lastRule {
			fmt.Printf("\n  ▸ %s\n    %s\n", v.Rule, v.Why)
			lastRule = v.Rule
		}
		fmt.Printf("      %s  →  %s\n", v.From, v.To)
	}
	fmt.Println()
}
