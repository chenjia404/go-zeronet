package site

import (
	"crypto/sha512"
	"encoding/hex"
	"fmt"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/chenjia404/go-zeronet/internal/tracker"
	"github.com/chenjia404/go-zeronet/internal/zeronet/conn"
)

// Manager 管理本地站点目录和回源下载逻辑。
type Manager struct {
	dataDir          string
	peerOrder        []string
	peerSet          map[string]struct{}
	mu               sync.Mutex
	clients          map[string]*conn.Client
	contents         map[string]map[string]*ContentJSON
	lastCheck        map[string]time.Time
	peerHashfields   map[string]conn.Hashfield
	hashfieldFetched map[string]time.Time
	optionalHashes   map[uint16]struct{}
	announcer        *tracker.Announcer
}

// FileMetadata 表示某个文件条目以及它是否来自 optional 文件集合。
type FileMetadata struct {
	ContentFile
	Optional bool
}

// NewManager 创建站点管理器。
func NewManager(dataDir string, peers []string, announcer *tracker.Announcer) *Manager {
	manager := &Manager{
		dataDir:          dataDir,
		peerSet:          make(map[string]struct{}),
		clients:          make(map[string]*conn.Client),
		contents:         make(map[string]map[string]*ContentJSON),
		lastCheck:        make(map[string]time.Time),
		peerHashfields:   make(map[string]conn.Hashfield),
		hashfieldFetched: make(map[string]time.Time),
		optionalHashes:   make(map[uint16]struct{}),
		announcer:        announcer,
	}
	for _, peer := range peers {
		manager.addPeer(peer)
	}
	return manager
}

// BootstrapPeers 返回当前配置的 bootstrap peer 列表。
func (m *Manager) BootstrapPeers() []string {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([]string(nil), m.peerOrder...)
}

// SetClient 将已经验证可用的连接放入缓存，避免后续重复拨号。
func (m *Manager) SetClient(peer string, client *conn.Client) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, ok := m.peerSet[peer]; !ok {
		m.peerSet[peer] = struct{}{}
		m.peerOrder = append(m.peerOrder, peer)
	}
	m.clients[peer] = client
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

	_ = m.refreshSite(siteAddress)

	fullPath := m.siteFilePath(siteAddress, innerPath)
	if _, err := os.Stat(fullPath); err == nil {
		valid, verifyErr := m.verifyLocalFile(siteAddress, innerPath)
		if verifyErr == nil && valid {
			return fullPath, nil
		}
	}

	if _, err := m.EnsureRootContent(siteAddress); err != nil {
		m.announceSite(siteAddress)
		if _, retryErr := m.EnsureRootContent(siteAddress); retryErr != nil {
			return "", retryErr
		}
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
		m.preloadRelatedContents(siteAddress, innerPath, content)
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
	m.preloadRelatedContents(siteAddress, innerPath, content)
	return content, nil
}

func (m *Manager) refreshSite(siteAddress string) error {
	if !m.shouldRefresh(siteAddress) {
		return nil
	}

	// 先确保根 content.json 在本地存在，后续比较修改时间和解析 include 都依赖它。
	_, _ = m.ensureContent(siteAddress, "content.json")
	if len(m.peers()) == 0 {
		m.announceSite(siteAddress)
	}

	var latestModifiedFiles conn.ModifiedFilesResponse
	for _, peer := range m.peers() {
		client, err := m.clientForPeer(peer)
		if err != nil {
			continue
		}
		modifiedFiles, err := client.ListModified(siteAddress, 0)
		if err != nil {
			m.dropClient(peer)
			continue
		}
		if len(modifiedFiles) == 0 {
			continue
		}
		latestModifiedFiles = modifiedFiles
		if modifiedFiles["content.json"] > m.contentModified(siteAddress, "content.json") {
			break
		}
	}

	if len(latestModifiedFiles) == 0 {
		return nil
	}
	return m.refreshModifiedContents(siteAddress, latestModifiedFiles)
}

func (m *Manager) lookupFile(siteAddress, innerPath string) (*FileMetadata, error) {
	content, err := m.EnsureRootContent(siteAddress)
	if err != nil {
		return nil, err
	}
	if file, ok := content.Files[innerPath]; ok {
		return &FileMetadata{ContentFile: file}, nil
	}
	if file, ok := content.FilesOptional[innerPath]; ok {
		return &FileMetadata{ContentFile: file, Optional: true}, nil
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
			return &FileMetadata{ContentFile: file}, nil
		}
		if file, ok := nestedContent.FilesOptional[relativePath]; ok {
			return &FileMetadata{ContentFile: file, Optional: true}, nil
		}
	}
	return nil, fmt.Errorf("文件未在 content.json 中声明: %s", innerPath)
}

