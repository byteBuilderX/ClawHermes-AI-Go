// Command coverage-gap-db reports database columns that are defined in
// tenant_schema.sql but never asserted in any E2E test (pack files or Go tests).
//
// Usage:
//
//	go run ./cmd/coverage-gap-db
//
// The tool reads tenant_schema.sql, scans pack TypeScript files and Go E2E
// tests for SQL queries, and cross-references column references to find
// columns that are never validated by any E2E assertion.
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

// column represents a database column from a CREATE TABLE or ALTER TABLE.
type column struct {
	Table    string
	Name     string
	Type     string
	Nullable bool
	Source   string // file:line
}

// columnRef records where a column is referenced in an E2E assertion.
type columnRef struct {
	Table   string
	Column  string
	File    string
	Line    int
	Snippet string
}

// uncoveredCol is a column with no E2E assertion coverage.
type uncoveredCol struct {
	col column
}

// tableStats tracks per-table coverage statistics.
type tableStats struct {
	totalColumns   int
	coveredColumns int
	uncoveredCols  []column
	hasE2ERef      bool
}

// coverageResult holds the computed coverage data.
type coverageResult struct {
	uncovered    []uncoveredCol
	coveredCount int
	totalColumns int
	tstats       map[string]*tableStats
	sysRefs      []columnRef
	unknownRefs  []columnRef
}

func main() {
	var (
		schemaFile string
		packsDir   string
		goE2EDir   string
		asJSON     bool
	)
	flag.StringVar(&schemaFile, "schema", "", "path to tenant_schema.sql")
	flag.StringVar(&packsDir, "packs", "", "path to stateful packs directory")
	flag.StringVar(&goE2EDir, "go-e2e", "", "path to Go E2E test directory")
	flag.BoolVar(&asJSON, "json", false, "output JSON")
	flag.Parse()

	repoRoot := reporoot.Find()
	if schemaFile == "" {
		schemaFile = filepath.Join(repoRoot, "pkg/storage/postgres/tenant_schema.sql")
	}
	if packsDir == "" {
		packsDir = filepath.Join(repoRoot, "web/e2e/stateful/packs")
	}
	if goE2EDir == "" {
		goE2EDir = filepath.Join(repoRoot, "test/e2e")
	}

	columns, err := extractColumns(schemaFile)
	if err != nil {
		fmt.Fprintf(os.Stderr, "failed to parse schema: %v\n", err)
		os.Exit(1)
	}

	refs, err := extractColumnRefs(packsDir, goE2EDir)
	if err != nil {
		fmt.Fprintf(os.Stderr, "failed to scan E2E references: %v\n", err)
		os.Exit(1)
	}

	result := computeCoverage(columns, refs)

	if asJSON {
		outputJSON(result)
		return
	}
	printTextReport(result)
}

// ---------------------------------------------------------------------------
// Coverage computation
// ---------------------------------------------------------------------------

func computeCoverage(columns []column, refs []columnRef) coverageResult {
	refIndex := buildRefIndex(refs)

	excludedTables := map[string]bool{
		"webhook_deliveries": true, "webhooks": true,
		"model_quotas": true, "model_usage": true, "model_presets": true,
		"scheduled_tasks": true,
		"exec_history":    true, "entity_relations": true,
		"memory_token_budgets": true, "sessions": true,
		"entities": true, "llm_api_keys": true,
		"agent_mcp_links": true, // deprecated
	}

	tstats := make(map[string]*tableStats)
	var uncovered []uncoveredCol
	coveredCount := 0
	totalColumns := 0

	for _, col := range columns {
		tkey := strings.ToLower(col.Table)
		if excludedTables[tkey] {
			continue
		}
		totalColumns++

		if tstats[tkey] == nil {
			tstats[tkey] = &tableStats{}
		}
		tstats[tkey].totalColumns++

		ckey := strings.ToLower(col.Name)
		if _, ok := refIndex[tkey][ckey]; ok {
			coveredCount++
		} else {
			uncovered = append(uncovered, uncoveredCol{col: col})
			tstats[tkey].uncoveredCols = append(tstats[tkey].uncoveredCols, col)
		}
	}

	// Mark tables with E2E references and compute covered counts.
	for tkey := range refIndex {
		if ts, ok := tstats[tkey]; ok {
			ts.hasE2ERef = true
		}
	}
	for _, ts := range tstats {
		ts.coveredColumns = ts.totalColumns - len(ts.uncoveredCols)
	}

	sysRefs, unknownRefs := classifyStaleRefs(refIndex, columns)
	return coverageResult{
		uncovered:    uncovered,
		coveredCount: coveredCount,
		totalColumns: totalColumns,
		tstats:       tstats,
		sysRefs:      sysRefs,
		unknownRefs:  unknownRefs,
	}
}

