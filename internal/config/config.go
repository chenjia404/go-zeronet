package config

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// Config 保存 go-zeronet 当前阶段需要的最小运行参数。
type Config struct {
	UIAddr       string
	DataDir      string
	BootstrapRaw string
	Bootstrap    []string
}

// Parse 解析命令行并生成运行配置。
func Parse() (*Config, error) {
	cfg := &Config{}
	flag.StringVar(&cfg.UIAddr, "ui-addr", "127.0.0.1:43110", "本地 HTTP 服务监听地址")
	flag.StringVar(&cfg.DataDir, "data-dir", "./data", "站点数据目录")
	flag.StringVar(&cfg.BootstrapRaw, "peer", "127.0.0.1:15441", "bootstrap peer，多个用逗号分隔")
	flag.Parse()

	for _, peer := range strings.Split(cfg.BootstrapRaw, ",") {
		peer = strings.TrimSpace(peer)
		if peer == "" {
			continue
		}
		cfg.Bootstrap = append(cfg.Bootstrap, peer)
	}
	if len(cfg.Bootstrap) == 0 {
		return nil, fmt.Errorf("至少需要一个 --peer")
	}

	absDataDir, err := filepath.Abs(cfg.DataDir)
	if err != nil {
		return nil, fmt.Errorf("解析 data 目录失败: %w", err)
	}
	cfg.DataDir = absDataDir

	if err := os.MkdirAll(cfg.DataDir, 0o755); err != nil {
		return nil, fmt.Errorf("创建 data 目录失败: %w", err)
	}

	return cfg, nil
}
