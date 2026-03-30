package site

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"math"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"

	_ "github.com/mattn/go-sqlite3"
)

// DBSchema 描述 ZeroNet 的 dbschema.json 子集。
type DBSchema struct {
	DBName  string                 `json:"db_name"`
	DBFile  string                 `json:"db_file"`
	Version int64                  `json:"version"`
	Maps    map[string]DBMapRule   `json:"maps"`
	Tables  map[string]DBTableSpec `json:"tables"`
}

// DBMapRule 定义某类 JSON 文件如何映射到表和 keyvalue。
type DBMapRule struct {
	ToTable    []any    `json:"to_table"`
	ToKeyvalue []string `json:"to_keyvalue"`
}

// DBTableSpec 定义站点自己的业务表结构。
type DBTableSpec struct {
	Cols    [][]string `json:"cols"`
	Indexes []string   `json:"indexes"`
}

type dbTableBinding struct {
	Node   string
	Table  string
	KeyCol string
	ValCol string
}

// queryDB 将站点 JSON 内容映射到内存 SQLite，再执行只读查询。
func (m *Manager) queryDB(siteAddress, query string) ([]map[string]any, error) {
	query = strings.TrimSpace(query)
	if query == "" {
		return []map[string]any{}, nil
	}

	db, err := m.siteDB(siteAddress)
	if err != nil {
		return []map[string]any{}, err
	}
	rows, err := db.Query(query)
	if err != nil {
		return []map[string]any{}, err
	}
	defer rows.Close()

	columns, err := rows.Columns()
	if err != nil {
		return []map[string]any{}, err
	}

	back := make([]map[string]any, 0)
	for rows.Next() {
		values := make([]any, len(columns))
		dest := make([]any, len(columns))
		for i := range values {
			dest[i] = &values[i]
		}
		if err := rows.Scan(dest...); err != nil {
			return []map[string]any{}, err
		}

		row := make(map[string]any, len(columns))
		for i, column := range columns {
			row[column] = normalizeDBValue(values[i])
		}
		back = append(back, row)
	}
	if err := rows.Err(); err != nil {
		return []map[string]any{}, err
	}
	return back, nil
}

func (m *Manager) siteDB(siteAddress string) (*sql.DB, error) {
	m.mu.Lock()
	if db := m.dbs[siteAddress]; db != nil {
		m.mu.Unlock()
		return db, nil
	}
	m.mu.Unlock()

	db, err := m.buildSiteDB(siteAddress)
	if err != nil {
		return nil, err
	}

	m.mu.Lock()
	defer m.mu.Unlock()
	if existing := m.dbs[siteAddress]; existing != nil {
		_ = db.Close()
		return existing, nil
	}
	m.dbs[siteAddress] = db
	return db, nil
}

func (m *Manager) buildSiteDB(siteAddress string) (*sql.DB, error) {
	// dbschema.json 是站点查询层的入口，缺少它就无法建立兼容查询。
	if err := m.NeedFile(siteAddress, "dbschema.json"); err != nil {
		return nil, fmt.Errorf("获取 dbschema.json 失败: %w", err)
	}

	rawSchema, err := m.ReadSiteFile(siteAddress, "dbschema.json", "text")
	if err != nil {
		return nil, err
	}

	var schema DBSchema
	if err := json.Unmarshal([]byte(rawSchema.(string)), &schema); err != nil {
		return nil, fmt.Errorf("解析 dbschema.json 失败: %w", err)
	}

	dbPath := m.siteFilePath(siteAddress, schema.DBFile)
	if err := os.MkdirAll(filepath.Dir(dbPath), 0o755); err != nil {
		return nil, fmt.Errorf("创建站点数据库目录失败: %w", err)
	}
	// 每次重建都从干净数据库开始，避免旧索引残留导致查询结果和当前 JSON 不一致。
	if err := os.Remove(dbPath); err != nil && !os.IsNotExist(err) {
		return nil, fmt.Errorf("删除旧站点数据库失败: %w", err)
	}

	db, err := sql.Open("sqlite3", dbPath)
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	if err := initializeSiteDB(db, &schema); err != nil {
		_ = db.Close()
		return nil, err
	}

	if err := m.ensureMappedFiles(siteAddress, &schema); err != nil {
		_ = db.Close()
		return nil, err
	}
	files, err := m.collectMappedFiles(siteAddress, &schema)
	if err != nil {
		_ = db.Close()
		return nil, err
	}
	for _, innerPath := range files {
		if err := m.importMappedFile(db, siteAddress, innerPath, &schema); err != nil {
			_ = db.Close()
			return nil, err
		}
	}
	return db, nil
}