func (m *Manager) downloadFile(siteAddress, innerPath string) error {
	fileInfo, err := m.lookupFile(siteAddress, innerPath)
	if err != nil {
		return err
	}

	var lastErr error
	for _, peer := range m.downloadPeers(siteAddress, fileInfo) {
		raw, fetchErr := m.fetchFromPeer(peer, siteAddress, innerPath)
		if fetchErr != nil {
			lastErr = fetchErr
			continue
		}
		if verifyErr := verifyFile(innerPath, &fileInfo.ContentFile, raw); verifyErr != nil {
			lastErr = verifyErr
			m.dropClient(peer)
			continue
		}
		if err := m.writeSiteFile(siteAddress, innerPath, raw); err != nil {
			return err
		}
		m.trackDownloadedOptional(fileInfo)
		return nil
	}

	if lastErr == nil {
		lastErr = fmt.Errorf("没有可用 peer")
	}
	return lastErr
}

func verifyFile(innerPath string, fileInfo *ContentFile, raw []byte) error {
	if int64(len(raw)) != fileInfo.Size {
		return fmt.Errorf("%s 文件大小不匹配: got=%d want=%d", innerPath, len(raw), fileInfo.Size)
	}
	sum := sha512.Sum512(raw)
	// Python ZeroNet 的 sha512sum 只保留前 256 bit。
	gotHash := hex.EncodeToString(sum[:32])
	wantHash := strings.ToLower(fileInfo.SHA512)
	if gotHash != wantHash {
		return fmt.Errorf("%s 文件 sha512 校验失败: got=%s want=%s", innerPath, gotHash, wantHash)
	}
	return nil
}

func (m *Manager) verifyLocalFile(siteAddress, innerPath string) (bool, error) {
	fileInfo, err := m.lookupFile(siteAddress, innerPath)
	if err != nil {
		return false, err
	}
	raw, err := os.ReadFile(m.siteFilePath(siteAddress, innerPath))
	if err != nil {
		return false, err
	}
	if err := verifyFile(innerPath, &fileInfo.ContentFile, raw); err != nil {
		return false, err
	}
	return true, nil
}

func (m *Manager) refreshModifiedContents(siteAddress string, modifiedFiles conn.ModifiedFilesResponse) error {
	// 根 content.json 必须最先刷新，否则子 content.json 的 include 树可能基于旧元数据。
	contentPaths := make([]string, 0, len(modifiedFiles))
	for innerPath := range modifiedFiles {
		if strings.HasSuffix(innerPath, "content.json") {
			contentPaths = append(contentPaths, innerPath)
		}
	}
	sort.Slice(contentPaths, func(i, j int) bool {
		if contentPaths[i] == "content.json" {
			return true
		}
		if contentPaths[j] == "content.json" {
			return false
		}
		return contentPaths[i] < contentPaths[j]
	})

	for _, innerPath := range contentPaths {
		if modifiedFiles[innerPath] <= m.contentModified(siteAddress, innerPath) {
			continue
		}
		if err := m.refreshContent(siteAddress, innerPath); err != nil {
			return err
		}
	}
	return nil
}

func (m *Manager) refreshContent(siteAddress, innerPath string) error {
	oldContent := m.getCachedContent(siteAddress, innerPath)
	if oldContent == nil {
		oldContent = m.readCachedContentFromDisk(siteAddress, innerPath)
	}

	raw, err := m.fetchFromPeers(siteAddress, innerPath)
	if err != nil {
		return err
	}
	content, err := ParseContentJSON(siteAddress, innerPath, raw)
	if err != nil {
		return err
	}
	if err := m.writeSiteFile(siteAddress, innerPath, raw); err != nil {
		return err
	}
	m.renameContentFiles(siteAddress, innerPath, oldContent, content)
	m.setCachedContent(siteAddress, innerPath, content)
	m.preloadRelatedContents(siteAddress, innerPath, content)
	if oldContent != nil {
		if err := m.removeStaleContentFiles(siteAddress, innerPath, oldContent, content); err != nil {
			return err
		}
		if err := m.removeArchivedUserContents(siteAddress, innerPath, oldContent, content); err != nil {
			return err
		}
	}
	return nil
}

