// Command coverage-gap reports HTTP API endpoints that are registered in the
// Gin router but lack a corresponding capability in the stateful E2E manifest.
//
// Usage:
//
//	go run ./cmd/coverage-gap [flags]
//
// Flags:
//
//	-router      path to api/http/router.go (default: auto-detect from go.mod)
//	-mcp-handler path to MCP handler (default: auto-detect)
//	-manifest    path to manifest.json (default: auto-detect)
//	-json        output JSON instead of text
package main

import (
	"bufio"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"github.com/byteBuilderX/stratum/pkg/reporoot"
)

// route represents a single HTTP route registered in Gin.
type route struct {
	Method string // GET, POST, PUT, DELETE, PATCH
	Path   string // normalized path, e.g. /agents/:id/execute
	Source string // file:line hint
}

// manifestMutation represents one capability's mutation declaration.
type manifestMutation struct {
	Method string // GET, POST, PUT, DELETE, PATCH, NAVIGATE
	Path   string // e.g. /agents, /agents/:id
	CapID  string // capability id from manifest
	Domain string // business domain
}

// uncovered records a route that lacks manifest coverage.
type uncovered struct {
	route  route
	reason string
}

// jsonUncovered is the JSON-serializable form of uncovered.
type jsonUncovered struct {
	Method string `json:"method"`
	Path   string `json:"path"`
	Source string `json:"source"`
	Domain string `json:"domain"`
}

// jsonOrphan is a manifest mutation with no matching route.
type jsonOrphan struct {
	Method string `json:"method"`
	Path   string `json:"path"`
	CapID  string `json:"capability_id"`
	Domain string `json:"domain"`
}

func main() {
	var (
		routerFile   string
		mcpFile      string
		manifestFile string
		asJSON       bool
	)
	flag.StringVar(&routerFile, "router", "", "path to api/http/router.go")
	flag.StringVar(&mcpFile, "mcp-handler", "", "path to MCP handler")
	flag.StringVar(&manifestFile, "manifest", "", "path to manifest.json")
	flag.BoolVar(&asJSON, "json", false, "output JSON")
	flag.Parse()

	repoRoot := reporoot.Find()
	if routerFile == "" {
		routerFile = filepath.Join(repoRoot, "api/http/router.go")
	}
	if mcpFile == "" {
		mcpFile = filepath.Join(repoRoot, "api/http/handler/mcp_handler.go")
	}
	if manifestFile == "" {
		manifestFile = filepath.Join(repoRoot, "test/e2e/stateful/manifest.json")
	}

	routes, err := extractRoutes(routerFile, mcpFile)
	if err != nil {
		fmt.Fprintf(os.Stderr, "failed to extract routes: %v\n", err)
		os.Exit(1)
	}

	mutations, err := extractManifestMutations(manifestFile)
	if err != nil {
		fmt.Fprintf(os.Stderr, "failed to parse manifest: %v\n", err)
		os.Exit(1)
	}

	manifestIndex := buildManifestIndex(mutations)
	uncoveredRoutes, coveredCount := findUncovered(routes, manifestIndex)
	orphanMutations := findOrphans(mutations, routes)

	if asJSON {
		outputJSON(uncoveredRoutes, orphanMutations, len(routes), coveredCount, len(mutations))
		return
	}
	printTextReport(uncoveredRoutes, orphanMutations, len(routes), coveredCount, len(mutations))
}

func buildManifestIndex(mutations []manifestMutation) map[string]manifestMutation {
	idx := make(map[string]manifestMutation)
	for _, m := range mutations {
		if m.Method == "NAVIGATE" {
			continue
		}
		idx[m.Method+" "+normalizePath(m.Path)] = m
	}
	return idx
}

