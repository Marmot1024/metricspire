// metricspire-install is an ephemeral setup helper, embedded in the distributed
// shell installer. It never queries data or implements a credential store.
package main

import (
	"bytes"
	"context"
	_ "embed"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/pelletier/go-toml/v2"
	"golang.org/x/term"
)

//go:embed skill/SKILL.md
var skill []byte

type connection struct {
	URL          string `json:"url"`
	ClientID     string `json:"client_id"`
	CallbackURL  string `json:"callback_url"`
	CallbackPort int    `json:"callback_port"`
}

func (c connection) validate() error {
	u, err := url.Parse(c.URL)
	if err != nil || u.Scheme != "https" || u.Hostname() == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		return errors.New("安装包的 MCP 地址必须是无凭据、无查询参数的 HTTPS 地址")
	}
	if c.ClientID == "" || strings.ContainsAny(c.ClientID, "\r\n\x00") {
		return errors.New("安装包缺少有效的 public OAuth client ID")
	}
	u, err = url.Parse(c.CallbackURL)
	if err != nil || u.Scheme != "http" || (u.Hostname() != "127.0.0.1" && u.Hostname() != "localhost" && u.Hostname() != "::1") || u.User != nil || u.Path == "" || u.RawQuery != "" || u.Fragment != "" || c.CallbackPort < 1 || c.CallbackPort > 65535 {
		return errors.New("安装包需要已注册的本机 OAuth 回调地址及端口")
	}
	if u.Port() != "" && u.Port() != strconv.Itoa(c.CallbackPort) {
		return errors.New("安装包的回调地址与监听端口不一致")
	}
	return nil
}

func main() {
	if err := run(os.Args[1:], os.Stdout); err != nil {
		fmt.Fprintln(os.Stderr, "MetricSpire:", err)
		os.Exit(1)
	}
}

func run(args []string, out io.Writer) error {
	flags := flag.NewFlagSet("metricspire-install", flag.ContinueOnError)
	flags.SetOutput(out)
	flags.Usage = func() {
		fmt.Fprintln(out, "用法：sh metricspire-install.sh [--check | --no-login]\n--check：只检查；--no-login：只安装配置和使用 skill。")
	}
	bundle := flags.String("bundle", "", "安装包内置的公开连接参数")
	check := flags.Bool("check", false, "只检查，不修改配置、skill 或授权")
	noLogin := flags.Bool("no-login", false, "安装配置和 skill，不发起登录")
	if err := flags.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return err
	}
	if flags.NArg() != 0 {
		return errors.New("不支持额外参数；使用 --help 查看用法")
	}
	data, err := os.ReadFile(*bundle)
	if err != nil {
		return errors.New("无法读取安装包连接参数")
	}
	var c connection
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&c) != nil {
		return errors.New("安装包连接参数无效")
	}
	if err := c.validate(); err != nil {
		return err
	}
	codex, err := exec.LookPath("codex")
	if err != nil {
		return errors.New("请先安装 Codex CLI，并确认终端可运行 codex；然后重新运行本文件")
	}
	version, err := capture(codex, "--version")
	if err != nil || !supportedCodex(string(version)) {
		return errors.New("需要 Codex CLI 0.162.0 或更新版本；请更新后重新运行")
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return errors.New("无法确定用户目录")
	}
	codexHome := os.Getenv("CODEX_HOME")
	if codexHome == "" {
		codexHome = filepath.Join(home, ".codex")
	}
	codexHome, err = filepath.Abs(codexHome)
	if err != nil {
		return errors.New("无法确定 Codex 配置目录")
	}
	configPath := filepath.Join(codexHome, "config.toml")
	skillPath := filepath.Join(home, ".agents", "skills", "metricspire", "SKILL.md")
	// Run CLI inspection from the user directory, not an arbitrary download or
	// repository directory with a project-local configuration override.
	inspect := func() (string, error) { return authStatus(codex, codexHome, home, c) }
	fmt.Fprintf(out, "MetricSpire MCP：%s\n", c.URL)
	created, err := install(configPath, skillPath, c, *check)
	if err != nil {
		return err
	}
	if *check {
		fmt.Fprintln(out, "配置和使用 skill 一致；未修改配置或 skill。")
	} else {
		fmt.Fprintln(out, "配置和使用 skill 已就绪；已有一致文件保持原样。")
	}
	status, err := inspect()
	if err != nil {
		return err
	}
	if status == "o_auth" {
		fmt.Fprintln(out, "Codex 已保存 OAuth 授权，本次未登录。")
	} else if status == "not_logged_in" {
		// Only a genuinely new connection starts browser login automatically.
		// A repeated installer run must not replace another window's credentials.
		if *check || *noLogin || !created {
			fmt.Fprintln(out, "Codex 当前未报告已登录。需要登录时，在普通终端运行：codex mcp login metricspire")
			return errors.New("连接已配置，登录尚未验证；本次未发起授权")
		}
		if !term.IsTerminal(int(os.Stdin.Fd())) {
			return errors.New("首次登录需要交互终端；请运行 codex mcp login metricspire")
		}
		fmt.Fprintln(out, "首次连接：将使用 Codex 原生浏览器登录，不指定额外 scopes。")
		cmd := exec.Command(codex, "mcp", "login", "metricspire")
		cmd.Dir, cmd.Env = home, codexEnvironment(codexHome)
		cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, out, os.Stderr
		if cmd.Run() != nil {
			return errors.New("Codex 登录未完成，已停止；未重试或更改权限")
		}
		status, err = inspect()
		if err != nil {
			return err
		}
		if status != "o_auth" {
			return errors.New("浏览器流程结束，但 Codex 未报告 OAuth 授权；已停止")
		}
		fmt.Fprintln(out, "Codex 已保存 OAuth 授权。")
	} else {
		return errors.New("Codex 无法确认原生 OAuth 状态；已停止，未登录或更改权限")
	}
	fmt.Fprintln(out, "请打开新的 Codex 会话，发送：使用 MetricSpire，调用 list_namespaces 验证连接。")
	fmt.Fprintln(out, "工具接入以该调用成功为准。")
	return nil
}

