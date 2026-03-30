package app

import (
	"fmt"
	"log"

	"github.com/chenjia404/go-zeronet/internal/config"
	"github.com/chenjia404/go-zeronet/internal/httpui"
	"github.com/chenjia404/go-zeronet/internal/zeronet/conn"
	"github.com/chenjia404/go-zeronet/internal/zeronet/site"
)

// Run 启动应用并执行启动时自检。
func Run() error {
	cfg, err := config.Parse()
	if err != nil {
		return err
	}

	for _, peer := range cfg.Bootstrap {
		client, err := conn.Dial(peer)
		if err != nil {
			log.Printf("bootstrap peer 握手失败: %s: %v", peer, err)
			continue
		}
		ok, err := client.Ping()
		if err != nil {
			log.Printf("bootstrap peer ping 失败: %s: %v", peer, err)
		} else {
			log.Printf("bootstrap peer 可用: %s pong=%v", peer, ok)
		}
		_ = client.Close()
		break
	}

	manager := site.NewManager(cfg.DataDir, cfg.Bootstrap)
	server := httpui.New(cfg.UIAddr, manager.NewFileHandler())

	log.Printf("data dir: %s", cfg.DataDir)
	log.Printf("bootstrap peers: %v", manager.BootstrapPeers())
	log.Printf("ui: http://%s/", cfg.UIAddr)
	log.Printf("open a site: http://%s/<site-address>", cfg.UIAddr)

	if err := server.Run(); err != nil {
		return fmt.Errorf("应用启动失败: %w", err)
	}
	return nil
}