func buildRefIndex(refs []columnRef) map[string]map[string][]columnRef {
	idx := make(map[string]map[string][]columnRef)
	for _, r := range refs {
		tkey := strings.ToLower(r.Table)
		if idx[tkey] == nil {
			idx[tkey] = make(map[string][]columnRef)
		}
		ckey := strings.ToLower(r.Column)
		idx[tkey][ckey] = append(idx[tkey][ckey], r)
	}
	return idx
}

func classifyStaleRefs(refIndex map[string]map[string][]columnRef, columns []column) (sysRefs, unknownRefs []columnRef) {
	systemTables := map[string]bool{
		"tenants": true, "users": true, "tenant_members": true,
		"tenant_invitations": true,
	}

	for tkey, cols := range refIndex {
		for ckey, refList := range cols {
			if hasColumn(columns, tkey, ckey) {
				continue
			}
			if systemTables[tkey] {
				sysRefs = append(sysRefs, refList...)
			} else {
				unknownRefs = append(unknownRefs, refList...)
			}
		}
	}
	return
}

func hasColumn(columns []column, table, col string) bool {
	for _, c := range columns {
		if strings.ToLower(c.Table) == table && strings.ToLower(c.Name) == col {
			return true
		}
	}
	return false
}

// ---------------------------------------------------------------------------
// Text report
// ---------------------------------------------------------------------------

func printTextReport(r coverageResult) {
	fmt.Println("═══════════════════════════════════════════════════════════")
	fmt.Println("  DB 列 → E2E 断言追踪报告")
	fmt.Println("═══════════════════════════════════════════════════════════")
	fmt.Printf("  DDL 列总数:                %d\n", r.totalColumns)
	fmt.Printf("  E2E 直接 SQL 引用:         %d 列 (%.1f%%)\n",
		r.coveredCount, pct(r.coveredCount, r.totalColumns))
	fmt.Printf("  有 E2E 查询的表:           %d / %d\n",
		queriedCount(r.tstats), len(r.tstats))
	fmt.Println()

	printQueriedTableDetails(r.tstats)
	printUnqueriedTables(r.tstats)
	printSystemRefs(r.sysRefs)
	printUnknownRefs(r.unknownRefs)

	fmt.Println("───────────────────────────────────────────────────────────")
	fmt.Println("  建议操作:")
	fmt.Println("  1. 优先为「有 E2E 查询的表」中未覆盖的列补充 DB 断言")
	fmt.Println("  2. 无直接查询的表，通过 API → Manifest 契约工具交叉验证")
	fmt.Println("  3. NULL 列需确认 E2E 覆盖了 NULL 值边界")
	fmt.Println("  4. SELECT * 和 ORM 查询无法静态追踪，需人工确认")
}

func queriedCount(tstats map[string]*tableStats) int {
	n := 0
	for _, ts := range tstats {
		if ts.hasE2ERef {
			n++
		}
	}
	return n
}

func printQueriedTableDetails(tstats map[string]*tableStats) {
	queried := sortedFilteredKeys(tstats, func(ts *tableStats) bool { return ts.hasE2ERef })
	if len(queried) == 0 {
		return
	}

	fmt.Println("───────────────────────────────────────────────────────────")
	fmt.Println("  有 E2E 查询的表 → 未覆盖列")
	fmt.Println("───────────────────────────────────────────────────────────")
	hasUncovered := false
	for _, tkey := range queried {
		ts := tstats[tkey]
		if len(ts.uncoveredCols) == 0 {
			continue
		}
		hasUncovered = true
		displayName := ts.uncoveredCols[0].Table
		fmt.Printf("\n  [%s] %d/%d covered, %d uncovered:\n",
			displayName, ts.coveredColumns, ts.totalColumns, len(ts.uncoveredCols))
		sort.Slice(ts.uncoveredCols, func(i, j int) bool {
			return ts.uncoveredCols[i].Name < ts.uncoveredCols[j].Name
		})
		for _, col := range ts.uncoveredCols {
			nullable := ""
			if col.Nullable {
				nullable = " NULL"
			}
			fmt.Printf("    %-35s %s%s\n", col.Name, col.Type, nullable)
		}
	}
	if !hasUncovered {
		fmt.Println("\n  ✓ 所有被查询表的列均已覆盖")
	}

	fmt.Println()
	fmt.Println("  完全覆盖的表:")
	for _, tkey := range queried {
		ts := tstats[tkey]
		if len(ts.uncoveredCols) == 0 {
			fmt.Printf("    ✓ %-38s %d/%d 列\n", tkey, ts.coveredColumns, ts.totalColumns)
		}
	}
	fmt.Println()
}