func initializeSiteDB(db *sql.DB, schema *DBSchema) error {
	baseStatements := []string{
		`CREATE TABLE json (json_id INTEGER PRIMARY KEY AUTOINCREMENT, directory TEXT NOT NULL, file_name TEXT NOT NULL)`,
		`CREATE UNIQUE INDEX json_path ON json(directory, file_name)`,
		`CREATE TABLE keyvalue (json_id INTEGER NOT NULL, key TEXT NOT NULL, value TEXT)`,
		`CREATE INDEX keyvalue_lookup ON keyvalue(json_id, key)`,
	}
	for _, statement := range baseStatements {
		if _, err := db.Exec(statement); err != nil {
			return err
		}
	}

	for tableName, tableSpec := range schema.Tables {
		columns := make([]string, 0, len(tableSpec.Cols))
		for _, column := range tableSpec.Cols {
			if len(column) < 2 {
				continue
			}
			columns = append(columns, fmt.Sprintf(`%s %s`, quoteIdent(column[0]), column[1]))
		}
		if len(columns) == 0 {
			continue
		}
		statement := fmt.Sprintf(`CREATE TABLE %s (%s)`, quoteIdent(tableName), strings.Join(columns, ", "))
		if _, err := db.Exec(statement); err != nil {
			return err
		}
		for _, indexStatement := range tableSpec.Indexes {
			if _, err := db.Exec(indexStatement); err != nil {
				return err
			}
		}
	}
	return nil
}

func (m *Manager) ensureMappedFiles(siteAddress string, schema *DBSchema) error {
	if _, err := m.EnsureRootContent(siteAddress); err != nil {
		return err
	}

	// 字面量路径直接拉取；正则路径则从已有 content.json 里推导声明文件。
	for pattern := range schema.Maps {
		if isLiteralMapPattern(pattern) {
			_ = m.NeedFile(siteAddress, innerPathFromSchemaPath(pattern))
		}
	}

	contentPaths, err := m.listChildContentPaths(siteAddress, "content.json")
	if err != nil {
		return err
	}
	if len(contentPaths) == 0 {
		contentPaths = []string{"content.json"}
	}
	sort.Strings(contentPaths)

	matchers := compileMapMatchers(schema)
	for _, contentPath := range contentPaths {
		content, err := m.ensureContent(siteAddress, contentPath)
		if err != nil || content == nil {
			continue
		}
		for relativePath := range mergeContentFiles(content) {
			resolvedPath := resolveContentFilePath(contentPath, relativePath)
			if !matchesAnyMap(schemaPathForInnerPath(resolvedPath), matchers) {
				continue
			}
			_ = m.NeedFile(siteAddress, resolvedPath)
		}
	}
	return nil
}

