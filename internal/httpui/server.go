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
	mu           sync.Mutex
	wsConn       *websocket.Conn
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
		s.handleIndex(w, r)
	case r.URL.Path == "/ZeroNet-Internal/site/new":
		s.handleSiteNew(w, r)
	case r.URL.Path == "/ZeroNet-Internal/site/clone":
		s.handleSiteClone(w, r)
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

func (s *Server) handleIndex(w http.ResponseWriter, r *http.Request) {
	setCommonHeaders(w)
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_, _ = w.Write([]byte(renderIndex(indexData{
		OwnedSites: s.manager.OwnedSites(),
		Created:    r.URL.Query().Get("created"),
		Error:      r.URL.Query().Get("error"),
	})))
}

func (s *Server) handleSiteNew(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Redirect(w, r, "/", http.StatusSeeOther)
		return
	}
	if err := r.ParseForm(); err != nil {
		http.Redirect(w, r, "/?error="+url.QueryEscape(err.Error()), http.StatusSeeOther)
		return
	}
	created, err := s.manager.CreateSite(strings.TrimSpace(r.FormValue("title")), strings.TrimSpace(r.FormValue("description")))
	if err != nil {
		http.Redirect(w, r, "/?error="+url.QueryEscape(err.Error()), http.StatusSeeOther)
		return
	}
	http.Redirect(w, r, "/?created="+url.QueryEscape(created.Address), http.StatusSeeOther)
}

func (s *Server) handleSiteClone(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Redirect(w, r, "/", http.StatusSeeOther)
		return
	}
	if err := r.ParseForm(); err != nil {
		http.Redirect(w, r, "/?error="+url.QueryEscape(err.Error()), http.StatusSeeOther)
		return
	}
	cloned, err := s.manager.CloneSite(strings.TrimSpace(r.FormValue("source")))
	if err != nil {
		http.Redirect(w, r, "/?error="+url.QueryEscape(err.Error()), http.StatusSeeOther)
		return
	}
	http.Redirect(w, r, "/?created="+url.QueryEscape(cloned.Address), http.StatusSeeOther)
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
	iframeURL := "/media/" + siteAddress + "/" + strings.TrimPrefix(innerPath, "/")
	if innerPath == "index.html" {
		iframeURL = "/media/" + siteAddress + "/"
	}
	rawQuery := strings.TrimPrefix(r.URL.RawQuery, "?")
	switch {
	case rawQuery == "":
		iframeURL += "?wrapper_nonce=" + url.QueryEscape(sess.WrapperNonce)
	case strings.Contains(rawQuery, "wrapper_nonce="):
		iframeURL += "?" + rawQuery
	default:
		iframeURL += "?" + rawQuery + "&wrapper_nonce=" + url.QueryEscape(sess.WrapperNonce)
	}

	info := s.manager.SiteInfo(siteAddress, "")
	title := siteAddress
	if content, ok := info["content"].(map[string]any); ok {
		if contentTitle, ok := content["title"].(string); ok && contentTitle != "" {
			title = contentTitle
		}
	}

	setCommonHeaders(w)
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
	setCommonHeaders(w)
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

func setCommonHeaders(w http.ResponseWriter) {
	// Chrome 2026 年开始逐步默认禁用 unload，这里显式为老 ZeroNet 站点保留兼容。
	w.Header().Set("Permissions-Policy", "unload=*")
}