func printUnqueriedTables(tstats map[string]*tableStats) {
	unqueried := sortedFilteredKeys(tstats, func(ts *tableStats) bool { return !ts.hasE2ERef })
	if len(unqueried) == 0 {
		return
	}

	fmt.Println("───────────────────────────────────────────────────────────")
	fmt.Println("  无直接 E2E SQL 查询的表（通过 HTTP API 间接验证）")
	fmt.Println("───────────────────────────────────────────────────────────")
	var unqueriedColCount int
	for _, tkey := range unqueried {
		ts := tstats[tkey]
		unqueriedColCount += ts.totalColumns
		fmt.Printf("    %-40s %d 列\n", tkey, ts.totalColumns)
	}
	fmt.Printf("\n  共 %d 个表 %d 列 — 通过 HTTP 响应间接验证\n",
		len(unqueried), unqueriedColCount)
	fmt.Println()
}

func printSystemRefs(sysRefs []columnRef) {
	if len(sysRefs) == 0 {
		return
	}
	fmt.Println("───────────────────────────────────────────────────────────")
	fmt.Printf("  System schema 引用 (%d 个) — 迁移表，不在 tenant_schema.sql 范围内:\n", len(sysRefs))
	seen := make(map[string]bool)
	for _, r := range sysRefs {
		k := r.Table + "." + r.Column
		if !seen[k] {
			seen[k] = true
			fmt.Printf("    %s.%s\n", r.Table, r.Column)
		}
	}
	fmt.Println()
}

func printUnknownRefs(unknownRefs []columnRef) {
	if len(unknownRefs) == 0 {
		return
	}
	fmt.Printf("  ⚠ %d 个引用指向未知列（可能已被删除/重命名）:\n\n", len(unknownRefs))
	for _, r := range unknownRefs {
		fmt.Printf("    %s.%s ← %s:%d\n", r.Table, r.Column, r.File, r.Line)
	}
	fmt.Println()
}

func sortedFilteredKeys(tstats map[string]*tableStats, fn func(*tableStats) bool) []string {
	var keys []string
	for t, ts := range tstats {
		if fn(ts) {
			keys = append(keys, t)
		}
	}
	sort.Strings(keys)
	return keys
}

// ---------------------------------------------------------------------------
// DDL column extraction
// ---------------------------------------------------------------------------

var (
	createTableRe = regexp.MustCompile(`CREATE TABLE(?: IF NOT EXISTS)?\s+(\w+)`)
	columnDefRe   = regexp.MustCompile(`^\s*(\w+)\s+([A-Za-z0-9_]+(?:\([^)]*\))?(?:\s*\[\])?)\s*(.*?)(?:,?\s*)$`)
	alterColRe    = regexp.MustCompile(`ALTER TABLE\s+(\w+)\s+ADD COLUMN(?: IF NOT EXISTS)?\s+(\w+)\s+([A-Za-z0-9_]+(?:\([^)]*\))?)\s*(.*?)(?:,?\s*);?`)
)

func extractColumns(schemaPath string) ([]column, error) {
	f, err := os.Open(schemaPath)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	var columns []column
	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 0, 1024*1024), 1024*1024)
	lineNo := 0

	parser := &ddlParser{path: filepath.Base(schemaPath)}
	for scanner.Scan() {
		lineNo++
		parser.parseLine(scanner.Text(), lineNo, &columns)
	}

	if err := scanner.Err(); err != nil {
		return nil, err
	}
	return dedupeColumns(columns), nil
}

// ddlParser tracks state while scanning DDL statements.
type ddlParser struct {
	path          string
	currentTable  string
	inCreateTable bool
	parenDepth    int
}