var versionPattern = regexp.MustCompile(`^codex-cli (\d+)\.(\d+)\.(\d+)(?:\s|$)`)

func supportedCodex(version string) bool {
	m := versionPattern.FindStringSubmatch(strings.TrimSpace(version))
	if m == nil {
		return false
	}
	major, _ := strconv.Atoi(m[1])
	minor, _ := strconv.Atoi(m[2])
	return major > 0 || minor >= 162
}

func codexEnvironment(home string) []string {
	env := []string{}
	for _, value := range os.Environ() {
		if !strings.HasPrefix(value, "CODEX_HOME=") {
			env = append(env, value)
		}
	}
	return append(env, "CODEX_HOME="+home)
}

func capture(binary string, args ...string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	return exec.CommandContext(ctx, binary, args...).Output()
}

func authStatus(binary, codexHome, home string, c connection) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, binary, "mcp", "list", "--json")
	cmd.Dir, cmd.Env = home, codexEnvironment(codexHome)
	data, err := cmd.Output() // Never print raw config, helpers or auth errors.
	if err != nil {
		return "", errors.New("无法读取 Codex 连接状态；未发起登录，请在普通终端检查 codex mcp list")
	}
	var servers []struct {
		Name       string `json:"name"`
		Enabled    bool   `json:"enabled"`
		AuthStatus string `json:"auth_status"`
		Transport  struct {
			Type string `json:"type"`
			URL  string `json:"url"`
		} `json:"transport"`
	}
	if json.Unmarshal(data, &servers) != nil {
		return "", errors.New("Codex 返回的连接状态格式不兼容；未发起登录")
	}
	for _, s := range servers {
		if s.Name == "metricspire" {
			if !s.Enabled || s.Transport.Type != "streamable_http" || s.Transport.URL != c.URL {
				return "", errors.New("Codex 实际加载的连接与安装包不同；请检查项目或托管配置，未发起登录")
			}
			return s.AuthStatus, nil
		}
	}
	return "", errors.New("Codex 未加载 metricspire 连接；请检查配置，未发起登录")
}

