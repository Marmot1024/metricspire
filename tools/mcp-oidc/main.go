// metricspire-oidc installs a standard native OAuth MCP connection. The client
// owns login, credential storage and refresh; this program never reads tokens.
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"time"

	"github.com/marmot1024/metricspire/internal/cliauth"
	"github.com/marmot1024/metricspire/internal/codexmcp"
	"github.com/pelletier/go-toml/v2"
)

const serverName = "metricspire-oidc"

type connection struct {
	URL      string   `json:"url"`
	Issuer   string   `json:"issuer"`
	ClientID string   `json:"client_id"`
	Scopes   []string `json:"scopes"`
}

func validURL(raw string, development bool) (*url.URL, error) {
	u, err := url.Parse(raw)
	if err != nil || u.Hostname() == "" || u.User != nil || u.RawQuery != "" || u.ForceQuery || u.Fragment != "" || strings.ContainsAny(raw, "\r\n\x00") {
		return nil, errors.New("connection requires a public URL without credentials, query or fragment")
	}
	ip := net.ParseIP(u.Hostname())
	loopback := u.Hostname() == "localhost" || (ip != nil && ip.IsLoopback())
	if u.Scheme != "https" && !(development && loopback && u.Scheme == "http") {
		return nil, errors.New("HTTPS required (explicit development mode permits loopback HTTP only)")
	}
	return u, nil
}
func (c connection) validate(development bool) error {
	u, err := validURL(c.URL, development)
	if err != nil {
		return err
	}
	if u.Path != "/mcp" && u.Path != "/api/v1/mcp" {
		return errors.New("MCP URL must end in /mcp or /api/v1/mcp")
	}
	if _, err = validURL(c.Issuer, development); err != nil {
		return err
	}
	if strings.TrimSpace(c.ClientID) == "" || len(c.ClientID) > 512 || strings.ContainsAny(c.ClientID, "\r\n\x00") {
		return errors.New("a registered public OAuth client ID is required")
	}
	if len(c.Scopes) == 0 || len(c.Scopes) > 16 {
		return errors.New("configure the provider-approved scopes explicitly")
	}
	seen := map[string]bool{}
	for _, s := range c.Scopes {
		if s == "" || len(s) > 128 || strings.ContainsAny(s, " ,\t\r\n\x00") || seen[s] {
			return errors.New("scopes must be unique nonempty names")
		}
		seen[s] = true
	}
	return nil
}
func fetch(ctx context.Context, client *http.Client, endpoint string, out any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return errors.New("invalid discovery request")
	}
	res, err := client.Do(req)
	if err != nil {
		return errors.New("OAuth discovery unavailable")
	}
	defer res.Body.Close()
	if res.StatusCode != 200 {
		return errors.New("OAuth discovery did not succeed")
	}
	data, err := io.ReadAll(io.LimitReader(res.Body, (128<<10)+1))
	if err != nil || len(data) > 128<<10 {
		return errors.New("OAuth discovery exceeds size limit")
	}
	if json.Unmarshal(data, out) != nil {
		return errors.New("invalid OAuth discovery document")
	}
	return nil
}
func (c connection) discover(ctx context.Context, development bool) error {
	client := &http.Client{Timeout: 10 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	u, _ := url.Parse(c.URL)
	endpoint := u.Scheme + "://" + u.Host + "/.well-known/oauth-protected-resource" + u.EscapedPath()
	var resource struct {
		Resource string   `json:"resource"`
		Servers  []string `json:"authorization_servers"`
	}
	if err := fetch(ctx, client, endpoint, &resource); err != nil {
		return err
	}
	if resource.Resource != c.URL || len(resource.Servers) != 1 || resource.Servers[0] != c.Issuer {
		return errors.New("MCP resource or authorization issuer differs from the reviewed connection")
	}
	var provider struct {
		Issuer        string   `json:"issuer"`
		Authorization string   `json:"authorization_endpoint"`
		Token         string   `json:"token_endpoint"`
		Methods       []string `json:"code_challenge_methods_supported"`
	}
	if err := fetch(ctx, client, strings.TrimSuffix(c.Issuer, "/")+"/.well-known/openid-configuration", &provider); err != nil {
		return err
	}
	if provider.Issuer != c.Issuer {
		return errors.New("OIDC discovery issuer mismatch")
	}
	for _, endpoint := range []string{provider.Authorization, provider.Token} {
		if _, err := validURL(endpoint, development); err != nil {
			return errors.New("OIDC authorization/token endpoint is invalid")
		}
	}
	pkce := false
	for _, m := range provider.Methods {
		pkce = pkce || m == "S256"
	}
	if !pkce {
		return errors.New("public OAuth client requires advertised S256 PKCE")
	}
	return nil
}
func expected(c connection) map[string]any {
	return map[string]any{"url": c.URL, "oauth": map[string]any{"client_id": c.ClientID}}
}
func checkEntry(data []byte, c connection) (bool, error) {
	var config struct {
		Servers map[string]map[string]any `toml:"mcp_servers"`
	}
	if err := toml.Unmarshal(data, &config); err != nil {
		return false, errors.New("cannot parse existing Codex config")
	}
	entry, ok := config.Servers[serverName]
	if !ok {
		return false, nil
	}
	if !reflect.DeepEqual(entry, expected(c)) {
		return false, errors.New("existing metricspire-oidc configuration differs; preserve it and resolve the conflict explicitly")
	}
	return true, nil
}
func install(path string, c connection, check bool) error {
	if check {
		data, err := os.ReadFile(path)
		if err != nil {
			return errors.New("metricspire-oidc is not configured")
		}
		exists, err := checkEntry(data, c)
		if err != nil {
			return err
		}
		if !exists {
			return errors.New("metricspire-oidc is not configured")
		}
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return err
	}
	lock, err := os.OpenFile(path+".metricspire-oidc.lock", os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return errors.New("another OIDC installation is active; inspect its lock before retrying")
	}
	lock.Close()
	defer os.Remove(path + ".metricspire-oidc.lock")
	if info, err := os.Lstat(path); err == nil && !info.Mode().IsRegular() {
		return errors.New("Codex config must be a regular file")
	}
	original, err := os.ReadFile(path)
	if err != nil && !os.IsNotExist(err) {
		return err
	}
	exists, err := checkEntry(original, c)
	if err != nil {
		return err
	}
	if exists {
		return nil
	}
	if check {
		return errors.New("metricspire-oidc is not configured")
	}
	block, err := toml.Marshal(map[string]any{"mcp_servers": map[string]any{serverName: expected(c)}})
	if err != nil {
		return err
	}
	data := append(append(append([]byte{}, original...), '\n'), block...)
	if _, err := checkEntry(data, c); err != nil {
		return err
	}
	// Retain an exact private backup and fail if a concurrent editor changed the file.
	if len(original) > 0 {
		backup, err := os.OpenFile(path+".metricspire-oidc.backup", os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
		if err != nil {
			return errors.New("config backup already exists or cannot be created")
		}
		_, err = backup.Write(original)
		closeErr := backup.Close()
		if err != nil || closeErr != nil {
			return errors.New("cannot save config backup")
		}
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".metricspire-oidc-*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if _, err = tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err = tmp.Sync(); err != nil {
		tmp.Close()
		return err
	}
	if err = tmp.Close(); err != nil {
		return err
	}
	current, err := os.ReadFile(path)
	if err != nil && !os.IsNotExist(err) {
		return err
	}
	if !bytes.Equal(current, original) {
		return errors.New("Codex config changed during installation; no replacement performed")
	}
	return os.Rename(tmp.Name(), path)
}
func run(ctx context.Context, args []string, out io.Writer) error {
	flags := flag.NewFlagSet("metricspire-oidc", flag.ContinueOnError)
	flags.SetOutput(out)
	bundle := flags.String("bundle", "", "reviewed public connection JSON")
	check := flags.Bool("check", false, "verify existing native OAuth connection without changing config or logging in")
	configure := flags.Bool("configure-only", false, "install non-secret configuration; log in later with codex mcp login metricspire-oidc")
	development := flags.Bool("development-loopback", false, "permit loopback-only HTTP for isolated synthetic acceptance")
	if err := flags.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return err
	}
	if *check && *configure {
		return errors.New("--check and --configure-only are mutually exclusive")
	}
	if flags.NArg() != 0 {
		return errors.New("unexpected arguments")
	}
	raw, err := os.ReadFile(*bundle)
	if err != nil {
		return errors.New("cannot read connection bundle")
	}
	var c connection
	if err := cliauth.DecodeBundle(raw, &c); err != nil {
		return err
	}
	if err := c.validate(*development); err != nil {
		return err
	}
	if err := c.discover(ctx, *development); err != nil {
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
	clientEnv := clientEnvironment(os.Environ(), codexHome)
	binary, err := exec.LookPath("codex")
	if err != nil {
		return errors.New("Codex CLI is required")
	}
	if err := install(filepath.Join(codexHome, "config.toml"), c, *check); err != nil {
		return err
	}
	// Use one explicit cwd for effective configuration, login and verification;
	// caller project layers must not redirect native authentication.
	configCtx, configCancel := context.WithTimeout(ctx, 90*time.Second)
	configured, err := codexmcp.StartServer(configCtx, binary, codexHome, clientEnv, serverName, "oAuth")
	if err != nil {
		configCancel()
		return err
	}
	err = configured.VerifyOAuthConnection(c.URL, c.ClientID)
	configured.Close()
	configCancel()
	if err != nil {
		return err
	}
	if *configure {
		fmt.Fprintln(out, "Configured metricspire-oidc; authenticate with codex mcp login metricspire-oidc --scopes "+cliauth.Quote(strings.Join(c.Scopes, ",")))
		return nil
	}
	if !*check {
		cmd := exec.CommandContext(ctx, binary, "mcp", "login", serverName, "--scopes", strings.Join(c.Scopes, ","))
		cmd.Dir = codexHome
		cmd.Env = clientEnv
		cmd.Stdin = os.Stdin
		cmd.Stdout = out
		cmd.Stderr = os.Stderr
		if cmd.Run() != nil {
			return errors.New("native OAuth login did not complete; configuration retained for retry")
		}
	}
	verifyCtx, cancel := context.WithTimeout(ctx, 90*time.Second)
	defer cancel()
	client, err := codexmcp.StartServer(verifyCtx, binary, codexHome, clientEnv, serverName, "oAuth")
	if err != nil {
		return err
	}
	defer client.Close()
	if err := client.VerifyOAuthConnection(c.URL, c.ClientID); err != nil {
		return err
	}
	if _, err := client.Verify(codexmcp.RequiredTools); err != nil {
		return err
	}
	if _, err := client.Namespaces(); err != nil {
		return err
	}
	fmt.Fprintln(out, "Native OAuth, MCP initialization, core tools and authorized namespace discovery verified. Token refresh remains owned by Codex.")
	return nil
}
func main() {
	if err := run(context.Background(), os.Args[1:], os.Stdout); err != nil {
		fmt.Fprintln(os.Stderr, "MetricSpire:", err)
		os.Exit(1)
	}
}

func clientEnvironment(original []string, codexHome string) []string {
	out := make([]string, 0, len(original)+1)
	for _, entry := range original {
		if !strings.HasPrefix(entry, "CODEX_HOME=") {
			out = append(out, entry)
		}
	}
	return append(out, "CODEX_HOME="+codexHome)
}