func (p *ddlParser) parseLine(raw string, lineNo int, columns *[]column) {
	if matches := createTableRe.FindStringSubmatch(raw); matches != nil {
		p.currentTable = matches[1]
		p.inCreateTable = true
		p.parenDepth = strings.Count(raw, "(") - strings.Count(raw, ")")
		if p.parenDepth == 0 && strings.Contains(raw, "();") {
			p.inCreateTable = false
		}
		return
	}

	if p.inCreateTable {
		p.parseCreateBody(raw, lineNo, columns)
		return
	}

	if matches := alterColRe.FindStringSubmatch(raw); matches != nil {
		rest := strings.ToUpper(matches[4])
		*columns = append(*columns, column{
			Table:    matches[1],
			Name:     matches[2],
			Type:     strings.TrimSpace(matches[3]),
			Nullable: !strings.Contains(rest, "NOT NULL"),
			Source:   fmt.Sprintf("%s:%d", p.path, lineNo),
		})
	}
}

func (p *ddlParser) parseCreateBody(raw string, lineNo int, columns *[]column) {
	p.parenDepth += strings.Count(raw, "(") - strings.Count(raw, ")")

	trimmed := strings.TrimSpace(raw)
	if isConstraintLine(trimmed) {
		if p.parenDepth <= 0 {
			p.inCreateTable = false
		}
		return
	}

	if matches := columnDefRe.FindStringSubmatch(trimmed); matches != nil {
		rest := strings.ToUpper(matches[3])
		*columns = append(*columns, column{
			Table:    p.currentTable,
			Name:     matches[1],
			Type:     strings.TrimSpace(matches[2]),
			Nullable: !strings.Contains(rest, "NOT NULL"),
			Source:   fmt.Sprintf("%s:%d", p.path, lineNo),
		})
	}

	if p.parenDepth <= 0 {
		p.inCreateTable = false
		p.currentTable = ""
	}
}

func dedupeColumns(columns []column) []column {
	seen := make(map[string]bool)
	var deduped []column
	for _, c := range columns {
		key := strings.ToLower(c.Table) + "." + strings.ToLower(c.Name)
		if !seen[key] {
			seen[key] = true
			deduped = append(deduped, c)
		}
	}
	sort.Slice(deduped, func(i, j int) bool {
		if deduped[i].Table != deduped[j].Table {
			return deduped[i].Table < deduped[j].Table
		}
		return deduped[i].Name < deduped[j].Name
	})
	return deduped
}

func isConstraintLine(line string) bool {
	upper := strings.ToUpper(strings.TrimSpace(line))
	return strings.HasPrefix(upper, "PRIMARY KEY") ||
		strings.HasPrefix(upper, "UNIQUE") ||
		strings.HasPrefix(upper, "FOREIGN KEY") ||
		strings.HasPrefix(upper, "CHECK") ||
		strings.HasPrefix(upper, "CONSTRAINT") ||
		strings.HasPrefix(upper, ");") ||
		strings.HasPrefix(upper, ")")
}

// ---------------------------------------------------------------------------
// E2E column reference extraction
// ---------------------------------------------------------------------------

var (
	// selectRe matches SELECT ... FROM table.
	selectRe = regexp.MustCompile(`(?is)SELECT\s+(.+?)\s+FROM\s+(?:public\.)?["]?([a-zA-Z_][a-zA-Z0-9_]*)["]?`)
	// selectSQLRe matches SQL SELECT within string literals (backtick or single-quote).
	selectSQLRe = regexp.MustCompile("(?i)`(SELECT\\s+[^`]+?\\s+FROM\\s+[a-zA-Z_][a-zA-Z0-9_.]*[^`]*)`|'(SELECT\\s+[^']+?\\s+FROM\\s+[a-zA-Z_][a-zA-Z0-9_.]*[^']*)'")
)