func findUncovered(routes []route, manifestIndex map[string]manifestMutation) ([]uncovered, int) {
	excludedPrefixes := []string{
		"/metrics", "/livez", "/readyz", "/health",
		"/avatars/",                   // static file serving
		"/auth/github",                // OAuth redirect (GET)
		"/auth/github/callback",       // OAuth callback (GET)
		"/prompts/",                   // prompt A/B infra
		"/resource-change-proposals/", // internal Agent tool chain
	}

	var uncoveredRoutes []uncovered
	coveredCount := 0

routeLoop:
	for _, r := range routes {
		key := r.Method + " " + normalizePath(r.Path)
		if _, ok := manifestIndex[key]; ok {
			coveredCount++
			continue
		}
		for _, p := range excludedPrefixes {
			if strings.HasPrefix(r.Path, p) {
				continue routeLoop
			}
		}
		uncoveredRoutes = append(uncoveredRoutes, uncovered{route: r, reason: "no manifest capability"})
	}
	return uncoveredRoutes, coveredCount
}

func findOrphans(mutations []manifestMutation, routes []route) []manifestMutation {
	var orphans []manifestMutation
	for _, m := range mutations {
		if m.Method == "NAVIGATE" {
			continue
		}
		key := m.Method + " " + normalizePath(m.Path)
		found := false
		for _, r := range routes {
			if r.Method+" "+normalizePath(r.Path) == key {
				found = true
				break
			}
		}
		if !found {
			orphans = append(orphans, m)
		}
	}
	return orphans
}

func printTextReport(uncoveredRoutes []uncovered, orphanMutations []manifestMutation,
	totalRoutes, coveredCount, totalMutations int) {

	fmt.Println("═══════════════════════════════════════════════════════════")
	fmt.Println("  API → Manifest 契约覆盖率报告")
	fmt.Println("═══════════════════════════════════════════════════════════")
	fmt.Printf("  Gin 路由总数:      %d\n", totalRoutes)
	fmt.Printf("  Manifest mutations: %d (NAVIGATE 已排除)\n", totalMutations)
	fmt.Printf("  已覆盖:            %d\n", coveredCount)
	fmt.Printf("  覆盖率:            %.1f%%\n", pct(coveredCount, totalRoutes))
	fmt.Println()

	if len(uncoveredRoutes) == 0 {
		fmt.Println("  ✓ 所有 API 路由均有对应 manifest capability")
	} else {
		fmt.Printf("  ✗ %d 个 API 路由缺少 manifest capability:\n\n", len(uncoveredRoutes))
		grouped := make(map[string][]uncovered)
		for _, u := range uncoveredRoutes {
			domain := pathDomain(u.route.Path)
			grouped[domain] = append(grouped[domain], u)
		}
		domains := sortedKeys(grouped)
		for _, d := range domains {
			fmt.Printf("  [%s]\n", d)
			for _, u := range grouped[d] {
				fmt.Printf("    %-7s %s\n", u.route.Method, u.route.Path)
			}
			fmt.Println()
		}
	}

	if len(orphanMutations) > 0 {
		fmt.Printf("  ⚠ %d 个 manifest capability 对应路由不存在（可能已删除/重命名）:\n\n",
			len(orphanMutations))
		for _, m := range orphanMutations {
			fmt.Printf("    %-7s %-40s ← %s (%s)\n", m.Method, m.Path, m.CapID, m.Domain)
		}
		fmt.Println()
	}

	fmt.Println("───────────────────────────────────────────────────────────")
	if len(uncoveredRoutes) > 0 {
		fmt.Println("  建议操作:")
		fmt.Println("  1. 评估未覆盖路由的业务风险，按优先级补充 manifest capability")
		fmt.Println("  2. 对应 pack 文件添加 Playwright 执行步骤 + evidence 三方对账")
		fmt.Println("  3. 基础设施端点（/health, /metrics）无需覆盖，已在 excludedPrefixes 排除")
	}
}

func pct(part, total int) float64 {
	if total == 0 {
		return 0
	}
	return float64(part) / float64(total) * 100
}

func sortedKeys[V any](m map[string]V) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

