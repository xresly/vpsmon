// vpsmon — a tiny VPS monitor panel: one binary that can act as a
// monitoring client (agent), as a display server (aggregator), or
// both. Configured through its WebUI after a one-click install,
// similar in spirit to 3x-ui.
//
// Subcommands:
//   vpsmon run        run the daemon (default; used by systemd)
//   vpsmon init       create the initial config (used by install.sh)
//   vpsmon menu       interactive management menu (like x-ui)
//   vpsmon resetpass  set panel username/password from the shell
//   vpsmon version
//
// Go standard library only.
package main

import (
	"bufio"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
)

const version = "1.0.0"

// bytesPerG — all traffic figures in config, reports and the
// dashboard are in G (1 G = 1024^3 bytes).
const bytesPerG = 1024 * 1024 * 1024

func removeFileIfExists(path string) error {
	err := os.Remove(path)
	if err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
}

func main() {
	args := os.Args[1:]
	sub := "run"
	if len(args) > 0 && !strings.HasPrefix(args[0], "-") {
		sub = args[0]
		args = args[1:]
	}
	switch sub {
	case "run":
		runDaemon(args)
	case "init":
		runInit(args)
	case "menu":
		runMenu(args)
	case "resetpass":
		runResetPass(args)
	case "version", "-version", "--version":
		fmt.Println("vpsmon", version)
	default:
		fmt.Fprintf(os.Stderr, "usage: vpsmon [run|init|menu|resetpass|version]\n")
		os.Exit(2)
	}
}

func configFlag(fs *flag.FlagSet, args []string) string {
	var p string
	fs.StringVar(&p, "config", "", "path to config JSON")
	_ = fs.Parse(args)
	if p == "" {
		for _, cand := range []string{"/etc/vpsmon/config.json", "./vpsmon.json"} {
			if _, err := os.Stat(cand); err == nil {
				return cand
			}
		}
		return "/etc/vpsmon/config.json"
	}
	return p
}

// ---------- daemon ----------

func runDaemon(args []string) {
	fs := flag.NewFlagSet("run", flag.ExitOnError)
	cfgPath := configFlag(fs, args)
	cfg, err := loadAppConfig(cfgPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "vpsmon: cannot load config: %v\n先运行 install.sh 或 vpsmon init 生成配置。\n", err)
		os.Exit(1)
	}
	if cfg.Web.Password == "" {
		fmt.Fprintln(os.Stderr, "vpsmon: 面板密码未设置，先运行 vpsmon init 或 vpsmon resetpass。")
		os.Exit(1)
	}
	app := newApp(cfgPath, cfg)
	if err := app.startHTTP(); err != nil {
		fmt.Fprintf(os.Stderr, "vpsmon: listen: %v\n", err)
		os.Exit(1)
	}
	app.applyClient()

	sig := make(chan os.Signal, 2)
	signal.Notify(sig, syscall.SIGINT, syscall.SIGTERM)
	<-sig
	app.shutdownHTTP()
}

// ---------- init (used by install.sh) ----------

func runInit(args []string) {
	fs := flag.NewFlagSet("init", flag.ExitOnError)
	var cfgPath, username, password, ingest, api string
	var port int
	var force bool
	fs.StringVar(&cfgPath, "config", "/etc/vpsmon/config.json", "config path")
	fs.IntVar(&port, "port", 2053, "webui port")
	fs.StringVar(&username, "username", "admin", "panel username")
	fs.StringVar(&password, "password", "", "panel password (empty = generate)")
	fs.StringVar(&ingest, "ingest-token", "", "ingest token (empty = generate)")
	fs.StringVar(&api, "api-token", "", "api token (empty = generate)")
	fs.BoolVar(&force, "force", false, "overwrite existing config")
	_ = fs.Parse(args)

	if _, err := os.Stat(cfgPath); err == nil && !force {
		fmt.Fprintf(os.Stderr, "vpsmon init: %s 已存在（用 -force 覆盖）\n", cfgPath)
		os.Exit(1)
	}
	if password == "" {
		password = randomPassword(12)
	}
	hash, err := hashPassword(password)
	if err != nil {
		fmt.Fprintln(os.Stderr, "vpsmon init:", err)
		os.Exit(1)
	}
	if ingest == "" {
		ingest = randomTokenHex(16)
	}
	if api == "" {
		api = randomTokenHex(16)
	}
	cfg := &AppConfig{}
	cfg.Web.Port = port
	cfg.Web.Username = username
	cfg.Web.Password = hash
	cfg.Web.APIToken = api
	cfg.Server.IngestToken = ingest
	cfg.applyDefaults()
	if err := cfg.save(cfgPath); err != nil {
		fmt.Fprintln(os.Stderr, "vpsmon init:", err)
		os.Exit(1)
	}
	fmt.Printf("配置已写入 %s\n  WebUI 端口: %d\n  用户名: %s\n  密码: %s\n  上报 token: %s\n  API token: %s\n", cfgPath, port, username, password, ingest, api)
}

// ---------- resetpass ----------