func extractColumnRefs(packsDir, goE2EDir string) ([]columnRef, error) {
	var refs []columnRef

	entries, err := os.ReadDir(packsDir)
	if err != nil {
		return nil, fmt.Errorf("read packs dir: %w", err)
	}
	for _, entry := range entries {
		if !strings.HasSuffix(entry.Name(), ".ts") {
			continue
		}
		path := filepath.Join(packsDir, entry.Name())
		fileRefs, err := extractTSRefs(path)
		if err != nil {
			fmt.Fprintf(os.Stderr, "warn: skip %s: %v\n", path, err)
			continue
		}
		refs = append(refs, fileRefs...)
	}

	goEntries, err := os.ReadDir(goE2EDir)
	if err == nil {
		for _, entry := range goEntries {
			if !strings.HasSuffix(entry.Name(), ".go") {
				continue
			}
			path := filepath.Join(goE2EDir, entry.Name())
			fileRefs, err := extractGoRefs(path)
			if err != nil {
				fmt.Fprintf(os.Stderr, "warn: skip %s: %v\n", path, err)
				continue
			}
			refs = append(refs, fileRefs...)
		}
	}

	return refs, nil
}

func extractTSRefs(path string) ([]columnRef, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	content := string(data)
	var refs []columnRef

	for _, match := range selectSQLRe.FindAllStringSubmatch(content, -1) {
		sql := match[1]
		if sql == "" {
			sql = match[2]
		}
		for _, sel := range selectRe.FindAllStringSubmatch(sql, -1) {
			tableName := cleanTableName(sel[2])
			extractColumnsFromSelect(sel[1], tableName, path, &refs)
		}
	}

	return refs, nil
}

func extractGoRefs(path string) ([]columnRef, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	content := string(data)
	var refs []columnRef

	for _, match := range selectRe.FindAllStringSubmatch(content, -1) {
		cols := match[1]
		if strings.TrimSpace(cols) == "*" {
			continue
		}
		tableName := cleanTableName(match[2])
		extractColumnsFromSelect(cols, tableName, path, &refs)
	}

	return refs, nil
}

func extractColumnsFromSelect(cols, table, file string, refs *[]columnRef) {
	parts := splitColumns(cols)
	for _, part := range parts {
		part = strings.TrimSpace(part)
		if part == "" || part == "*" {
			continue
		}
		colName := extractColumnName(part)
		if colName == "" {
			continue
		}
		*refs = append(*refs, columnRef{
			Table:   table,
			Column:  colName,
			File:    filepath.Base(file),
			Line:    0,
			Snippet: strings.TrimSpace(part)[:min(60, len(strings.TrimSpace(part)))],
		})
	}
}

func splitColumns(s string) []string {
	var parts []string
	depth := 0
	start := 0
	for i, c := range s {
		switch c {
		case '(':
			depth++
		case ')':
			depth--
		case ',':
			if depth == 0 {
				parts = append(parts, s[start:i])
				start = i + 1
			}
		}
	}
	parts = append(parts, s[start:])
	return parts
}

func extractColumnName(expr string) string {
	expr = strings.TrimSpace(expr)

	if matched, _ := regexp.MatchString(
		`^(?i)(count|sum|avg|min|max|bool_or|array_agg|string_agg|json_agg)\s*\(`, expr); matched {
		return ""
	}

	// Strip ::cast first.
	if idx := strings.Index(expr, "::"); idx >= 0 {
		expr = strings.TrimSpace(expr[:idx])
	}

	// "expr IS NOT NULL AS alias" → extract column before IS.
	if idx := regexp.MustCompile(`(?i)\s+IS\s+(NOT\s+)?NULL`).FindStringIndex(expr); idx != nil {
		return lastIdentifier(strings.TrimSpace(expr[:idx[0]]))
	}

	// "expr AS alias" → extract column before AS.
	if idx := regexp.MustCompile(`(?i)\s+AS\s+\w+`).FindStringIndex(expr); idx != nil {
		return lastIdentifier(strings.TrimSpace(expr[:idx[0]]))
	}

	// "table.column" → "column".
	if idx := strings.LastIndex(expr, "."); idx >= 0 {
		return expr[idx+1:]
	}

	// Simple identifier.
	if matched, _ := regexp.MatchString(`^[a-zA-Z_][a-zA-Z0-9_]*$`, expr); matched {
		if isSQLKeyword(strings.ToUpper(expr)) {
			return ""
		}
		return expr
	}

	return lastIdentifier(expr)
}

func lastIdentifier(expr string) string {
	re := regexp.MustCompile(`[a-zA-Z_][a-zA-Z0-9_]*`)
	matches := re.FindAllString(expr, -1)
	if len(matches) == 0 {
		return ""
	}
	return matches[len(matches)-1]
}

