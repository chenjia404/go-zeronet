package site

import (
	"crypto/rand"
	"crypto/sha256"
	"crypto/sha512"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io/fs"
	"math"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/btcsuite/btcd/btcec/v2"
	"github.com/btcsuite/btcd/btcec/v2/ecdsa"
	"github.com/btcsuite/btcd/btcutil"
	"github.com/btcsuite/btcd/chaincfg"
)

var optionalPathPattern = regexp.MustCompile(`(?i)\.(zip|tar|gz|bz2|7z|mp4|mkv|avi|mov|webm|mp3|ogg|flac|pdf|epub)$`)

// SiteCreation 保存 CLI 创建站点后的结果。
type SiteCreation struct {
	Address    string
	PrivateKey string
}

// GenerateSitePrivateKey 生成一个新的 ZeroNet 站点私钥。
func GenerateSitePrivateKey() (string, string, error) {
	privateKey, err := btcec.NewPrivateKey()
	if err != nil {
		return "", "", fmt.Errorf("生成私钥失败: %w", err)
	}
	wif, err := btcutil.NewWIF(privateKey, &chaincfg.MainNetParams, true)
	if err != nil {
		return "", "", fmt.Errorf("编码 WIF 失败: %w", err)
	}
	address, err := privateKeyToAddress(wif.String())
	if err != nil {
		return "", "", err
	}
	return wif.String(), address, nil
}

func privateKeyToAddress(privateKey string) (string, error) {
	wif, err := btcutil.DecodeWIF(privateKey)
	if err != nil {
		return "", fmt.Errorf("解析 WIF 失败: %w", err)
	}
	serialized := wif.PrivKey.PubKey().SerializeCompressed()
	address, err := btcutil.NewAddressPubKey(serialized, &chaincfg.MainNetParams)
	if err != nil {
		return "", fmt.Errorf("生成地址失败: %w", err)
	}
	return address.EncodeAddress(), nil
}

func signBitcoinMessage(message, privateKey string) (string, error) {
	wif, err := btcutil.DecodeWIF(privateKey)
	if err != nil {
		return "", fmt.Errorf("解析 WIF 失败: %w", err)
	}
	hash := bitcoinMessageHash(message)
	signature := ecdsa.SignCompact(wif.PrivKey, hash, true)
	return base64.StdEncoding.EncodeToString(signature), nil
}

func bitcoinMessageHash(message string) []byte {
	magic := append([]byte{0x18}, []byte("Bitcoin Signed Message:\n")...)
	payload := append(magic, insaneInt(len(message))...)
	payload = append(payload, []byte(message)...)

	first := sha256.Sum256(payload)
	second := sha256.Sum256(first[:])
	return second[:]
}

func insaneInt(value int) []byte {
	switch {
	case value < 253:
		return []byte{byte(value)}
	case value < 65536:
		return append([]byte{253}, encodeLittleEndian(uint64(value), 2)...)
	case value < 4294967296:
		return append([]byte{254}, encodeLittleEndian(uint64(value), 4)...)
	default:
		return append([]byte{255}, encodeLittleEndian(uint64(value), 8)...)
	}
}

func encodeLittleEndian(value uint64, size int) []byte {
	back := make([]byte, size)
	for i := 0; i < size; i++ {
		back[i] = byte(value & 0xff)
		value >>= 8
	}
	return back
}

// CreateSite 创建一个最小可签名发布的新站点。
func (m *Manager) CreateSite(title, description string) (*SiteCreation, error) {
	privateKey, siteAddress, err := GenerateSitePrivateKey()
	if err != nil {
		return nil, err
	}

	if title == "" {
		title = siteAddress
	}
	indexHTML := buildStarterIndex(title, description)
	if err := m.writeSiteFile(siteAddress, "index.html", []byte(indexHTML)); err != nil {
		return nil, err
	}
	if err := m.SignContent(siteAddress, "content.json", privateKey, map[string]any{
		"title":       title,
		"description": description,
	}, true); err != nil {
		return nil, err
	}
	if err := m.store.setPrivateKey(siteAddress, privateKey); err != nil {
		return nil, err
	}
	return &SiteCreation{Address: siteAddress, PrivateKey: privateKey}, nil
}

