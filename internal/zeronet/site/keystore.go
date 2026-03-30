package site

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
)

// siteRecord 保存站点持久化信息。
type siteRecord struct {
	PrivateKey string `json:"privatekey"`
}

type siteStore struct {
	mu    sync.Mutex
	path  string
	Sites map[string]siteRecord `json:"sites"`
}

func loadSiteStore(dataDir string) (*siteStore, error) {
	store := &siteStore{
		path:  filepath.Join(dataDir, "sites.json"),
		Sites: make(map[string]siteRecord),
	}

	raw, err := os.ReadFile(store.path)
	if err != nil {
		if os.IsNotExist(err) {
			return store, nil
		}
		return nil, fmt.Errorf("读取 sites.json 失败: %w", err)
	}
	if len(raw) == 0 {
		return store, nil
	}
	if err := json.Unmarshal(raw, store); err != nil {
		return nil, fmt.Errorf("解析 sites.json 失败: %w", err)
	}
	if store.Sites == nil {
		store.Sites = make(map[string]siteRecord)
	}
	return store, nil
}

func (s *siteStore) save() error {
	s.mu.Lock()
	defer s.mu.Unlock()

	raw, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return fmt.Errorf("序列化 sites.json 失败: %w", err)
	}
	if err := os.WriteFile(s.path, raw, 0o600); err != nil {
		return fmt.Errorf("写入 sites.json 失败: %w", err)
	}
	return nil
}

func (s *siteStore) privateKey(siteAddress string) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.Sites[siteAddress].PrivateKey
}

func (s *siteStore) hasSite(siteAddress string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	_, ok := s.Sites[siteAddress]
	return ok
}

func (s *siteStore) setPrivateKey(siteAddress, privateKey string) error {
	s.mu.Lock()
	if s.Sites == nil {
		s.Sites = make(map[string]siteRecord)
	}
	s.Sites[siteAddress] = siteRecord{PrivateKey: privateKey}
	s.mu.Unlock()
	return s.save()
}
