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

	reachablePeer := false
	var reachablePeerAddr string
	var reachableClient *conn.Client
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
			reachablePeer = ok
			if ok {
				reachablePeerAddr = peer
				reachableClient = client
			}
		}
		if reachablePeer {
			break
		}
		_ = client.Close()
	}
	if !reachablePeer {
		return fmt.Errorf("没有可用的 bootstrap peer，请先确认目标 ZeroNet 正在监听 fileserver 端口")
	}

	manager := site.NewManager(cfg.DataDir, cfg.Bootstrap)
	if reachableClient != nil {
		manager.SetClient(reachablePeerAddr, reachableClient)
	}
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