// CloneSite 从一个本地已有站点复制站点文件并重签名。
func (m *Manager) CloneSite(sourceAddress string) (*SiteCreation, error) {
	privateKey, siteAddress, err := GenerateSitePrivateKey()
	if err != nil {
		return nil, err
	}

	sourceRoot := filepath.Join(m.dataDir, sourceAddress)
	if _, err := os.Stat(sourceRoot); err != nil {
		return nil, fmt.Errorf("源站点不存在: %w", err)
	}
	// 先尽量补齐模板运行必需的静态文件，但这里不强依赖 peer。
	// 如果当前没有可用 peer，后续会保留源站点的静态文件清单，访问时再按需回源。
	_ = m.prepareCloneSource(sourceAddress)

	targetRoot := filepath.Join(m.dataDir, siteAddress)
	if err := cloneSiteTree(sourceRoot, targetRoot); err != nil {
		return nil, err
	}
	if err := rewriteRootContent(targetRoot, siteAddress, sourceAddress); err != nil {
		return nil, err
	}
	if err := m.store.setPrivateKey(siteAddress, privateKey); err != nil {
		return nil, err
	}
	if err := m.SignAllContents(siteAddress, privateKey); err != nil {
		return nil, err
	}
	if err := m.reconcileClonedRootContent(siteAddress, sourceAddress, privateKey); err != nil {
		return nil, err
	}
	if err := m.initializeClonedZeroBlog(siteAddress, sourceAddress, privateKey); err != nil {
		return nil, err
	}
	return &SiteCreation{Address: siteAddress, PrivateKey: privateKey}, nil
}

// prepareCloneSource 确保克隆模板需要的根文件已同步到本地，但不会拉取文章正文。
func (m *Manager) prepareCloneSource(siteAddress string) error {
	content, err := m.EnsureRootContent(siteAddress)
	if err != nil {
		return err
	}

	required := make([]string, 0, len(content.Files)+len(content.FilesOptional))
	for relativePath := range content.Files {
		if shouldSkipClonedContent(relativePath) {
			continue
		}
		required = append(required, relativePath)
	}
	for relativePath := range content.FilesOptional {
		if shouldSkipClonedContent(relativePath) {
			continue
		}
		required = append(required, relativePath)
	}
	sort.Strings(required)

	for _, relativePath := range required {
		fullPath := m.siteFilePath(siteAddress, relativePath)
		if _, statErr := os.Stat(fullPath); statErr == nil {
			continue
		}
		if err := m.NeedFile(siteAddress, relativePath); err != nil {
			return fmt.Errorf("同步克隆依赖 %s 失败: %w", relativePath, err)
		}
	}
	return nil
}

// SignAllContents 对站点内所有 content.json 重新签名。
func (m *Manager) SignAllContents(siteAddress, privateKey string) error {
	contentPaths, err := m.listAllContentPaths(siteAddress)
	if err != nil {
		return err
	}
	// 子 content 先签，根 content 最后签，保证 include 树先稳定。
	sort.Slice(contentPaths, func(i, j int) bool {
		if contentPaths[i] == "content.json" {
			return false
		}
		if contentPaths[j] == "content.json" {
			return true
		}
		return contentPaths[i] > contentPaths[j]
	})
	for _, innerPath := range contentPaths {
		if err := m.SignContent(siteAddress, innerPath, privateKey, nil, true); err != nil {
			return err
		}
	}
	return nil
}

// SignContent 重新生成并签名指定 content.json。
func (m *Manager) SignContent(siteAddress, innerPath, privateKey string, extend map[string]any, removeMissingOptional bool) error {
	return m.signContent(siteAddress, innerPath, privateKey, extend, removeMissingOptional, nil)
}

