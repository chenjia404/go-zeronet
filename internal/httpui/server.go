package httpui

import (
	"fmt"
	"net/http"
	"time"
)

// Server 是当前阶段的本地 HTTP UI 服务封装。
type Server struct {
	addr    string
	handler http.Handler
}

// New 创建 HTTP UI 服务器。
func New(addr string, handler http.Handler) *Server {
	return &Server{addr: addr, handler: handler}
}

// Run 启动本地 HTTP 服务。
func (s *Server) Run() error {
	httpServer := &http.Server{
		Addr:              s.addr,
		Handler:           s.handler,
		ReadHeaderTimeout: 5 * time.Second,
	}
	if err := httpServer.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		return fmt.Errorf("HTTP 服务启动失败: %w", err)
	}
	return nil
}
