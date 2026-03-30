package app

import (
	"flag"
	"fmt"
	"io"
	"os"

	"github.com/chenjia404/go-zeronet/internal/config"
	"github.com/chenjia404/go-zeronet/internal/zeronet/site"
)

// RunSiteCommand 处理站点创建和克隆命令。
func RunSiteCommand(args []string, stdout io.Writer) error {
	if len(args) == 0 {
		return fmt.Errorf("missing site subcommand: use new or clone")
	}

	switch args[0] {
	case "new":
		fs := flag.NewFlagSet("site new", flag.ContinueOnError)
		fs.SetOutput(io.Discard)
		dataDir := fs.String("data-dir", "./data", "站点数据目录")
		title := fs.String("title", "New ZeroNet Site", "站点标题")
		description := fs.String("description", "", "站点描述")
		if err := fs.Parse(args[1:]); err != nil {
			return err
		}
		cfg, err := config.PrepareDataDir(*dataDir)
		if err != nil {
			return err
		}
		manager := site.NewManager(cfg, nil, nil)
		created, err := manager.CreateSite(*title, *description)
		if err != nil {
			return err
		}
		_, _ = fmt.Fprintf(stdout, "address: %s\nprivatekey: %s\n", created.Address, created.PrivateKey)
		return nil
	case "clone":
		fs := flag.NewFlagSet("site clone", flag.ContinueOnError)
		fs.SetOutput(io.Discard)
		dataDir := fs.String("data-dir", "./data", "站点数据目录")
		source := fs.String("source", "", "要克隆的源站点地址")
		if err := fs.Parse(args[1:]); err != nil {
			return err
		}
		if *source == "" {
			return fmt.Errorf("--source is required")
		}
		cfg, err := config.PrepareDataDir(*dataDir)
		if err != nil {
			return err
		}
		manager := site.NewManager(cfg, nil, nil)
		cloned, err := manager.CloneSite(*source)
		if err != nil {
			return err
		}
		_, _ = fmt.Fprintf(stdout, "address: %s\nprivatekey: %s\n", cloned.Address, cloned.PrivateKey)
		return nil
	default:
		return fmt.Errorf("unknown site subcommand: %s", args[0])
	}
}

func init() {
	_ = os.Stdout
}
