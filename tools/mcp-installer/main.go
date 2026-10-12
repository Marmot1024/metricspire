// metricspire-install installs a local header helper and verifies native Codex MCP.
// Databricks CLI owns user authentication and token refresh.
package main

import (
	"bytes"
	"context"
	"crypto/sha256"
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
	"reflect"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/marmot1024/metricspire/internal/codexmcp"

	"github.com/marmot1024/metricspire/internal/cliauth"
	"github.com/pelletier/go-toml/v2"
)

//go:embed skill/SKILL.md
var skill []byte

type connection struct {
	Upgrade       bool   `json:"-"`
	Profile       string `json:"-"`
	URL           string `json:"url"`
	WorkspaceHost string `json:"workspace_host"`
	Helper        string `json:"-"`
	HelperPath    string `json:"-"`
	HelperData    []byte `json:"-"`
	Migrate       bool   `json:"-"`
}

func (c connection) validate() error {
	u, err := url.Parse(c.URL)
	if err != nil || u.Scheme != "https" || u.Hostname() == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || u.Path != "/api/v1/mcp" {
		return errors.New("安装包需要无凭据的 HTTPS MCP 地址")
	}
	return cliauth.Validate("validation", c.WorkspaceHost)
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
	bundle := flags.String("bundle", "", "公开连接参数")
	profile := flags.String("profile", "", "已有 Databricks CLI 用户授权 profile")
	workspace := flags.String("workspace-host", "", "仅供 header helper 使用")
	headers := flags.Bool("headers", false, "输出认证 headers，仅供 Codex 内部调用")
	check := flags.Bool("check", false, "验证配置、工具及实际连接")
	upgrade := flags.Bool("upgrade", false, "备份并升级已验证的同 profile/workspace helper")
	migrate := flags.Bool("migrate-oauth", false, "备份并停用同地址旧 OAuth 连接，保留原授权")
	if err := flags.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return err
	}
	if flags.NArg() != 0 {
		return errors.New("不支持额外参数")
	}
	if *headers {
		h, err := cliauth.Headers(*profile, *workspace)
		if err != nil {
			return err
		}
		return json.NewEncoder(out).Encode(h)
	}
	data, err := os.ReadFile(*bundle)
	if err != nil {
		return errors.New("无法读取安装包连接参数")
	}
	var c connection
	if err := cliauth.DecodeBundle(data, &c); err != nil {
		return err
	}
	if err := c.validate(); err != nil {
		return err
	}
	if err := cliauth.Validate(*profile, c.WorkspaceHost); err != nil {
		return err
	}
	codex, err := exec.LookPath("codex")
	if err != nil {
		return errors.New("请先安装 Codex CLI")
	}
	version, err := capture(codex, "--version")
	if err != nil || !supportedCodex(string(version)) {
		return errors.New("需要 Codex CLI 0.162.0 或更新版本")
	}
	// Check existing CLI authentication before changing any client configuration.
	if _, err := cliauth.Headers(*profile, c.WorkspaceHost); err != nil {
		return err
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return err
	}
	codexHome := os.Getenv("CODEX_HOME")
	if codexHome == "" {
		codexHome = filepath.Join(home, ".codex")
	}
	codexHome, err = filepath.Abs(codexHome)
	if err != nil {
		return err
	}
	executable, err := os.Executable()
	if err != nil {
		return err
	}
	c.HelperData, err = os.ReadFile(executable)
	if err != nil {
		return errors.New("无法读取安装 helper")
	}
	digest := fmt.Sprintf("%x", sha256.Sum256(c.HelperData))
	c.HelperPath = filepath.Join(codexHome, "metricspire", "bin", digest)
	c.Helper = cliauth.Command(c.HelperPath, *profile, c.WorkspaceHost)
	c.Migrate = *migrate
	c.Upgrade = *upgrade
	c.Profile = *profile
	if _, err := install(filepath.Join(codexHome, "config.toml"), filepath.Join(home, ".agents", "skills", "metricspire", "SKILL.md"), c, *check); err != nil {
		return err
	}
	if _, err := authStatus(codex, codexHome, home, c); err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	client, err := codexmcp.StartServer(ctx, codex, home, codexEnvironment(codexHome), cliauth.Server, "unknown")
	if err != nil {
		return err
	}
	defer client.Close()
	if err := client.VerifyConnection(c.URL, c.Helper); err != nil {
		return err
	}
	if _, err := client.Verify(codexmcp.RequiredTools); err != nil {
		return err
	}
	if _, err := client.Namespaces(); err != nil {
		return err
	}
	fmt.Fprintln(out, "Databricks CLI 授权、原生 MCP 初始化、7 个基础工具和 list_namespaces 实际调用验证通过。")
	fmt.Fprintln(out, "连接名：metricspire-cli；已打开的 Codex 会话需重连。没有发起登录或清理旧授权。")
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
		if s.Name == cliauth.Server {
			if !s.Enabled || s.Transport.Type != "streamable_http" || s.Transport.URL != c.URL {
				return "", errors.New("Codex 实际加载的连接与安装包不同；请检查项目或托管配置，未发起登录")
			}
			if s.AuthStatus != "unknown" {
				return "", errors.New("CLI 连接存在优先级更高的 OAuth 或 bearer 授权；已停止，未清理缓存")
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
	upgradeSkill := oldSkill != nil && !bytes.Equal(oldSkill, skill)
	if upgradeSkill && !((c.Migrate || c.Upgrade) && knownPreviousSkill(oldSkill)) {
		return false, errors.New("已有不同的 MetricSpire skill，已保留；请维护者核对后处理，不自动覆盖")
	}
	if check {
		if created || oldSkill == nil || upgradeSkill {
			return false, errors.New("配置或 MetricSpire skill 尚未安装；只检查模式未修改文件")
		}
		if err := installHelper(c, true); err != nil {
			return false, err
		}
		return false, nil
	}
	if err := installHelper(c, false); err != nil {
		return false, err
	}
	if created {
		if (c.Migrate || c.Upgrade) && original != nil {
			if err := backup(configPath, original); err != nil {
				return false, err
			}
		}
		if err := writeUnchanged(configPath, original, next); err != nil {
			return false, err
		}
	}
	if oldSkill == nil || upgradeSkill {
		if err := os.MkdirAll(filepath.Dir(skillPath), 0700); err != nil {
			return false, errors.New("连接配置已保留，但无法创建 skill 目录；请修复目录权限后重试")
		}
		if upgradeSkill {
			if err := backup(skillPath, oldSkill); err != nil {
				return false, err
			}
		}
		if err := writeUnchanged(skillPath, oldSkill, skill); err != nil {
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
	if toml.Unmarshal(original, &config) != nil {
		return nil, false, errors.New("现有配置不是有效 TOML；已保留")
	}
	servers, ok := config["mcp_servers"].(map[string]any)
	if config["mcp_servers"] != nil && !ok {
		return nil, false, errors.New("现有 MCP 配置不兼容")
	}
	next := append([]byte(nil), original...)
	if old, exists := servers["metricspire"]; exists {
		s, ok := old.(map[string]any)
		if !ok || s["url"] != c.URL {
			return nil, false, errors.New("旧 metricspire 连接与目标不同；已保留")
		}
		if s["enabled"] != false {
			if !c.Migrate {
				return nil, false, errors.New("发现旧 OAuth 连接；使用 --migrate-oauth 备份并停用后迁移")
			}
			if _, ok := s["oauth"].(map[string]any); !ok {
				return nil, false, errors.New("旧连接不是可迁移的 OAuth 配置")
			}
			for _, key := range []string{"command", "http_headers_helper", "bearer_token_env_var", "http_headers", "env_http_headers"} {
				if _, exists := s[key]; exists {
					return nil, false, errors.New("旧连接混有其他认证；已保留")
				}
			}
			// Only edit the unquoted canonical table, preserving every other byte.
			re := regexp.MustCompile(`(?m)^\[mcp_servers\.metricspire\][ \t]*(?:#[^\n]*)?\r?$`)
			loc := re.FindIndex(next)
			if loc == nil {
				return nil, false, errors.New("旧连接布局无法安全迁移；已保留")
			}
			end := len(next)
			if n := bytes.Index(next[loc[1]:], []byte("\n[")); n >= 0 {
				end = loc[1] + n
			}
			body := next[loc[1]:end]
			enabled := regexp.MustCompile(`(?m)^[ \t]*enabled[ \t]*=[^\n]*`)
			if enabled.Match(body) {
				body = enabled.ReplaceAll(body, []byte("enabled = false"))
			} else {
				body = append([]byte("\nenabled = false"), body...)
			}
			next = append(append(append([]byte{}, next[:loc[1]]...), body...), next[end:]...)
			var verify map[string]any
			if toml.Unmarshal(next, &verify) != nil {
				return nil, false, errors.New("迁移配置校验失败")
			}
			expected := s["enabled"]
			s["enabled"] = false
			same := reflect.DeepEqual(config, verify)
			s["enabled"] = expected
			if !same {
				return nil, false, errors.New("迁移会影响其他配置；已停止")
			}
		}
	}
	if existing, exists := servers[cliauth.Server]; exists {
		s, ok := existing.(map[string]any)
		if !ok || s["url"] != c.URL || s["enabled"] == false {
			return nil, false, errors.New("已有 CLI 连接与安装包、profile 或 helper 不同；已保留")
		}
		for _, key := range []string{"oauth", "command", "bearer_token_env_var", "http_headers", "env_http_headers"} {
			if _, exists := s[key]; exists {
				return nil, false, errors.New("CLI 连接混有其他授权；已保留")
			}
		}
		if s["http_headers_helper"] != c.Helper {
			oldHelper, ok := s["http_headers_helper"].(string)
			if !c.Upgrade || !ok || !cliauth.ManagedCommand(oldHelper, filepath.Dir(c.HelperPath), c.Profile, c.WorkspaceHost) {
				return nil, false, errors.New("helper 版本不同；核对后使用 --upgrade，profile/workspace 不可变")
			}
			re := regexp.MustCompile(`(?m)^\[mcp_servers\.metricspire-cli\][ \t]*(?:#[^\n]*)?\r?$`)
			loc := re.FindIndex(next)
			if loc == nil {
				return nil, false, errors.New("CLI 配置布局无法安全升级")
			}
			end := len(next)
			if n := bytes.Index(next[loc[1]:], []byte("\n[")); n >= 0 {
				end = loc[1] + n
			}
			body := next[loc[1]:end]
			field := regexp.MustCompile(`(?m)^[ \t]*http_headers_helper[ \t]*=[^\n]*`)
			if len(field.FindAllIndex(body, -1)) != 1 {
				return nil, false, errors.New("helper 配置无法安全升级")
			}
			quoted, _ := json.Marshal(c.Helper)
			body = field.ReplaceAllFunc(body, func([]byte) []byte { return append([]byte("http_headers_helper = "), quoted...) })
			upgraded := append(append(append([]byte{}, next[:loc[1]]...), body...), next[end:]...)
			var before, after map[string]any
			if toml.Unmarshal(next, &before) != nil || toml.Unmarshal(upgraded, &after) != nil {
				return nil, false, errors.New("升级配置校验失败")
			}
			before["mcp_servers"].(map[string]any)[cliauth.Server].(map[string]any)["http_headers_helper"] = c.Helper
			if !reflect.DeepEqual(before, after) {
				return nil, false, errors.New("升级会影响其他配置")
			}
			next = upgraded
		}
		return next, !bytes.Equal(original, next), nil
	}
	quoted := func(s string) string { b, _ := json.Marshal(s); return string(b) }
	block := fmt.Sprintf("\n\n[mcp_servers.%s]\nurl = %s\nhttp_headers_helper = %s\n", cliauth.Server, quoted(c.URL), quoted(c.Helper))
	next = append(next, block...)
	var verify map[string]any
	if toml.Unmarshal(next, &verify) != nil {
		return nil, false, errors.New("现有配置布局不能安全追加连接；已保留")
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

func backup(path string, data []byte) error {
	name := fmt.Sprintf("%s.metricspire-backup-%x", path, sha256.Sum256(data))
	existing, err := readRegular(name)
	if err != nil {
		return err
	}
	if existing != nil {
		if bytes.Equal(existing, data) {
			return nil
		}
		return errors.New("备份文件冲突")
	}
	return writeUnchanged(name, nil, data)
}
func installHelper(c connection, check bool) error {
	current, err := readRegular(c.HelperPath)
	if err != nil {
		return err
	}
	if current != nil {
		info, err := os.Stat(c.HelperPath)
		if !bytes.Equal(current, c.HelperData) || err != nil || info.Mode().Perm() != 0700 {
			return errors.New("已安装 helper 内容或权限不一致")
		}
		return nil
	}
	if check {
		return errors.New("本版本 header helper 尚未安装")
	}
	if err := os.MkdirAll(filepath.Dir(c.HelperPath), 0700); err != nil {
		return err
	}
	if err := writeUnchanged(c.HelperPath, nil, c.HelperData); err != nil {
		return err
	}
	return os.Chmod(c.HelperPath, 0700)
}
func knownPreviousSkill(data []byte) bool {
	digest := fmt.Sprintf("%x", sha256.Sum256(data))
	return digest == "2f9fa1f29192836de75c28a44595f4975119cc299ed274845ae2c59efab8587e"
}