func install(configPath, skillPath string, c connection, check bool) (bool, error) {
	if !check {
		if err := os.MkdirAll(filepath.Dir(configPath), 0700); err != nil {
			return false, errors.New("无法创建 Codex 配置目录")
		}
		lock, err := os.OpenFile(configPath+".metricspire-install.lock", os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
		if err != nil {
			return false, errors.New("另一安装任务可能正在运行；本次未修改配置")
		}
		_ = lock.Close()
		defer os.Remove(configPath + ".metricspire-install.lock")
	}
	original, err := readRegular(configPath)
	if err != nil {
		return false, err
	}
	next, created, err := configure(original, c)
	if err != nil {
		return false, err
	}
	oldSkill, err := readRegular(skillPath)
	if err != nil {
		return false, err
	}
	if oldSkill != nil && !bytes.Equal(oldSkill, skill) {
		return false, errors.New("已有不同的 MetricSpire skill，已保留；请维护者核对后处理，不自动覆盖")
	}
	if check {
		if created || oldSkill == nil {
			return false, errors.New("配置或 MetricSpire skill 尚未安装；只检查模式未修改文件")
		}
		return false, nil
	}
	if created {
		if err := writeUnchanged(configPath, original, next); err != nil {
			return false, err
		}
	}
	if oldSkill == nil {
		if err := os.MkdirAll(filepath.Dir(skillPath), 0700); err != nil {
			return false, errors.New("连接配置已保留，但无法创建 skill 目录；请修复目录权限后重试")
		}
		if err := writeUnchanged(skillPath, nil, skill); err != nil {
			return false, err
		}
	}
	return created, nil
}

func readRegular(path string) ([]byte, error) {
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil || !info.Mode().IsRegular() {
		return nil, errors.New("目标文件不可读或不是普通文件；已停止，未覆盖")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, errors.New("无法读取现有安装文件；已停止")
	}
	return data, nil
}

func configure(original []byte, c connection) ([]byte, bool, error) {
	var config map[string]any
	if err := toml.Unmarshal(original, &config); err != nil {
		return nil, false, errors.New("现有 Codex 配置不是有效 TOML；已保留原文件，请先修复")
	}
	servers, ok := config["mcp_servers"].(map[string]any)
	if config["mcp_servers"] != nil && !ok {
		return nil, false, errors.New("现有 MCP 配置格式不兼容；已保留")
	}
	if existing, exists := servers["metricspire"]; exists {
		s, ok := existing.(map[string]any)
		if !ok {
			return nil, false, errors.New("现有 metricspire 连接格式不兼容；已保留")
		}
		o, ok := s["oauth"].(map[string]any)
		if !ok || s["url"] != c.URL || o["client_id"] != c.ClientID || o["callback_url"] != c.CallbackURL || o["callback_port"] != int64(c.CallbackPort) {
			return nil, false, errors.New("已有 metricspire 连接与安装包参数冲突；已保留原配置和授权，请维护者核对环境、client ID 和回调")
		}
		if s["enabled"] == false {
			return nil, false, errors.New("现有 metricspire 连接被禁用；已保留，未自动启用")
		}
		for _, key := range []string{"command", "bearer_token_env_var", "http_headers", "env_http_headers", "http_headers_helper"} {
			if _, exists := s[key]; exists {
				return nil, false, errors.New("现有 metricspire 连接含其他认证或传输配置；已保留，不混用原生 OAuth")
			}
		}
		return original, false, nil
	}
	// JSON quoted strings are also valid TOML basic strings for these validated
	// public fields. Existing bytes and comments are never reserialized.
	quoted := func(s string) string { data, _ := json.Marshal(s); return string(data) }
	block := fmt.Sprintf("\n\n[mcp_servers.metricspire]\nurl = %s\n\n[mcp_servers.metricspire.oauth]\nclient_id = %s\ncallback_url = %s\ncallback_port = %d\n", quoted(c.URL), quoted(c.ClientID), quoted(c.CallbackURL), c.CallbackPort)
	next := append(append([]byte(nil), original...), block...)
	var verified map[string]any
	if err := toml.Unmarshal(next, &verified); err != nil {
		return nil, false, errors.New("现有配置布局不能安全追加连接；已保留原文件，请维护者核对")
	}
	return next, true, nil
}

func writeUnchanged(path string, original, next []byte) error {
	file, err := os.CreateTemp(filepath.Dir(path), ".metricspire-install-*")
	if err != nil {
		return errors.New("无法创建安装临时文件")
	}
	defer os.Remove(file.Name())
	if _, err := file.Write(next); err != nil {
		_ = file.Close()
		return errors.New("无法写入安装临时文件")
	}
	if file.Sync() != nil {
		_ = file.Close()
		return errors.New("无法保存安装临时文件")
	}
	if file.Close() != nil {
		return errors.New("无法关闭安装临时文件")
	}
	current, err := readRegular(path)
	if err != nil {
		return err
	}
	if (current == nil) != (original == nil) || !bytes.Equal(current, original) {
		return errors.New("安装期间文件发生变化；已停止，请稍后重试")
	}
	if os.Rename(file.Name(), path) != nil {
		return errors.New("无法保存安装文件")
	}
	return nil
}