func isSQLKeyword(s string) bool {
	return sqlKeywords[s]
}

var sqlKeywords = map[string]bool{
	"SELECT": true, "FROM": true, "WHERE": true, "AND": true, "OR": true,
	"NOT": true, "NULL": true, "IS": true, "AS": true, "IN": true,
	"JOIN": true, "LEFT": true, "RIGHT": true, "INNER": true, "OUTER": true,
	"ON": true, "ORDER": true, "BY": true, "GROUP": true, "HAVING": true,
	"LIMIT": true, "OFFSET": true, "INSERT": true, "UPDATE": true, "DELETE": true,
	"SET": true, "INTO": true, "VALUES": true, "DEFAULT": true, "EXISTS": true,
	"BETWEEN": true, "LIKE": true, "ILIKE": true, "CASE": true, "WHEN": true,
	"THEN": true, "ELSE": true, "END": true, "DISTINCT": true, "ALL": true,
	"UNION": true, "INTERSECT": true, "EXCEPT": true, "WITH": true, "RECURSIVE": true,
	"RETURNING": true, "NOW": true, "INTERVAL": true, "TRUE": true, "FALSE": true,
	"COUNT": true, "SUM": true, "AVG": true, "MIN": true, "MAX": true,
	// SQL type casts.
	"INT": true, "INTEGER": true, "BIGINT": true, "SMALLINT": true,
	"TEXT": true, "VARCHAR": true, "BOOL": true, "BOOLEAN": true,
	"UUID": true, "FLOAT": true, "FLOAT8": true, "NUMERIC": true,
	"JSONB": true, "JSON": true, "TIMESTAMPTZ": true, "TIMESTAMP": true,
	"DATE": true, "BYTEA": true, "CONFIG": true,
}

func cleanTableName(name string) string {
	name = strings.Trim(name, `"`)
	if idx := strings.LastIndex(name, "."); idx >= 0 {
		name = name[idx+1:]
	}
	return name
}

// ---------------------------------------------------------------------------
// JSON output
// ---------------------------------------------------------------------------

func outputJSON(r coverageResult) {
	type jsonUncoveredCol struct {
		Table    string `json:"table"`
		Column   string `json:"column"`
		Type     string `json:"type"`
		Nullable bool   `json:"nullable"`
	}
	type jsonTableStats struct {
		Table          string `json:"table"`
		TotalColumns   int    `json:"total_columns"`
		CoveredColumns int    `json:"covered_columns"`
		Queried        bool   `json:"queried_by_e2e"`
	}
	type jsonStaleRef struct {
		Table  string `json:"table"`
		Column string `json:"column"`
		File   string `json:"file"`
		Line   int    `json:"line"`
	}
	type result struct {
		TotalColumns    int                `json:"total_columns"`
		CoveredColumns  int                `json:"covered_columns"`
		CoveragePct     float64            `json:"coverage_pct"`
		Tables          []jsonTableStats   `json:"tables"`
		Uncovered       []jsonUncoveredCol `json:"uncovered_columns"`
		StaleReferences []jsonStaleRef     `json:"stale_references,omitempty"`
	}

	res := result{
		TotalColumns:   r.totalColumns,
		CoveredColumns: r.coveredCount,
		CoveragePct:    pct(r.coveredCount, r.totalColumns),
	}

	for _, u := range r.uncovered {
		res.Uncovered = append(res.Uncovered, jsonUncoveredCol{
			Table: u.col.Table, Column: u.col.Name,
			Type: u.col.Type, Nullable: u.col.Nullable,
		})
	}

	var tkeys []string
	for t := range r.tstats {
		tkeys = append(tkeys, t)
	}
	sort.Strings(tkeys)
	for _, t := range tkeys {
		ts := r.tstats[t]
		res.Tables = append(res.Tables, jsonTableStats{
			Table: t, TotalColumns: ts.totalColumns,
			CoveredColumns: ts.coveredColumns, Queried: ts.hasE2ERef,
		})
	}

	for _, s := range r.unknownRefs {
		res.StaleReferences = append(res.StaleReferences, jsonStaleRef{
			Table: s.Table, Column: s.Column, File: s.File, Line: s.Line,
		})
	}

	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	_ = enc.Encode(res)
}

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

func pct(part, total int) float64 {
	if total == 0 {
		return 0
	}
	return float64(part) / float64(total) * 100
}
