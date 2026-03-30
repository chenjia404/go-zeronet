package httpui

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"net/http"
	"net/url"
	"path"
	"strings"
	"sync"
	"time"

	"github.com/chenjia404/go-zeronet/internal/zeronet/site"
	"github.com/gorilla/websocket"
)

const version = "go-zeronet/0.1.0"

// Server 是 ZeroFrame 兼容层的 HTTP/WebSocket 入口。
type Server struct {
	addr     string
	manager  *site.Manager
	upgrader websocket.Upgrader

	mu       sync.Mutex
	sessions map[string]*session
}

type session struct {
	Key          string
	SiteAddress  string
	InnerPath    string
	WrapperNonce string
	AjaxKey      string
}

// New 创建 HTTP UI 服务器。
func New(addr string, manager *site.Manager) *Server {
	return &Server{
		addr:    addr,
		manager: manager,
		upgrader: websocket.Upgrader{
			CheckOrigin: func(r *http.Request) bool { return true },
		},
		sessions: make(map[string]*session),
	}
}

// Run 启动本地 HTTP 服务。
func (s *Server) Run() error {
	httpServer := &http.Server{
		Addr:              s.addr,
		Handler:           http.HandlerFunc(s.route),
		ReadHeaderTimeout: 5 * time.Second,
	}
	if err := httpServer.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		return fmt.Errorf("HTTP 服务启动失败: %w", err)
	}
	return nil
}

func (s *Server) route(w http.ResponseWriter, r *http.Request) {
	switch {
	case r.URL.Path == "/":
		s.handleIndex(w)
	case r.URL.Path == "/ZeroNet-Internal/Websocket":
		s.handleWebsocket(w, r)
	case strings.HasPrefix(r.URL.Path, "/raw/"):
		s.handleMedia(w, r, "/raw/")
	case strings.HasPrefix(r.URL.Path, "/media/"):
		s.handleMedia(w, r, "/media/")
	default:
		s.handleSite(w, r)
	}
}

func (s *Server) handleIndex(w http.ResponseWriter) {
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	_, _ = w.Write([]byte("go-zeronet is running\n\nOpen /<site-address> to fetch a site.\n"))
}

func (s *Server) handleSite(w http.ResponseWriter, r *http.Request) {
	siteAddress, innerPath := parseSitePath(r.URL.Path)
	if siteAddress == "" {
		http.NotFound(w, r)
		return
	}

	if !shouldWrap(innerPath) || r.URL.Query().Get("wrapper") == "False" {
		s.serveSiteFile(w, r, siteAddress, innerPath)
		return
	}

	sess := s.newSession(siteAddress, innerPath)
	query := cloneQuery(r.URL.Query())
	query.Set("wrapper_nonce", sess.WrapperNonce)
	iframeURL := "/media/" + siteAddress + "/" + strings.TrimPrefix(innerPath, "/")
	if innerPath == "index.html" {
		iframeURL = "/media/" + siteAddress + "/"
	}
	if encoded := query.Encode(); encoded != "" {
		iframeURL += "?" + encoded
	}

	info := s.manager.SiteInfo(siteAddress, "")
	title := siteAddress
	if content, ok := info["content"].(map[string]any); ok {
		if contentTitle, ok := content["title"].(string); ok && contentTitle != "" {
			title = contentTitle
		}
	}

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_, _ = w.Write([]byte(renderWrapper(wrapperData{
		Title:        title,
		SiteAddress:  siteAddress,
		InnerPath:    innerPath,
		IFrameURL:    iframeURL,
		WrapperKey:   sess.Key,
		WrapperNonce: sess.WrapperNonce,
		AjaxKey:      sess.AjaxKey,
	})))
}

func (s *Server) handleMedia(w http.ResponseWriter, r *http.Request, prefix string) {
	trimmed := strings.TrimPrefix(r.URL.Path, prefix)
	parts := strings.SplitN(trimmed, "/", 2)
	if len(parts) == 0 || parts[0] == "" {
		http.NotFound(w, r)
		return
	}
	siteAddress := parts[0]
	innerPath := "index.html"
	if len(parts) == 2 && parts[1] != "" {
		innerPath = parts[1]
	}
	s.serveSiteFile(w, r, siteAddress, innerPath)
}

func (s *Server) serveSiteFile(w http.ResponseWriter, r *http.Request, siteAddress, innerPath string) {
	filePath, err := s.manager.OpenSiteFile(siteAddress, innerPath)
	if err != nil {
		http.Error(w, err.Error(), http.StatusNotFound)
		return
	}
	http.ServeFile(w, r, filePath)
}

func (s *Server) newSession(siteAddress, innerPath string) *session {
	sess := &session{
		Key:          randomToken(16),
		SiteAddress:  siteAddress,
		InnerPath:    innerPath,
		WrapperNonce: randomToken(12),
		AjaxKey:      randomToken(12),
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.sessions[sess.Key] = sess
	return sess
}

func (s *Server) session(key string) *session {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.sessions[key]
}

func parseSitePath(rawPath string) (string, string) {
	trimmed := strings.TrimPrefix(rawPath, "/")
	if trimmed == "" {
		return "", ""
	}
	parts := strings.SplitN(trimmed, "/", 2)
	siteAddress := parts[0]
	if siteAddress == "" {
		return "", ""
	}
	if len(parts) == 1 || parts[1] == "" {
		return siteAddress, "index.html"
	}
	return siteAddress, path.Clean(parts[1])
}

func shouldWrap(innerPath string) bool {
	ext := strings.ToLower(path.Ext(innerPath))
	return ext == "" || ext == ".html" || ext == ".htm"
}

func randomToken(size int) string {
	buf := make([]byte, size)
	if _, err := rand.Read(buf); err != nil {
		now := time.Now().UnixNano()
		return fmt.Sprintf("%x", now)
	}
	return hex.EncodeToString(buf)
}

func cloneQuery(values url.Values) url.Values {
	back := make(url.Values, len(values))
	for key, items := range values {
		back[key] = append([]string(nil), items...)
	}
	return back
}