func (m *Manager) collectMappedFiles(siteAddress string, schema *DBSchema) ([]string, error) {
	root := m.siteFilePath(siteAddress, "")
	matchers := compileMapMatchers(schema)
	var files []string
	err := filepath.WalkDir(root, func(fullPath string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".json") {
			return nil
		}
		relativePath, err := filepath.Rel(root, fullPath)
		if err != nil {
			return err
		}
		innerPath := filepath.ToSlash(relativePath)
		if matchesAnyMap(schemaPathForInnerPath(innerPath), matchers) {
			files = append(files, innerPath)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	sort.Strings(files)
	return files, nil
}

func (m *Manager) importMappedFile(db *sql.DB, siteAddress, innerPath string, schema *DBSchema) error {
	schemaPath := schemaPathForInnerPath(innerPath)
	rule, ok := schemaRuleForPath(schemaPath, schema)
	if !ok {
		return nil
	}

	raw, err := m.ReadSiteFile(siteAddress, innerPath, "text")
	if err != nil {
		return err
	}

	var payload map[string]any
	if err := json.Unmarshal([]byte(raw.(string)), &payload); err != nil {
		return nil
	}

	jsonID, err := insertJSONRow(db, schemaPath)
	if err != nil {
		return err
	}
	for _, key := range rule.ToKeyvalue {
		if value, ok := payload[key]; ok {
			if err := insertKeyValue(db, jsonID, key, stringifyJSONValue(value)); err != nil {
				return err
			}
		}
	}
	for _, binding := range parseTableBindings(rule.ToTable) {
		if err := insertMappedNode(db, jsonID, payload, binding, schema); err != nil {
			return err
		}
	}
	return nil
}

func insertJSONRow(db *sql.DB, innerPath string) (int64, error) {
	directory := pathDir(innerPath)
	fileName := path.Base(innerPath)
	result, err := db.Exec(`INSERT INTO json(directory, file_name) VALUES(?, ?)`, directory, fileName)
	if err != nil {
		return 0, err
	}
	return result.LastInsertId()
}

func insertKeyValue(db *sql.DB, jsonID int64, key, value string) error {
	_, err := db.Exec(`INSERT INTO keyvalue(json_id, key, value) VALUES(?, ?, ?)`, jsonID, key, value)
	return err
}

func insertMappedNode(db *sql.DB, jsonID int64, payload map[string]any, binding dbTableBinding, schema *DBSchema) error {
	nodeValue, ok := payload[binding.Node]
	if !ok {
		return nil
	}

	switch typed := nodeValue.(type) {
	case []any:
		for _, item := range typed {
			row, ok := item.(map[string]any)
			if !ok {
				continue
			}
			if err := insertTableRow(db, binding.Table, row, jsonID, schema); err != nil {
				return err
			}
		}
	case map[string]any:
		if binding.KeyCol != "" && binding.ValCol != "" {
			for key, value := range typed {
				row := map[string]any{
					binding.KeyCol: key,
					binding.ValCol: value,
				}
				if err := insertTableRow(db, binding.Table, row, jsonID, schema); err != nil {
					return err
				}
			}
		}
	}
	return nil
}

func insertTableRow(db *sql.DB, tableName string, row map[string]any, jsonID int64, schema *DBSchema) error {
	tableSpec, ok := schema.Tables[tableName]
	if !ok {
		return nil
	}

	columns := make([]string, 0, len(tableSpec.Cols))
	values := make([]any, 0, len(tableSpec.Cols))
	for _, column := range tableSpec.Cols {
		if len(column) < 1 {
			continue
		}
		columnName := column[0]
		if columnName == "json_id" {
			columns = append(columns, quoteIdent(columnName))
			values = append(values, jsonID)
			continue
		}
		value, ok := row[columnName]
		if !ok {
			continue
		}
		columns = append(columns, quoteIdent(columnName))
		values = append(values, normalizeJSONScalar(value))
	}
	if len(columns) == 0 {
		return nil
	}

	holders := make([]string, len(columns))
	for i := range holders {
		holders[i] = "?"
	}
	statement := fmt.Sprintf(`INSERT INTO %s(%s) VALUES(%s)`,
		quoteIdent(tableName),
		strings.Join(columns, ", "),
		strings.Join(holders, ", "),
	)
	_, err := db.Exec(statement, values...)
	return err
}

func schemaRuleForPath(innerPath string, schema *DBSchema) (DBMapRule, bool) {
	for pattern, rule := range schema.Maps {
		regex, err := regexp.Compile("^" + pattern + "$")
		if err != nil {
			continue
		}
		if regex.MatchString(innerPath) {
			return rule, true
		}
	}
	return DBMapRule{}, false
}

func compileMapMatchers(schema *DBSchema) []*regexp.Regexp {
	matchers := make([]*regexp.Regexp, 0, len(schema.Maps))
	for pattern := range schema.Maps {
		regex, err := regexp.Compile("^" + pattern + "$")
		if err != nil {
			continue
		}
		matchers = append(matchers, regex)
	}
	return matchers
}

func matchesAnyMap(innerPath string, matchers []*regexp.Regexp) bool {
	for _, matcher := range matchers {
		if matcher.MatchString(innerPath) {
			return true
		}
	}
	return false
}

func schemaPathForInnerPath(innerPath string) string {
	innerPath = strings.TrimPrefix(filepath.ToSlash(innerPath), "/")
	if strings.HasPrefix(innerPath, "data/") {
		return strings.TrimPrefix(innerPath, "data/")
	}
	return innerPath
}

func innerPathFromSchemaPath(schemaPath string) string {
	schemaPath = strings.TrimPrefix(filepath.ToSlash(schemaPath), "/")
	if schemaPath == "" {
		return schemaPath
	}
	if strings.HasPrefix(schemaPath, "users/") || schemaPath == "data.json" || strings.HasPrefix(schemaPath, "data/") {
		return "data/" + schemaPath
	}
	return schemaPath
}

func isLiteralMapPattern(pattern string) bool {
	return !strings.ContainsAny(pattern, `+*?[](){}|\^$`)
}

func parseTableBindings(rawBindings []any) []dbTableBinding {
	back := make([]dbTableBinding, 0, len(rawBindings))
	for _, item := range rawBindings {
		switch typed := item.(type) {
		case string:
			back = append(back, dbTableBinding{
				Node:  typed,
				Table: typed,
			})
		case map[string]any:
			binding := dbTableBinding{
				Node:   anyToString(typed["node"]),
				Table:  anyToString(typed["table"]),
				KeyCol: anyToString(typed["key_col"]),
				ValCol: anyToString(typed["val_col"]),
			}
			if binding.Node == "" || binding.Table == "" {
				continue
			}
			back = append(back, binding)
		}
	}
	return back
}

func anyToString(value any) string {
	switch typed := value.(type) {
	case string:
		return typed
	case []byte:
		return string(typed)
	default:
		return ""
	}
}

func normalizeJSONScalar(value any) any {
	switch typed := value.(type) {
	case nil:
		return nil
	case bool:
		if typed {
			return int64(1)
		}
		return int64(0)
	case float64:
		if math.Trunc(typed) == typed {
			return int64(typed)
		}
		return typed
	case float32:
		if math.Trunc(float64(typed)) == float64(typed) {
			return int64(typed)
		}
		return float64(typed)
	case json.Number:
		if integer, err := typed.Int64(); err == nil {
			return integer
		}
		if floatValue, err := typed.Float64(); err == nil {
			return floatValue
		}
		return typed.String()
	case string:
		return typed
	default:
		return stringifyJSONValue(typed)
	}
}

func stringifyJSONValue(value any) string {
	switch typed := value.(type) {
	case nil:
		return ""
	case string:
		return typed
	case bool:
		if typed {
			return "true"
		}
		return "false"
	case float64:
		if math.Trunc(typed) == typed {
			return strconv.FormatInt(int64(typed), 10)
		}
		return strconv.FormatFloat(typed, 'f', -1, 64)
	case float32:
		if math.Trunc(float64(typed)) == float64(typed) {
			return strconv.FormatInt(int64(typed), 10)
		}
		return strconv.FormatFloat(float64(typed), 'f', -1, 64)
	case int, int8, int16, int32, int64:
		return strconv.FormatInt(anyToInt64(typed), 10)
	case uint, uint8, uint16, uint32, uint64:
		return strconv.FormatInt(anyToInt64(typed), 10)
	default:
		raw, err := json.Marshal(typed)
		if err != nil {
			return fmt.Sprint(typed)
		}
		return string(raw)
	}
}

func normalizeDBValue(value any) any {
	switch typed := value.(type) {
	case []byte:
		return string(typed)
	default:
		return typed
	}
}

func quoteIdent(name string) string {
	return `"` + strings.ReplaceAll(name, `"`, `""`) + `"`
}