func outputJSON(uncoveredRoutes []uncovered, orphanMutations []manifestMutation,
	totalRoutes, coveredCount, totalMutations int) {

	type result struct {
		TotalRoutes     int             `json:"total_routes"`
		CoveredRoutes   int             `json:"covered_routes"`
		CoveragePct     float64         `json:"coverage_pct"`
		TotalMutations  int             `json:"total_mutations"`
		Uncovered       []jsonUncovered `json:"uncovered_routes"`
		OrphanMutations []jsonOrphan    `json:"orphan_mutations,omitempty"`
	}
	r := result{
		TotalRoutes:    totalRoutes,
		CoveredRoutes:  coveredCount,
		CoveragePct:    pct(coveredCount, totalRoutes),
		TotalMutations: totalMutations,
	}
	for _, u := range uncoveredRoutes {
		r.Uncovered = append(r.Uncovered, jsonUncovered{
			Method: u.route.Method,
			Path:   u.route.Path,
			Source: u.route.Source,
			Domain: pathDomain(u.route.Path),
		})
	}
	for _, m := range orphanMutations {
		r.OrphanMutations = append(r.OrphanMutations, jsonOrphan(m))
	}
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	_ = enc.Encode(r)
}

// ---------------------------------------------------------------------------
// Path helpers
// ---------------------------------------------------------------------------

// domainMap maps URL path prefixes to business domains.
var domainMap = map[string]string{
	"agents": "agent", "conversations": "agent",
	"skills":             "skill",
	"workflows":          "workflow",
	"workflow-runs":      "workflow",
	"workflow-approvals": "workflow",
	"evaluations":        "evaluation",
	"knowledge":          "knowledge",
	"mcp":                "mcp",
	"memory":             "memory",
	"auth":               "iam",
	"tenant":             "iam",
	"dashboard":          "dashboard",
	"audit":              "audit",
	"models":             "llm-admin",
	"prompts":            "prompt",
}

func pathDomain(path string) string {
	parts := strings.Split(strings.TrimPrefix(path, "/"), "/")
	if len(parts) == 0 || parts[0] == "" {
		return "root"
	}
	if d, ok := domainMap[parts[0]]; ok {
		return d
	}
	if parts[0] == "admin" && len(parts) > 1 {
		return "admin/" + parts[1]
	}
	return parts[0]
}

// paramPattern matches Gin/manifest-style path parameters.
var paramPattern = regexp.MustCompile(`:[A-Za-z_][A-Za-z0-9_]*`)

// normalizePath converts Gin-style :param and *param to a canonical form
// where all path parameters are normalized to :p to enable structural comparison.
func normalizePath(p string) string {
	p = regexp.MustCompile(`\*[^/]+`).ReplaceAllString(p, ":p")
	p = paramPattern.ReplaceAllString(p, ":p")
	p = strings.TrimRight(p, "/")
	if p == "" {
		p = "/"
	}
	return p
}

// ---------------------------------------------------------------------------
// Route extraction from Go source
// ---------------------------------------------------------------------------

var (
	groupRegex        = regexp.MustCompile(`(\w+)\s*:=\s*(?:r|router)\.Group\(\s*"([^"]+)"`)
	routeOnGroupRegex = regexp.MustCompile(`(\w+)\.(GET|POST|PUT|DELETE|PATCH|HEAD|OPTIONS)\("([^"]*)"`)
	directRouteRegex  = regexp.MustCompile(`\br\.(GET|POST|PUT|DELETE|PATCH|HEAD|OPTIONS)\("([^"]+)"`)
	mcpGroupRegex     = regexp.MustCompile(`(\w+)\s*:=\s*router\.Group\(\s*"([^"]+)"`)
)