func (m *Manager) contentModified(siteAddress, innerPath string) int64 {
	content := m.getCachedContent(siteAddress, innerPath)
	if content != nil {
		return content.Modified
	}

	content = m.readCachedContentFromDisk(siteAddress, innerPath)
	if content == nil {
		return 0
	}
	return content.Modified
}

func (m *Manager) readCachedContentFromDisk(siteAddress, innerPath string) *ContentJSON {
	raw, err := os.ReadFile(m.siteFilePath(siteAddress, innerPath))
	if err != nil {
		return nil
	}
	content, err := ParseContentJSON(siteAddress, innerPath, raw)
	if err != nil {
		return nil
	}
	m.setCachedContent(siteAddress, innerPath, content)
	return content
}

func (m *Manager) removeStaleContentFiles(siteAddress, contentPath string, oldContent, newContent *ContentJSON) error {
	// content.json 更新后，需要移除本地已不再声明的文件，避免继续读取旧资源。
	removedFiles := make([]string, 0)
	seen := make(map[string]struct{})
	for relativePath := range oldContent.Files {
		if _, ok := newContent.Files[relativePath]; ok {
			continue
		}
		if _, ok := newContent.FilesOptional[relativePath]; ok {
			continue
		}
		removedFiles = append(removedFiles, relativePath)
		seen[relativePath] = struct{}{}
	}
	for relativePath := range oldContent.FilesOptional {
		if _, ok := seen[relativePath]; ok {
			continue
		}
		if _, ok := newContent.Files[relativePath]; ok {
			continue
		}
		if _, ok := newContent.FilesOptional[relativePath]; ok {
			continue
		}
		removedFiles = append(removedFiles, relativePath)
	}

	for _, relativePath := range removedFiles {
		fullPath := m.siteFilePath(siteAddress, resolveContentFilePath(contentPath, relativePath))
		if err := os.Remove(fullPath); err != nil && !os.IsNotExist(err) {
			return fmt.Errorf("删除失效文件失败 %s: %w", fullPath, err)
		}
		m.trackRemovedOptional(oldContent, newContent, relativePath)
	}
	return nil
}

func (m *Manager) renameContentFiles(siteAddress, contentPath string, oldContent, newContent *ContentJSON) {
	if oldContent == nil || newContent == nil {
		return
	}

	renamed := detectRenamedFiles(oldContent, newContent)
	for oldRelativePath, newRelativePath := range renamed {
		oldPath := m.siteFilePath(siteAddress, resolveContentFilePath(contentPath, oldRelativePath))
		newPath := m.siteFilePath(siteAddress, resolveContentFilePath(contentPath, newRelativePath))
		if _, err := os.Stat(oldPath); err != nil {
			continue
		}
		if err := os.MkdirAll(filepath.Dir(newPath), 0o755); err != nil {
			continue
		}
		if err := os.Rename(oldPath, newPath); err != nil {
			continue
		}
	}
}

func (m *Manager) removeArchivedUserContents(siteAddress, contentPath string, oldContent, newContent *ContentJSON) error {
	if len(newContent.UserContents) == 0 {
		return nil
	}

	contentBaseDir := pathDir(contentPath)
	oldArchived := archivedDirectories(oldContent)
	newArchived := archivedDirectories(newContent)

	// 指定目录被新归档后，清理其本地用户内容目录。
	for relativeDir, archivedAt := range newArchived {
		if archivedAt <= oldArchived[relativeDir] {
			continue
		}
		childContentPath := joinInnerPath(contentBaseDir, relativeDir+"/content.json")
		if m.contentModified(siteAddress, childContentPath) >= archivedAt {
			continue
		}
		if err := m.removeContentTree(siteAddress, childContentPath); err != nil {
			return err
		}
	}

	oldArchivedBefore := archivedBefore(oldContent)
	newArchivedBefore := archivedBefore(newContent)
	if newArchivedBefore <= oldArchivedBefore {
		return nil
	}

	childContents, err := m.listChildContentPaths(siteAddress, contentPath)
	if err != nil {
		return err
	}
	for _, childContentPath := range childContents {
		if childContentPath == contentPath {
			continue
		}
		if m.contentModified(siteAddress, childContentPath) > newArchivedBefore {
			continue
		}
		if err := m.removeContentTree(siteAddress, childContentPath); err != nil {
			return err
		}
	}
	return nil
}