func (m *Manager) signContent(siteAddress, innerPath, privateKey string, extend map[string]any, removeMissingOptional bool, preserveMissing map[string]map[string]any) error {
	if !strings.HasSuffix(innerPath, "content.json") {
		return fmt.Errorf("只能签名 content.json")
	}
	if privateKey == "" {
		privateKey = m.PrivateKey(siteAddress)
	}
	if privateKey == "" {
		return fmt.Errorf("站点 %s 没有已存储私钥", siteAddress)
	}
	privateKeyAddress, err := privateKeyToAddress(privateKey)
	if err != nil {
		return err
	}
	if privateKeyAddress != siteAddress {
		return fmt.Errorf("私钥地址不匹配: got=%s want=%s", privateKeyAddress, siteAddress)
	}

	contentPath := m.siteFilePath(siteAddress, innerPath)
	currentContent := map[string]any{
		"files": map[string]any{},
		"signs": map[string]any{},
	}
	if raw, err := os.ReadFile(contentPath); err == nil {
		if err := json.Unmarshal(raw, &currentContent); err != nil {
			return fmt.Errorf("解析 %s 失败: %w", innerPath, err)
		}
	}

	files, optionalFiles, err := m.hashContentFiles(siteAddress, innerPath, currentContent)
	if err != nil {
		return err
	}
	if !removeMissingOptional {
		if existingOptional, ok := currentContent["files_optional"].(map[string]any); ok {
			for key, value := range existingOptional {
				if _, exists := optionalFiles[key]; !exists {
					optionalFiles[key] = value
				}
			}
		}
	}
	if preserveMissing != nil {
		mergeMissingManifestEntries(files, preserveMissing["files"])
		mergeMissingManifestEntries(optionalFiles, preserveMissing["files_optional"])
	}

	newContent := cloneJSONObject(currentContent)
	for key, value := range extend {
		if _, ok := newContent[key]; !ok {
			newContent[key] = value
		}
	}
	newContent["files"] = files
	if len(optionalFiles) > 0 {
		newContent["files_optional"] = optionalFiles
	} else {
		delete(newContent, "files_optional")
	}
	delete(newContent, "signs")
	delete(newContent, "sign")
	newContent["modified"] = time.Now().Unix()
	newContent["address"] = siteAddress
	newContent["inner_path"] = innerPath
	if innerPath == "content.json" {
		if _, ok := newContent["title"]; !ok {
			newContent["title"] = siteAddress
		}
		if _, ok := newContent["description"]; !ok {
			newContent["description"] = ""
		}
		if _, ok := newContent["signs_required"]; !ok {
			newContent["signs_required"] = int64(1)
		}
		if _, ok := newContent["ignore"]; !ok {
			newContent["ignore"] = ""
		}
		signersData := fmt.Sprintf("%v:%s", newContent["signs_required"], siteAddress)
		signersSign, err := signBitcoinMessage(signersData, privateKey)
		if err != nil {
			return err
		}
		newContent["signers_sign"] = signersSign
	}

	signContent := pythonJSONDumps(newContent)
	signature, err := signBitcoinMessage(signContent, privateKey)
	if err != nil {
		return err
	}
	newContent["signs"] = map[string]any{siteAddress: signature}

	serialized := pythonJSONDumps(newContent)
	if err := m.writeSiteFile(siteAddress, innerPath, []byte(serialized)); err != nil {
		return err
	}
	m.setCachedContent(siteAddress, innerPath, nil)
	return nil
}

func mergeMissingManifestEntries(target, source map[string]any) {
	for key, value := range source {
		if _, exists := target[key]; !exists {
			target[key] = value
		}
	}
}

func (m *Manager) reconcileClonedRootContent(siteAddress, sourceAddress, privateKey string) error {
	preserveMissing, err := preservedCloneManifest(m.siteFilePath(sourceAddress, "content.json"))
	if err != nil {
		return err
	}
	return m.signContent(siteAddress, "content.json", privateKey, nil, true, preserveMissing)
}

func preservedCloneManifest(contentPath string) (map[string]map[string]any, error) {
	raw, err := os.ReadFile(contentPath)
	if err != nil {
		return nil, err
	}
	var content map[string]any
	if err := json.Unmarshal(raw, &content); err != nil {
		return nil, err
	}
	return map[string]map[string]any{
		"files":          filterCloneManifestEntries(content["files"]),
		"files_optional": filterCloneManifestEntries(content["files_optional"]),
	}, nil
}

func filterCloneManifestEntries(value any) map[string]any {
	typed, ok := value.(map[string]any)
	if !ok {
		return map[string]any{}
	}
	filtered := make(map[string]any)
	for relativePath, fileInfo := range typed {
		if shouldSkipClonedContent(relativePath) {
			continue
		}
		filtered[relativePath] = fileInfo
	}
	return filtered
}

