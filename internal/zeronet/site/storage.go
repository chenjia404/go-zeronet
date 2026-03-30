package site

import (
	"crypto/sha512"
	"encoding/hex"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/chenjia404/go-zeronet/internal/zeronet/conn"
)

// Manager 管理本地站点目录和回源下载逻辑。
type Manager struct {
	dataDir  string
	peers    []string
	mu       sync.Mutex
	clients  map[string]*conn.Client
	contents map[string]map[string]*ContentJSON
}

// NewManager 创建站点管理器。
func NewManager(dataDir string, peers []string) *Manager {
	return &Manager{
		dataDir:  dataDir,
		peers:    append([]string(nil), peers...),
		clients:  make(map[string]*conn.Client),
		contents: make(map[string]map[string]*ContentJSON),
	}
}

// BootstrapPeers 返回当前配置的 bootstrap peer 列表。
func (m *Manager) BootstrapPeers() []string {
	return append([]string(nil), m.peers...)
}

// EnsureRootContent 确保根 content.json 已落盘并被索引。
func (m *Manager) EnsureRootContent(siteAddress string) (*ContentJSON, error) {
	return m.ensureContent(siteAddress, "content.json")
}

// OpenSiteFile 打开站点文件，不存在时自动回源下载。
func (m *Manager) OpenSiteFile(siteAddress, innerPath string) (string, error) {
	innerPath = strings.TrimPrefix(innerPath, "/")
	if innerPath == "" {
		innerPath = "index.html"
	}
	fullPath := m.siteFilePath(siteAddress, innerPath)
	if _, err := os.Stat(fullPath); err == nil {
		return fullPath, nil
	}

	if _, err := m.EnsureRootContent(siteAddress); err != nil {
		return "", err
	}
	if _, err := m.lookupFile(siteAddress, innerPath); err != nil {
		return "", err
	}
	if err := m.downloadFile(siteAddress, innerPath); err != nil {
		return "", err
	}
	return fullPath, nil
}

// NewFileHandler 返回用于本地 HTTP 服务的文件处理器。
func (m *Manager) NewFileHandler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		pathValue := strings.TrimPrefix(r.URL.Path, "/")
		if pathValue == "" {
			w.Header().Set("Content-Type", "text/plain; charset=utf-8")
			_, _ = w.Write([]byte("go-zeronet is running\n\nOpen /<site-address> to fetch a site from a Python ZeroNet peer.\n"))
			return
		}

		parts := strings.SplitN(pathValue, "/", 2)
		siteAddress := parts[0]
		innerPath := "index.html"
		if len(parts) == 2 && parts[1] != "" {
			innerPath = parts[1]
		}

		filePath, err := m.OpenSiteFile(siteAddress, innerPath)
		if err != nil {
			http.Error(w, err.Error(), http.StatusNotFound)
			return
		}
		http.ServeFile(w, r, filePath)
	})
}

func (m *Manager) ensureContent(siteAddress, innerPath string) (*ContentJSON, error) {
	if content := m.getCachedContent(siteAddress, innerPath); content != nil {
		return content, nil
	}

	fullPath := m.siteFilePath(siteAddress, innerPath)
	if raw, err := os.ReadFile(fullPath); err == nil {
		content, parseErr := ParseContentJSON(siteAddress, innerPath, raw)
		if parseErr != nil {
			return nil, parseErr
		}
		m.setCachedContent(siteAddress, innerPath, content)
		return content, nil
	}

	raw, err := m.fetchFromPeers(siteAddress, innerPath)
	if err != nil {
		return nil, err
	}
	content, err := ParseContentJSON(siteAddress, innerPath, raw)
	if err != nil {
		return nil, err
	}
	if err := m.writeSiteFile(siteAddress, innerPath, raw); err != nil {
		return nil, err
	}
	m.setCachedContent(siteAddress, innerPath, content)
	return content, nil
}