func (m *Manager) preloadRelatedContents(siteAddress, contentPath string, content *ContentJSON) {
	// 显式 includes 可以直接推导出子 content.json 路径，优先预加载到缓存。
	for relativePath := range content.Includes {
		includePath := joinInnerPath(pathDir(contentPath), relativePath)
		if !strings.HasSuffix(includePath, "content.json") {
			continue
		}
		if _, err := m.ensureContent(siteAddress, includePath); err != nil {
			continue
		}
	}

	// user_contents 是盲加载规则：先扫描本地已存在的用户子目录并建立缓存。
	if len(content.UserContents) == 0 {
		return
	}

	contentDir := filepath.Dir(m.siteFilePath(siteAddress, contentPath))
	entries, err := os.ReadDir(contentDir)
	if err != nil {
		return
	}
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		childContentPath := joinInnerPath(pathDir(contentPath), entry.Name()+"/content.json")
		childDiskPath := m.siteFilePath(siteAddress, childContentPath)
		if _, err := os.Stat(childDiskPath); err != nil {
			continue
		}
		if _, err := m.ensureContent(siteAddress, childContentPath); err != nil {
			continue
		}
	}
}

func (m *Manager) fetchFromPeers(siteAddress, innerPath string) ([]byte, error) {
	var lastErr error
	for _, peer := range m.peers() {
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
	m.announceSite(siteAddress)
	for _, peer := range m.peers() {
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
	return nil, fmt.Errorf("从 peers 获取 %s 失败: %w", innerPath, lastErr)
}

func (m *Manager) fetchFromPeer(peer, siteAddress, innerPath string) ([]byte, error) {
	client, err := m.clientForPeer(peer)
	if err != nil {
		return nil, err
	}

	// 下载成功后立即做一次 PEX，滚动扩展 peer 池。
	m.expandPeers(siteAddress, peer, client)

	raw, err := client.GetFile(siteAddress, innerPath)
	if err != nil {
		m.dropClient(peer)
		return nil, err
	}
	return raw, nil
}

func (m *Manager) downloadPeers(siteAddress string, fileInfo *FileMetadata) []string {
	if fileInfo == nil || !fileInfo.Optional {
		return m.peers()
	}

	hashID, ok := optionalHashID(fileInfo.SHA512)
	if !ok {
		return m.peers()
	}

	m.expandOptionalPeers(siteAddress, hashID)
	peers := m.peers()

	preferred := make([]string, 0, len(peers))
	fallback := make([]string, 0, len(peers))
	for _, peer := range peers {
		hasFile, known := m.peerHasOptionalFile(siteAddress, peer, hashID)
		if known && hasFile {
			preferred = append(preferred, peer)
			continue
		}
		fallback = append(fallback, peer)
	}
	return append(preferred, fallback...)
}

func (m *Manager) expandOptionalPeers(siteAddress string, hashID uint16) {
	for _, peer := range m.peers() {
		client, err := m.clientForPeer(peer)
		if err != nil {
			continue
		}
		hashPeers, err := client.FindHashIDs(siteAddress, []uint16{hashID})
		if err != nil {
			continue
		}
		for _, candidate := range hashPeers[hashID] {
			if candidate.Port == 0 || strings.HasSuffix(candidate.IP, ".onion") {
				continue
			}
			m.addPeer(fmt.Sprintf("%s:%d", candidate.IP, candidate.Port))
		}
	}
}

func (m *Manager) peerHasOptionalFile(siteAddress, peer string, hashID uint16) (bool, bool) {
	hashfield, ok := m.getPeerHashfield(peer)
	if ok {
		_, hasFile := hashfield[hashID]
		return hasFile, true
	}

	client, err := m.clientForPeer(peer)
	if err != nil {
		return false, false
	}
	hashfield, err = client.GetHashfield(siteAddress)
	if err != nil {
		m.dropClient(peer)
		return false, false
	}
	m.setPeerHashfield(peer, hashfield)
	_, hasFile := hashfield[hashID]
	return hasFile, true
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
	m.discoverSharedTrackers()
	return client, nil
}

func (m *Manager) dropClient(peer string) {
	m.mu.Lock()
	client := m.clients[peer]
	delete(m.clients, peer)
	delete(m.peerHashfields, peer)
	delete(m.hashfieldFetched, peer)
	m.mu.Unlock()
	if client != nil {
		_ = client.Close()
	}
}

func (m *Manager) peers() []string {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([]string(nil), m.peerOrder...)
}

func (m *Manager) addPeer(peer string) {
	if peer == "" {
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, ok := m.peerSet[peer]; ok {
		return
	}
	m.peerSet[peer] = struct{}{}
	m.peerOrder = append(m.peerOrder, peer)
}

func (m *Manager) announceSite(siteAddress string) {
	if m.announcer == nil {
		return
	}
	peers, err := m.announcer.Announce(siteAddress)
	if err != nil {
		return
	}
	for _, peer := range peers {
		m.addPeer(peer)
	}
}

func (m *Manager) discoverSharedTrackers() {
	if m.announcer == nil {
		return
	}
	m.announcer.DiscoverTrackersFromPeers(m.peers())
}

func (m *Manager) getPeerHashfield(peer string) (conn.Hashfield, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	hashfield := m.peerHashfields[peer]
	fetchedAt := m.hashfieldFetched[peer]
	if hashfield == nil || time.Since(fetchedAt) > 5*time.Minute {
		return nil, false
	}
	return hashfield, true
}

func (m *Manager) setPeerHashfield(peer string, hashfield conn.Hashfield) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.peerHashfields[peer] = hashfield
	m.hashfieldFetched[peer] = time.Now()
}

func (m *Manager) trackDownloadedOptional(fileInfo *FileMetadata) {
	if fileInfo == nil || !fileInfo.Optional {
		return
	}
	hashID, ok := optionalHashID(fileInfo.SHA512)
	if !ok {
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.optionalHashes[hashID] = struct{}{}
}

func (m *Manager) trackRemovedOptional(oldContent, newContent *ContentJSON, relativePath string) {
	if oldContent == nil {
		return
	}
	fileInfo, ok := oldContent.FilesOptional[relativePath]
	if !ok {
		return
	}
	for _, current := range newContent.FilesOptional {
		if strings.EqualFold(current.SHA512, fileInfo.SHA512) {
			return
		}
	}
	hashID, ok := optionalHashID(fileInfo.SHA512)
	if !ok {
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.optionalHashes, hashID)
}

func (m *Manager) expandPeers(siteAddress, currentPeer string, client *conn.Client) {
	knownPeers := m.knownPeerAddresses(currentPeer)
	discoveredPeers, err := client.Pex(siteAddress, knownPeers, 10)
	if err != nil {
		return
	}
	for _, peer := range discoveredPeers {
		if strings.HasSuffix(peer.IP, ".onion") {
			continue
		}
		m.addPeer(fmt.Sprintf("%s:%d", peer.IP, peer.Port))
	}
}

func (m *Manager) knownPeerAddresses(exclude string) []conn.PeerAddress {
	m.mu.Lock()
	defer m.mu.Unlock()

	known := make([]conn.PeerAddress, 0, len(m.peerOrder))
	for _, peer := range m.peerOrder {
		if peer == exclude {
			continue
		}
		host, port, err := net.SplitHostPort(peer)
		if err != nil {
			continue
		}
		portNum, err := strconv.Atoi(port)
		if err != nil {
			continue
		}
		known = append(known, conn.PeerAddress{IP: host, Port: portNum})
	}
	return known
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

func (m *Manager) shouldRefresh(siteAddress string) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	lastCheck := m.lastCheck[siteAddress]
	if time.Since(lastCheck) < 15*time.Second {
		return false
	}
	m.lastCheck[siteAddress] = time.Now()
	return true
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

func resolveContentFilePath(contentPath, relativePath string) string {
	baseDir := pathDir(contentPath)
	if baseDir == "" {
		return relativePath
	}
	return baseDir + "/" + relativePath
}

func joinInnerPath(baseDir, relativePath string) string {
	relativePath = strings.TrimPrefix(relativePath, "/")
	if baseDir == "" {
		return relativePath
	}
	return baseDir + "/" + relativePath
}

func pathDir(innerPath string) string {
	index := strings.LastIndex(innerPath, "/")
	if index < 0 {
		return ""
	}
	return innerPath[:index]
}

func archivedDirectories(content *ContentJSON) map[string]int64 {
	if content == nil || len(content.UserContents) == 0 {
		return nil
	}

	raw, ok := content.UserContents["archived"]
	if !ok {
		return nil
	}

	back := make(map[string]int64)
	switch val := raw.(type) {
	case map[string]any:
		for key, item := range val {
			back[key] = anyToInt64(item)
		}
	case map[string]int64:
		for key, item := range val {
			back[key] = item
		}
	case map[any]any:
		for key, item := range val {
			keyStr, ok := key.(string)
			if !ok {
				continue
			}
			back[keyStr] = anyToInt64(item)
		}
	}
	return back
}

func archivedBefore(content *ContentJSON) int64 {
	if content == nil || len(content.UserContents) == 0 {
		return 0
	}
	return anyToInt64(content.UserContents["archived_before"])
}

func (m *Manager) listChildContentPaths(siteAddress, contentPath string) ([]string, error) {
	contentRoot := m.siteFilePath(siteAddress, pathDir(contentPath))
	if _, err := os.Stat(contentRoot); err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}

	var childPaths []string
	err := filepath.WalkDir(contentRoot, func(fullPath string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() {
			return nil
		}
		if entry.Name() != "content.json" {
			return nil
		}

		relativePath, err := filepath.Rel(m.siteFilePath(siteAddress, ""), fullPath)
		if err != nil {
			return err
		}
		childPaths = append(childPaths, filepath.ToSlash(relativePath))
		return nil
	})
	if err != nil {
		return nil, err
	}
	sort.Strings(childPaths)
	return childPaths, nil
}

func (m *Manager) removeContentTree(siteAddress, contentPath string) error {
	if contentPath == "content.json" {
		return nil
	}

	removedContents := m.collectContentTree(siteAddress, contentPath)
	dirPath := m.siteFilePath(siteAddress, pathDir(contentPath))
	if err := os.RemoveAll(dirPath); err != nil {
		return fmt.Errorf("删除归档目录失败 %s: %w", dirPath, err)
	}

	for _, content := range removedContents {
		for relativePath := range content.FilesOptional {
			m.trackRemovedOptional(content, &ContentJSON{}, relativePath)
		}
	}

	m.mu.Lock()
	defer m.mu.Unlock()
	siteContents := m.contents[siteAddress]
	if siteContents == nil {
		return nil
	}
	prefix := pathDir(contentPath) + "/"
	for innerPath := range siteContents {
		if innerPath == contentPath || strings.HasPrefix(innerPath, prefix) {
			delete(siteContents, innerPath)
		}
	}
	return nil
}

func (m *Manager) collectContentTree(siteAddress, contentPath string) []*ContentJSON {
	m.mu.Lock()
	defer m.mu.Unlock()

	siteContents := m.contents[siteAddress]
	if siteContents == nil {
		return nil
	}

	prefix := pathDir(contentPath) + "/"
	var removed []*ContentJSON
	for innerPath, content := range siteContents {
		if innerPath == contentPath || strings.HasPrefix(innerPath, prefix) {
			removed = append(removed, content)
		}
	}
	return removed
}

func anyToInt64(v any) int64 {
	switch val := v.(type) {
	case int:
		return int64(val)
	case int8:
		return int64(val)
	case int16:
		return int64(val)
	case int32:
		return int64(val)
	case int64:
		return val
	case uint:
		return int64(val)
	case uint8:
		return int64(val)
	case uint16:
		return int64(val)
	case uint32:
		return int64(val)
	case uint64:
		return int64(val)
	case float32:
		return int64(val)
	case float64:
		return int64(val)
	default:
		return 0
	}
}

func optionalHashID(sha512hex string) (uint16, bool) {
	if len(sha512hex) < 4 {
		return 0, false
	}
	value, err := strconv.ParseUint(sha512hex[:4], 16, 16)
	if err != nil {
		return 0, false
	}
	return uint16(value), true
}

func detectRenamedFiles(oldContent, newContent *ContentJSON) map[string]string {
	oldFiles := mergeContentFiles(oldContent)
	newFiles := mergeContentFiles(newContent)
	deletedByHash := make(map[string]string)
	for relativePath, fileInfo := range oldFiles {
		if _, ok := newFiles[relativePath]; ok {
			continue
		}
		deletedByHash[strings.ToLower(fileInfo.SHA512)] = relativePath
	}

	renamed := make(map[string]string)
	for relativePath, fileInfo := range newFiles {
		if _, ok := oldFiles[relativePath]; ok {
			continue
		}
		if oldRelativePath, ok := deletedByHash[strings.ToLower(fileInfo.SHA512)]; ok {
			renamed[oldRelativePath] = relativePath
		}
	}
	return renamed
}

func mergeContentFiles(content *ContentJSON) map[string]ContentFile {
	back := make(map[string]ContentFile)
	if content == nil {
		return back
	}
	for relativePath, fileInfo := range content.Files {
		back[relativePath] = fileInfo
	}
	for relativePath, fileInfo := range content.FilesOptional {
		back[relativePath] = fileInfo
	}
	return back
}