// initializeClonedZeroBlog 为 ZeroBlog 克隆站点生成一份空的 data/data.json，
// 这样新站点首次打开后就能直接创建文章，而不会因为缺少博客数据文件而卡住。
func (m *Manager) initializeClonedZeroBlog(siteAddress, sourceAddress, privateKey string) error {
	schemaPath := m.siteFilePath(sourceAddress, "dbschema.json")
	rawSchema, err := os.ReadFile(schemaPath)
	if err != nil {
		return nil
	}

	var schema map[string]any
	if err := json.Unmarshal(rawSchema, &schema); err != nil {
		return nil
	}
	if dbName, _ := schema["db_name"].(string); dbName != "ZeroBlog" {
		return nil
	}

	data := emptyZeroBlogData(siteAddress)
	if sourceData, err := readZeroBlogDataTemplate(m.siteFilePath(sourceAddress, "data/data.json")); err == nil {
		for _, key := range []string{"title", "description", "links", "demo"} {
			if value, ok := sourceData[key]; ok {
				data[key] = value
			}
		}
	}
	if data["title"] == "" {
		if rootContent, err := m.EnsureRootContent(siteAddress); err == nil && rootContent != nil {
			data["title"] = rootContent.Title
		}
	}

	serialized := pythonJSONDumps(data)
	if err := m.writeSiteFile(siteAddress, "data/data.json", []byte(serialized)); err != nil {
		return err
	}
	preserveMissing, err := preservedCloneManifest(m.siteFilePath(sourceAddress, "content.json"))
	if err != nil {
		return err
	}
	return m.signContent(siteAddress, "content.json", privateKey, nil, true, preserveMissing)
}

func readZeroBlogDataTemplate(dataPath string) (map[string]any, error) {
	raw, err := os.ReadFile(dataPath)
	if err != nil {
		return nil, err
	}
	var data map[string]any
	if err := json.Unmarshal(raw, &data); err != nil {
		return nil, err
	}
	return data, nil
}

func emptyZeroBlogData(siteAddress string) map[string]any {
	return map[string]any{
		"title":        siteAddress,
		"description":  "",
		"links":        "",
		"next_post_id": int64(1),
		"demo":         false,
		"modified":     time.Now().Unix(),
		"post":         []any{},
	}
}

// PublishSite 将已签名的 content.json 更新推送给已知 peer。
func (m *Manager) PublishSite(siteAddress, innerPath string, sign bool) (int, error) {
	if innerPath == "" {
		innerPath = "content.json"
	}
	if !strings.HasSuffix(innerPath, "content.json") {
		if fileInfo, err := m.lookupFile(siteAddress, innerPath); err == nil {
			_ = fileInfo
			baseDir := pathDir(innerPath)
			if baseDir == "" {
				innerPath = "content.json"
			} else {
				innerPath = baseDir + "/content.json"
			}
		} else {
			baseDir := pathDir(innerPath)
			if baseDir == "" {
				innerPath = "content.json"
			} else {
				innerPath = baseDir + "/content.json"
			}
		}
	}
	if sign {
		if err := m.SignContent(siteAddress, innerPath, "", nil, true); err != nil {
			return 0, err
		}
	}
	raw, err := os.ReadFile(m.siteFilePath(siteAddress, innerPath))
	if err != nil {
		return 0, err
	}
	var content map[string]any
	if err := json.Unmarshal(raw, &content); err != nil {
		return 0, err
	}
	modified := anyToInt64(content["modified"])

	m.announceSite(siteAddress)
	peers := m.peers()
	published := 0
	var lastErr error
	for _, peer := range peers {
		client, release, err := m.clientForPeer(peer)
		if err != nil {
			lastErr = err
			continue
		}
		resp, err := client.Command("update", map[string]any{
			"site":       siteAddress,
			"inner_path": innerPath,
			"body":       raw,
			"modified":   modified,
			"diffs":      []any{},
		})
		release()
		if err != nil {
			lastErr = err
			continue
		}
		if errText, ok := resp["error"].(string); ok && errText != "" {
			lastErr = fmt.Errorf(errText)
			continue
		}
		published++
	}
	if published == 0 && lastErr != nil {
		return 0, lastErr
	}
	return published, nil
}

// WriteSiteData 保存站点文件内容。
func (m *Manager) WriteSiteData(siteAddress, innerPath string, raw []byte) error {
	if !m.IsOwned(siteAddress) {
		return fmt.Errorf("站点 %s 不属于当前用户", siteAddress)
	}
	if err := m.writeSiteFile(siteAddress, innerPath, raw); err != nil {
		return err
	}
	if strings.HasSuffix(innerPath, "content.json") {
		m.setCachedContent(siteAddress, innerPath, nil)
	}
	return nil
}