func extractRoutes(routerFile, mcpFile string) ([]route, error) {
	var routes []route

	routesFromRouter, err := parseRouterFile(routerFile)
	if err != nil {
		return nil, fmt.Errorf("router.go: %w", err)
	}
	routes = append(routes, routesFromRouter...)

	mcpRoutes, err := parseMCPHandler(mcpFile)
	if err != nil {
		return nil, fmt.Errorf("mcp_handler.go: %w", err)
	}
	routes = append(routes, mcpRoutes...)

	sort.Slice(routes, func(i, j int) bool {
		if routes[i].Method != routes[j].Method {
			return routes[i].Method < routes[j].Method
		}
		return routes[i].Path < routes[j].Path
	})

	seen := make(map[string]bool)
	var deduped []route
	for _, r := range routes {
		key := r.Method + " " + r.Path
		if !seen[key] {
			seen[key] = true
			deduped = append(deduped, r)
		}
	}
	return deduped, nil
}

func parseRouterFile(path string) ([]route, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	var routes []route
	groups := make(map[string]string)
	scanner := bufio.NewScanner(f)
	lineNo := 0

	for scanner.Scan() {
		lineNo++
		line := scanner.Text()

		if matches := groupRegex.FindStringSubmatch(line); matches != nil {
			groups[matches[1]] = matches[2]
		}

		if matches := routeOnGroupRegex.FindStringSubmatch(line); matches != nil {
			varName, method, path := matches[1], matches[2], matches[3]
			if prefix, ok := groups[varName]; ok {
				fullPath := prefix + path
				fullPath = strings.ReplaceAll(fullPath, "//", "/")
				routes = append(routes, route{
					Method: method,
					Path:   fullPath,
					Source: fmt.Sprintf("%s:%d", filepath.Base(path), lineNo),
				})
			}
		}

		if matches := directRouteRegex.FindStringSubmatch(line); matches != nil {
			routes = append(routes, route{
				Method: matches[1],
				Path:   matches[2],
				Source: fmt.Sprintf("%s:%d", filepath.Base(path), lineNo),
			})
		}
	}

	_ = scanner.Err()
	return routes, nil
}

func parseMCPHandler(path string) ([]route, error) {
	f, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	defer f.Close()

	var routes []route
	groups := make(map[string]string)
	scanner := bufio.NewScanner(f)
	lineNo := 0

	for scanner.Scan() {
		lineNo++
		line := scanner.Text()

		if matches := mcpGroupRegex.FindStringSubmatch(line); matches != nil {
			groups[matches[1]] = matches[2]
		}

		if matches := routeOnGroupRegex.FindStringSubmatch(line); matches != nil {
			varName, method, path := matches[1], matches[2], matches[3]
			if prefix, ok := groups[varName]; ok {
				routes = append(routes, route{
					Method: method,
					Path:   prefix + path,
					Source: fmt.Sprintf("%s:%d", filepath.Base(path), lineNo),
				})
			}
		}
	}

	_ = scanner.Err()
	return routes, nil
}

// ---------------------------------------------------------------------------
// Manifest parsing
// ---------------------------------------------------------------------------

type manifestFile struct {
	Capabilities []manifestCap `json:"capabilities"`
}

type manifestCap struct {
	ID       string `json:"id"`
	Domain   string `json:"domain"`
	Mutation string `json:"mutation"`
}

func extractManifestMutations(path string) ([]manifestMutation, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var mf manifestFile
	if err := json.Unmarshal(data, &mf); err != nil {
		return nil, fmt.Errorf("parse manifest: %w", err)
	}
	var mutations []manifestMutation
	for _, cap := range mf.Capabilities {
		m := parseMutation(cap.Mutation)
		m.CapID = cap.ID
		m.Domain = cap.Domain
		mutations = append(mutations, m)
	}
	return mutations, nil
}

func parseMutation(raw string) manifestMutation {
	raw = strings.TrimSpace(raw)
	method, path, found := strings.Cut(raw, " ")
	if !found {
		return manifestMutation{Path: raw}
	}
	return manifestMutation{Method: method, Path: path}
}
