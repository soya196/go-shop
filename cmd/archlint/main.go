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

	report(cfg, pkgs, layerOf, violations, *verbose)
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
	if ee, ok := err.(*exec.ExitError); ok {
		*target = ee
		return true
	}
	return false
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

func check(cfg *config, pkgs []pkg, layerOf map[string]string, verbose bool) []violation {
	var vs []violation

	shared := map[string]bool{}
	for _, s := range cfg.Rules.SharedKernel {
		shared[path.Join(cfg.Module, s)] = true
	}
	allowedCross := map[string]bool{}
	for _, s := range cfg.Rules.AllowedCrossDomain {
		allowedCross[s] = true
	}

	for _, p := range pkgs {
		if p.Standard {
			continue
		}
		layer := layerOf[p.ImportPath]

		// ── กฎ: ชื่อ package ต้องไม่เป็นคำเทคนิคกลวงๆ (Screaming) ──
		if contains(cfg.Rules.BannedPackageNames, p.Name) {
			vs = append(vs, violation{
				Rule: "banned-package-name",
				From: short(cfg.Module, p.ImportPath),
				To:   p.Name,
				Why:  "ชื่อ package บอกแค่ 'เทคนิค' ไม่ได้บอกว่าธุรกิจทำอะไร (Screaming Architecture)",
			})
		}

		// ── package ที่ไม่ได้อยู่ใน layer ไหนเลย = ลืมประกาศใน arch.json ──
		if layer == "" {
			vs = append(vs, violation{
				Rule: "unclassified-package",
				From: short(cfg.Module, p.ImportPath),
				To:   "-",
				Why:  "ยังไม่ได้ระบุ layer ใน arch.json — package ใหม่ต้องประกาศว่าเป็น domain หรือ adapter",
			})
			continue
		}

		for _, imp := range p.Imports {
			isOwn := strings.HasPrefix(imp, cfg.Module+"/") || imp == cfg.Module
			impLayer := layerOf[imp]

			switch layer {
			case "domain":
				// domain ห้าม import layer ที่กำหนด
				if isOwn && contains(cfg.Rules.DomainMustNotImportLayers, impLayer) {
					vs = append(vs, violation{
						Rule: "domain-imports-" + impLayer,
						From: short(cfg.Module, p.ImportPath),
						To:   short(cfg.Module, imp),
						Why:  "ลูกศรพึ่งพาต้องชี้เข้าหา domain เท่านั้น (Dependency Rule)",
					})
				}
				// domain ห้ามรู้จัก domain อื่น (ยกเว้น shared kernel)
				if isOwn && impLayer == "domain" && imp != p.ImportPath && !shared[imp] {
					key := short(cfg.Module, p.ImportPath) + " -> " + short(cfg.Module, imp)
					if !allowedCross[key] {
						vs = append(vs, violation{
							Rule: "cross-domain-import",
							From: short(cfg.Module, p.ImportPath),
							To:   short(cfg.Module, imp),
							Why:  "domain ต้องคุยกันผ่าน port ที่ตัวเองประกาศ + bridge ไม่ใช่ import ตรง",
						})
					}
				}
				// domain ห้ามพึ่ง library ภายนอก
				if !isOwn && cfg.Rules.DomainMustNotImportThird && isThirdParty(imp) {
					vs = append(vs, violation{
						Rule: "domain-imports-third-party",
						From: short(cfg.Module, p.ImportPath),
						To:   imp,
						Why:  "domain ต้องไม่ผูกกับ library ภายนอก (Framework Independent)",
					})
				}
				// domain ห้ามแตะ stdlib ที่เป็นเรื่อง infrastructure
				if !isOwn && contains(cfg.Rules.DomainStdlibDenyList, imp) {
					vs = append(vs, violation{
						Rule: "domain-imports-infrastructure",
						From: short(cfg.Module, p.ImportPath),
						To:   imp,
						Why:  "เป็นเรื่องของโลกภายนอก ควรอยู่ใน adapter",
					})
				}

			case "adapter":
				if isOwn && contains(cfg.Rules.AdapterMustNotImportLayers, impLayer) {
					vs = append(vs, violation{
						Rule: "adapter-imports-" + impLayer,
						From: short(cfg.Module, p.ImportPath),
						To:   short(cfg.Module, imp),
						Why:  "adapter ต้องไม่รู้จักจุดประกอบร่าง",
					})
				}
			}
		}

		if verbose {
			fmt.Printf("  %-12s %s\n", "["+layer+"]", short(cfg.Module, p.ImportPath))
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

func report(cfg *config, pkgs []pkg, layerOf map[string]string, vs []violation, verbose bool) {
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