// DeleteSiteData 删除站点文件。
func (m *Manager) DeleteSiteData(siteAddress, innerPath string) error {
	if !m.IsOwned(siteAddress) {
		return fmt.Errorf("站点 %s 不属于当前用户", siteAddress)
	}
	fullPath := m.siteFilePath(siteAddress, innerPath)
	if err := os.Remove(fullPath); err != nil {
		return err
	}
	if strings.HasSuffix(innerPath, "content.json") {
		m.setCachedContent(siteAddress, innerPath, nil)
	}
	m.invalidateDB(siteAddress)
	return nil
}

// ListSiteFiles 返回目录下的相对文件列表。
func (m *Manager) ListSiteFiles(siteAddress, dirInnerPath string) ([]string, error) {
	root := m.siteFilePath(siteAddress, dirInnerPath)
	var files []string
	err := filepath.WalkDir(root, func(pathValue string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() {
			return nil
		}
		relativePath, err := filepath.Rel(root, pathValue)
		if err != nil {
			return err
		}
		files = append(files, filepath.ToSlash(relativePath))
		return nil
	})
	if err != nil {
		return nil, err
	}
	sort.Strings(files)
	return files, nil
}

// IsOwned 返回当前节点是否持有站点私钥。
func (m *Manager) IsOwned(siteAddress string) bool {
	return m.store != nil && m.store.hasSite(siteAddress)
}

// PrivateKey 返回站点已保存的私钥。
func (m *Manager) PrivateKey(siteAddress string) string {
	if m.store == nil {
		return ""
	}
	return m.store.privateKey(siteAddress)
}

// OwnedSites 返回当前节点持有私钥的站点地址列表。
func (m *Manager) OwnedSites() []string {
	if m.store == nil {
		return nil
	}
	return m.store.listSites()
}

func (m *Manager) listAllContentPaths(siteAddress string) ([]string, error) {
	root := m.siteFilePath(siteAddress, "")
	var contentPaths []string
	err := filepath.WalkDir(root, func(fullPath string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() || entry.Name() != "content.json" {
			return nil
		}
		relativePath, err := filepath.Rel(root, fullPath)
		if err != nil {
			return err
		}
		contentPaths = append(contentPaths, filepath.ToSlash(relativePath))
		return nil
	})
	if err != nil {
		return nil, err
	}
	sort.Strings(contentPaths)
	return contentPaths, nil
}

func (m *Manager) hashContentFiles(siteAddress, contentInnerPath string, content map[string]any) (map[string]any, map[string]any, error) {
	baseDir := pathDir(contentInnerPath)
	root := m.siteFilePath(siteAddress, baseDir)
	files := make(map[string]any)
	optionalFiles := make(map[string]any)
	ignorePattern := toRegexp(content["ignore"])
	dbInnerPath := m.dbInnerPath(siteAddress)

	err := filepath.WalkDir(root, func(fullPath string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() {
			return nil
		}

		relativePath, err := filepath.Rel(root, fullPath)
		if err != nil {
			return err
		}
		relativePath = filepath.ToSlash(relativePath)
		fileName := filepath.Base(relativePath)
		switch {
		case fileName == "content.json":
			return nil
		case strings.HasPrefix(fileName, "."):
			return nil
		case strings.HasSuffix(fileName, "-old"), strings.HasSuffix(fileName, "-new"):
			return nil
		case baseDir == "" && dbInnerPath != "" && strings.HasPrefix(relativePath, dbInnerPath):
			return nil
		case ignorePattern != nil && ignorePattern.MatchString(relativePath):
			return nil
		}

		raw, err := os.ReadFile(fullPath)
		if err != nil {
			return err
		}
		sum := sha256TruncatedHex(raw)
		node := map[string]any{
			"sha512": sum,
			"size":   int64(len(raw)),
		}
		if optionalPathPattern.MatchString(relativePath) {
			optionalFiles[relativePath] = node
		} else {
			files[relativePath] = node
		}
		return nil
	})
	if err != nil {
		return nil, nil, err
	}
	return files, optionalFiles, nil
}

func (m *Manager) dbInnerPath(siteAddress string) string {
	raw, err := os.ReadFile(m.siteFilePath(siteAddress, "dbschema.json"))
	if err != nil {
		return ""
	}
	var schema map[string]any
	if err := json.Unmarshal(raw, &schema); err != nil {
		return ""
	}
	pathValue, _ := schema["db_file"].(string)
	return strings.TrimPrefix(filepath.ToSlash(pathValue), "/")
}