func (m *Manager) lookupFile(siteAddress, innerPath string) (*ContentFile, error) {
	content, err := m.EnsureRootContent(siteAddress)
	if err != nil {
		return nil, err
	}
	if file, ok := content.Files[innerPath]; ok {
		return &file, nil
	}
	if file, ok := content.FilesOptional[innerPath]; ok {
		return &file, nil
	}

	for i := strings.Count(innerPath, "/"); i >= 1; i-- {
		idx := nthSlash(innerPath, i)
		if idx <= 0 {
			continue
		}
		candidateContentPath := innerPath[:idx] + "/content.json"
		nestedContent, err := m.ensureContent(siteAddress, candidateContentPath)
		if err != nil {
			continue
		}
		relativePath := strings.TrimPrefix(innerPath[idx+1:], "/")
		if file, ok := nestedContent.Files[relativePath]; ok {
			return &file, nil
		}
		if file, ok := nestedContent.FilesOptional[relativePath]; ok {
			return &file, nil
		}
	}
	return nil, fmt.Errorf("文件未在 content.json 中声明: %s", innerPath)
}

func (m *Manager) downloadFile(siteAddress, innerPath string) error {
	raw, err := m.fetchFromPeers(siteAddress, innerPath)
	if err != nil {
		return err
	}
	fileInfo, err := m.lookupFile(siteAddress, innerPath)
	if err != nil {
		return err
	}
	if err := verifyFile(fileInfo, raw); err != nil {
		return err
	}
	return m.writeSiteFile(siteAddress, innerPath, raw)
}

func verifyFile(fileInfo *ContentFile, raw []byte) error {
	if int64(len(raw)) != fileInfo.Size {
		return fmt.Errorf("文件大小不匹配: got=%d want=%d", len(raw), fileInfo.Size)
	}
	sum := sha512.Sum512(raw)
	if hex.EncodeToString(sum[:]) != strings.ToLower(fileInfo.SHA512) {
		return fmt.Errorf("文件 sha512 校验失败")
	}
	return nil
}

func (m *Manager) fetchFromPeers(siteAddress, innerPath string) ([]byte, error) {
	var lastErr error
	for _, peer := range m.peers {
		client, err := m.clientForPeer(peer)
		if err != nil {
			lastErr = err
			continue
		}
		raw, err := client.GetFile(siteAddress, innerPath)
		if err != nil {
			lastErr = err
			continue
		}
		return raw, nil
	}
	if lastErr == nil {
		lastErr = fmt.Errorf("没有可用 peer")
	}
	return nil, fmt.Errorf("从 peers 获取 %s 失败: %w", innerPath, lastErr)
}

func (m *Manager) clientForPeer(peer string) (*conn.Client, error) {
	m.mu.Lock()
	if client, ok := m.clients[peer]; ok {
		m.mu.Unlock()
		return client, nil
	}
	m.mu.Unlock()

	client, err := conn.Dial(peer)
	if err != nil {
		return nil, err
	}

	m.mu.Lock()
	defer m.mu.Unlock()
	if existing, ok := m.clients[peer]; ok {
		_ = client.Close()
		return existing, nil
	}
	m.clients[peer] = client
	return client, nil
}

func (m *Manager) siteFilePath(siteAddress, innerPath string) string {
	return filepath.Join(m.dataDir, siteAddress, filepath.FromSlash(innerPath))
}

func (m *Manager) writeSiteFile(siteAddress, innerPath string, raw []byte) error {
	fullPath := m.siteFilePath(siteAddress, innerPath)
	if err := os.MkdirAll(filepath.Dir(fullPath), 0o755); err != nil {
		return fmt.Errorf("创建站点目录失败: %w", err)
	}
	if err := os.WriteFile(fullPath, raw, 0o644); err != nil {
		return fmt.Errorf("写入站点文件失败: %w", err)
	}
	return nil
}

func (m *Manager) getCachedContent(siteAddress, innerPath string) *ContentJSON {
	m.mu.Lock()
	defer m.mu.Unlock()
	siteContents, ok := m.contents[siteAddress]
	if !ok {
		return nil
	}
	return siteContents[innerPath]
}

func (m *Manager) setCachedContent(siteAddress, innerPath string, content *ContentJSON) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, ok := m.contents[siteAddress]; !ok {
		m.contents[siteAddress] = make(map[string]*ContentJSON)
	}
	m.contents[siteAddress][innerPath] = content
}

func nthSlash(s string, n int) int {
	count := 0
	for i, r := range s {
		if r == '/' {
			count++
			if count == n {
				return i
			}
		}
	}
	return -1
}
