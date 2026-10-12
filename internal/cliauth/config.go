package cliauth

import (
	"crypto/sha256"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/pelletier/go-toml/v2"
)

// VerifyConfig checks the installed command and its content-addressed executable.
// Callers must also verify the effective Codex transport and actual MCP handshake.
func VerifyConfig(home, endpoint, profile, host string) (string, error) {
	if err := Validate(profile, host); err != nil {
		return "", err
	}
	data, err := os.ReadFile(filepath.Join(home, "config.toml"))
	if err != nil {
		return "", errors.New("cannot read Codex configuration")
	}
	var c struct {
		Servers map[string]map[string]any `toml:"mcp_servers"`
	}
	if toml.Unmarshal(data, &c) != nil {
		return "", errors.New("invalid Codex configuration")
	}
	s := c.Servers[Server]
	if s["url"] != endpoint || s["enabled"] == false {
		return "", errors.New("CLI MCP endpoint not configured")
	}
	for _, k := range []string{"oauth", "command", "bearer_token_env_var", "http_headers", "env_http_headers"} {
		if _, ok := s[k]; ok {
			return "", errors.New("CLI connection must not mix authentication sources")
		}
	}
	if old := c.Servers["metricspire"]; old != nil && old["enabled"] != false {
		return "", errors.New("old native OAuth connection must remain disabled")
	}
	command, ok := s["http_headers_helper"].(string)
	if !ok || !ManagedCommand(command, filepath.Join(home, "metricspire", "bin"), profile, host) {
		return "", errors.New("CLI helper command, profile, workspace or executable does not match installation")
	}
	return command, nil
}

func ManagedCommand(command, dir, profile, host string) bool {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return false
	}
	for _, entry := range entries {
		path := filepath.Join(dir, entry.Name())
		if command != Command(path, profile, host) {
			continue
		}
		info, err := os.Lstat(path)
		if err != nil || !info.Mode().IsRegular() || info.Mode().Perm() != 0700 {
			return false
		}
		data, err := os.ReadFile(path)
		return err == nil && fmt.Sprintf("%x", sha256.Sum256(data)) == entry.Name()
	}
	return false
}