func sha256TruncatedHex(raw []byte) string {
	return sha512Truncated(raw)
}

func sha512Truncated(raw []byte) string {
	full := sha512.Sum512(raw)
	return strings.ToLower(hex.EncodeToString(full[:32]))
}

func toRegexp(value any) *regexp.Regexp {
	pattern, ok := value.(string)
	if !ok || pattern == "" {
		return nil
	}
	regex, err := regexp.Compile(pattern)
	if err != nil {
		return nil
	}
	return regex
}

func cloneJSONObject(source map[string]any) map[string]any {
	back := make(map[string]any, len(source))
	for key, value := range source {
		back[key] = cloneJSONValue(value)
	}
	return back
}

func cloneJSONValue(value any) any {
	switch typed := value.(type) {
	case map[string]any:
		return cloneJSONObject(typed)
	case []any:
		back := make([]any, len(typed))
		for i, item := range typed {
			back[i] = cloneJSONValue(item)
		}
		return back
	default:
		return typed
	}
}

func pythonJSONDumps(value any) string {
	var builder strings.Builder
	writePythonJSON(&builder, value)
	return builder.String()
}

func writePythonJSON(builder *strings.Builder, value any) {
	switch typed := value.(type) {
	case nil:
		builder.WriteString("null")
	case bool:
		if typed {
			builder.WriteString("true")
		} else {
			builder.WriteString("false")
		}
	case string:
		builder.WriteString(pythonJSONString(typed))
	case json.Number:
		builder.WriteString(typed.String())
	case float64:
		builder.WriteString(formatJSONNumber(typed))
	case float32:
		builder.WriteString(formatJSONNumber(float64(typed)))
	case int, int8, int16, int32, int64:
		builder.WriteString(strconv.FormatInt(anyToInt64(typed), 10))
	case uint, uint8, uint16, uint32, uint64:
		builder.WriteString(strconv.FormatUint(uint64(anyToInt64(typed)), 10))
	case []any:
		builder.WriteByte('[')
		for i, item := range typed {
			if i > 0 {
				builder.WriteString(", ")
			}
			writePythonJSON(builder, item)
		}
		builder.WriteByte(']')
	case map[string]any:
		keys := make([]string, 0, len(typed))
		for key := range typed {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		builder.WriteByte('{')
		for i, key := range keys {
			if i > 0 {
				builder.WriteString(", ")
			}
			builder.WriteString(pythonJSONString(key))
			builder.WriteString(": ")
			writePythonJSON(builder, typed[key])
		}
		builder.WriteByte('}')
	default:
		if marshaled, err := json.Marshal(typed); err == nil {
			builder.Write(marshaled)
			return
		}
		builder.WriteString("null")
	}
}

func pythonJSONString(value string) string {
	var builder strings.Builder
	builder.WriteByte('"')
	for _, r := range value {
		switch r {
		case '\\':
			builder.WriteString(`\\`)
		case '"':
			builder.WriteString(`\"`)
		case '\b':
			builder.WriteString(`\b`)
		case '\f':
			builder.WriteString(`\f`)
		case '\n':
			builder.WriteString(`\n`)
		case '\r':
			builder.WriteString(`\r`)
		case '\t':
			builder.WriteString(`\t`)
		default:
			if r < 0x20 || r > 0x7e {
				if r <= 0xffff {
					builder.WriteString(`\u`)
					builder.WriteString(fmt.Sprintf("%04x", r))
				} else {
					high, low := utf16Surrogate(r)
					builder.WriteString(`\u`)
					builder.WriteString(fmt.Sprintf("%04x", high))
					builder.WriteString(`\u`)
					builder.WriteString(fmt.Sprintf("%04x", low))
				}
			} else {
				builder.WriteRune(r)
			}
		}
	}
	builder.WriteByte('"')
	return builder.String()
}

func utf16Surrogate(r rune) (uint16, uint16) {
	v := uint32(r - 0x10000)
	return uint16(0xd800 + (v >> 10)), uint16(0xdc00 + (v & 0x3ff))
}

func formatJSONNumber(value float64) string {
	if math.Trunc(value) == value {
		return strconv.FormatInt(int64(value), 10)
	}
	return strconv.FormatFloat(value, 'f', -1, 64)
}

func rewriteRootContent(targetRoot, newAddress, sourceAddress string) error {
	contentPath := filepath.Join(targetRoot, "content.json")
	raw, err := os.ReadFile(contentPath)
	if err != nil {
		return err
	}
	var content map[string]any
	if err := json.Unmarshal(raw, &content); err != nil {
		return err
	}
	delete(content, "signs")
	delete(content, "sign")
	delete(content, "signers_sign")
	delete(content, "domain")
	content["address"] = newAddress
	content["cloned_from"] = sourceAddress
	if title, ok := content["title"].(string); ok && !strings.HasPrefix(title, "my") {
		content["title"] = "my" + title
	}
	if content["files"] == nil {
		content["files"] = map[string]any{}
	}
	serialized := pythonJSONDumps(content)
	return os.WriteFile(contentPath, []byte(serialized), 0o644)
}

func cloneSiteTree(sourceRoot, targetRoot string) error {
	if err := os.MkdirAll(targetRoot, 0o755); err != nil {
		return err
	}
	defaultFiles := make(map[string]string)
	regularFiles := make([]string, 0)
	err := filepath.WalkDir(sourceRoot, func(pathValue string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		relative, err := filepath.Rel(sourceRoot, pathValue)
		if err != nil {
			return err
		}
		if relative == "." {
			return nil
		}
		relative = filepath.ToSlash(relative)
		if strings.HasPrefix(relative, "data/users/") {
			return nil
		}
		if entry.IsDir() {
			return nil
		}
		if shouldSkipClonedContent(relative) {
			return nil
		}
		if strings.Contains(relative, "-default") {
			defaultFiles[strings.ReplaceAll(relative, "-default", "")] = pathValue
			return nil
		}
		regularFiles = append(regularFiles, relative)
		return nil
	})
	if err != nil {
		return err
	}

	sort.Strings(regularFiles)
	for _, relative := range regularFiles {
		if err := copyClonedFile(filepath.Join(sourceRoot, filepath.FromSlash(relative)), filepath.Join(targetRoot, filepath.FromSlash(relative))); err != nil {
			return err
		}
	}

	defaultKeys := make([]string, 0, len(defaultFiles))
	for relative := range defaultFiles {
		defaultKeys = append(defaultKeys, relative)
	}
	sort.Strings(defaultKeys)
	for _, relative := range defaultKeys {
		targetPath := filepath.Join(targetRoot, filepath.FromSlash(relative))
		if _, err := os.Stat(targetPath); err == nil {
			continue
		}
		if err := copyClonedFile(defaultFiles[relative], targetPath); err != nil {
			return err
		}
	}
	return nil
}

func shouldSkipClonedContent(relative string) bool {
	switch {
	case relative == "data/data.json":
		return true
	case relative == "data/zeroblog.db":
		return true
	case strings.HasPrefix(relative, "data/img/"):
		return true
	default:
		return false
	}
}

func copyClonedFile(sourcePath, targetPath string) error {
	raw, err := os.ReadFile(sourcePath)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(targetPath), 0o755); err != nil {
		return err
	}
	return os.WriteFile(targetPath, raw, 0o644)
}

func buildStarterIndex(title, description string) string {
	if description == "" {
		description = "A site published by go-zeronet"
	}
	nonce := make([]byte, 8)
	_, _ = rand.Read(nonce)
	return fmt.Sprintf(`<!doctype html>
<html lang="en">
<head>
  <meta charset="utf-8">
  <meta name="viewport" content="width=device-width, initial-scale=1">
  <title>%s</title>
  <style>
    body { margin: 0; font-family: Georgia, serif; background: linear-gradient(180deg, #f8f4ec 0%%, #efe7d8 100%%); color: #171512; }
    main { max-width: 760px; margin: 0 auto; padding: 64px 20px; }
    h1 { font-size: 48px; margin-bottom: 12px; }
    p { font-size: 20px; line-height: 1.7; color: #5f574e; }
    code { background: rgba(0,0,0,.06); padding: 2px 6px; border-radius: 6px; }
  </style>
</head>
<body>
  <main>
    <h1>%s</h1>
    <p>%s</p>
    <p>Published by <code>go-zeronet</code>.</p>
  </main>
</body>
</html>`, title, title, description)
}
