package config

import (
	"context"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// Config 保存 go-zeronet 当前阶段需要的最小运行参数。
type Config struct {
	UIAddr       string
	DataDir      string
	BootstrapRaw string
	Bootstrap    []string
	TrackersRaw  string
	Trackers     []string
	DisableUDP   bool
	SharedLimit  int
}

var defaultTrackers = []string{
	"zero://zero.booth.moe#f36ca555bee6ba216b14d10f38c16f7769ff064e0e37d887603548cc2e64191d:443",
	"udp://tracker.coppersurfer.tk:6969",
	"udp://104.238.198.186:8000",
	"udp://retracker.akado-ural.ru:80",
	"http://h4.trakx.nibba.trade:80/announce",
	"http://open.acgnxtracker.com:80/announce",
	"http://tracker.bt4g.com:2095/announce",
	"zero://2602:ffc5::c5b2:5360:26312",
}

const trackerListURL = "https://raw.githubusercontent.com/XIU2/TrackersListCollection/refs/heads/master/all.txt"

// Parse 解析命令行并生成运行配置。
func Parse() (*Config, error) {
	cfg := &Config{}
	flag.StringVar(&cfg.UIAddr, "ui-addr", "127.0.0.1:43110", "本地 HTTP 服务监听地址")
	flag.StringVar(&cfg.DataDir, "data-dir", "./data", "站点数据目录")
	flag.StringVar(&cfg.BootstrapRaw, "peer", "127.0.0.1:15441", "bootstrap peer，多个用逗号分隔")
	flag.StringVar(&cfg.TrackersRaw, "trackers", strings.Join(defaultTrackers, ","), "tracker 列表，多个用逗号分隔；留空表示禁用 tracker")
	flag.BoolVar(&cfg.DisableUDP, "disable-udp", false, "禁用 UDP tracker")
	flag.IntVar(&cfg.SharedLimit, "working-shared-trackers-limit", 5, "共享 tracker 上限")
	flag.Parse()

	for _, peer := range strings.Split(cfg.BootstrapRaw, ",") {
		peer = strings.TrimSpace(peer)
		if peer == "" {
			continue
		}
		cfg.Bootstrap = append(cfg.Bootstrap, peer)
	}
	if len(cfg.Bootstrap) == 0 {
		cfg.Bootstrap = nil
	}

	for _, tracker := range strings.Split(cfg.TrackersRaw, ",") {
		tracker = strings.TrimSpace(tracker)
		if tracker == "" {
			continue
		}
		cfg.Trackers = append(cfg.Trackers, tracker)
	}
	cfg.Trackers = mergeTrackers(defaultTrackers, fetchRemoteTrackers(), cfg.Trackers)
	if len(cfg.Bootstrap) == 0 && len(cfg.Trackers) == 0 {
		return nil, fmt.Errorf("至少需要一个 --peer 或 --trackers")
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

func fetchRemoteTrackers() []string {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, trackerListURL, nil)
	if err != nil {
		return nil
	}

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil
	}
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil
	}

	var trackers []string
	for _, line := range strings.Split(string(body), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || !strings.Contains(line, "://") {
			continue
		}
		trackers = append(trackers, line)
	}
	return trackers
}

func mergeTrackers(groups ...[]string) []string {
	seen := make(map[string]struct{})
	var trackers []string
	for _, group := range groups {
		for _, tracker := range group {
			tracker = strings.TrimSpace(tracker)
			if tracker == "" {
				continue
			}
			if _, ok := seen[tracker]; ok {
				continue
			}
			seen[tracker] = struct{}{}
			trackers = append(trackers, tracker)
		}
	}
	return trackers
}