func runResetPass(args []string) {
	fs := flag.NewFlagSet("resetpass", flag.ExitOnError)
	var cfgPath, username, password string
	fs.StringVar(&cfgPath, "config", "", "path to config JSON")
	fs.StringVar(&username, "username", "", "new username (empty = keep)")
	fs.StringVar(&password, "password", "", "new password (required)")
	_ = fs.Parse(args)
	if cfgPath == "" {
		cfgPath = "/etc/vpsmon/config.json"
	}
	if password == "" {
		fmt.Fprintln(os.Stderr, "vpsmon resetpass: -password 必填")
		os.Exit(2)
	}
	cfg, err := loadAppConfig(cfgPath)
	if err != nil {
		fmt.Fprintln(os.Stderr, "vpsmon resetpass:", err)
		os.Exit(1)
	}
	if username != "" {
		cfg.Web.Username = username
	}
	hash, err := hashPassword(password)
	if err != nil {
		fmt.Fprintln(os.Stderr, "vpsmon resetpass:", err)
		os.Exit(1)
	}
	cfg.Web.Password = hash
	if err := cfg.save(cfgPath); err != nil {
		fmt.Fprintln(os.Stderr, "vpsmon resetpass:", err)
		os.Exit(1)
	}
	fmt.Printf("已更新面板凭据（用户 %s）。若服务正在运行，执行 systemctl restart vpsmon 生效。\n", cfg.Web.Username)
}

// ---------- interactive menu (like x-ui) ----------

func systemctl(args ...string) {
	cmd := exec.Command("systemctl", args...)
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		fmt.Println("systemctl 执行失败（本机可能没有 systemd）:", err)
	}
}

func runMenu(args []string) {
	fs := flag.NewFlagSet("menu", flag.ExitOnError)
	cfgPath := configFlag(fs, args)
	in := bufio.NewReader(os.Stdin)
	prompt := func(label string) string {
		fmt.Print(label)
		s, _ := in.ReadString('\n')
		return strings.TrimSpace(s)
	}
	for {
		cfg, cfgErr := loadAppConfig(cfgPath)
		fmt.Println()
		fmt.Println("======== vpsmon 管理菜单 ========")
		if cfgErr == nil {
			fmt.Printf("面板: 端口 %d · 用户 %s · 服务端 %s · 客户端 %s\n",
				cfg.Web.Port, cfg.Web.Username, onoff(cfg.Server.Enabled), onoff(cfg.Client.Enabled))
		} else {
			fmt.Println("配置未找到:", cfgErr)
		}
		fmt.Println(" 1. 启动服务")
		fmt.Println(" 2. 停止服务")
		fmt.Println(" 3. 重启服务")
		fmt.Println(" 4. 查看服务状态")
		fmt.Println(" 5. 修改 WebUI 端口")
		fmt.Println(" 6. 重置用户名 / 密码")
		fmt.Println(" 7. 重新生成上报 token")
		fmt.Println(" 8. 查看当前配置")
		fmt.Println(" 0. 退出")
		switch prompt("请选择: ") {
		case "1":
			systemctl("start", "vpsmon")
		case "2":
			systemctl("stop", "vpsmon")
		case "3":
			systemctl("restart", "vpsmon")
		case "4":
			systemctl("status", "vpsmon", "--no-pager")
		case "5":
			if cfgErr != nil {
				break
			}
			if v := prompt("新端口: "); v != "" {
				if n, err := strconv.Atoi(v); err == nil && n > 0 && n < 65536 {
					cfg.Web.Port = n
					if err := cfg.save(cfgPath); err == nil {
						fmt.Println("端口已改为", n, "，重启服务生效")
						systemctl("restart", "vpsmon")
					}
				} else {
					fmt.Println("端口不合法")
				}
			}
		case "6":
			if cfgErr != nil {
				break
			}
			user := prompt("用户名（回车保持不变）: ")
			pass := prompt("新密码: ")
			if pass == "" {
				fmt.Println("密码不能为空，已取消")
				break
			}
			if user != "" {
				cfg.Web.Username = user
			}
			hash, err := hashPassword(pass)
			if err != nil {
				fmt.Println("失败:", err)
				break
			}
			cfg.Web.Password = hash
			if err := cfg.save(cfgPath); err == nil {
				fmt.Println("凭据已更新，重启服务生效")
				systemctl("restart", "vpsmon")
			}
		case "7":
			if cfgErr != nil {
				break
			}
			cfg.Server.IngestToken = randomTokenHex(16)
			if err := cfg.save(cfgPath); err == nil {
				fmt.Println("新的上报 token:", cfg.Server.IngestToken)
				fmt.Println("注意：所有客户端都要换成新 token。重启服务生效")
				systemctl("restart", "vpsmon")
			}
		case "8":
			if cfgErr != nil {
				break
			}
			fmt.Printf("配置路径: %s\nWebUI 端口: %d\n用户名: %s\nAPI token: %s\n服务端: %s，上报 token: %s\n客户端: %s，节点名: %s，服务端地址: %s\n状态文件: %s\n",
				cfgPath, cfg.Web.Port, cfg.Web.Username, cfg.Web.APIToken,
				onoff(cfg.Server.Enabled), cfg.Server.IngestToken,
				onoff(cfg.Client.Enabled), cfg.Client.NodeName, cfg.Client.ServerURL,
				cfg.StateFile)
		case "0":
			return
		default:
			fmt.Println("无效选择")
		}
	}
}

func onoff(b bool) string {
	if b {
		return "开"
	}
	return "关"
}
