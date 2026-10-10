package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/pelletier/go-toml/v2"
)

var testConnection = connection{
	URL: "https://metrics.example.invalid/api/v1/mcp", ClientID: "public-test-client",
	CallbackURL: "http://127.0.0.1/callback/test", CallbackPort: 43127,
}

func TestConfigurePreservesAndRejectsConflicts(t *testing.T) {
	original := []byte("# Keep this comment\nmodel = 'example'\n[mcp_servers.other]\nurl = 'https://other.example.invalid/mcp'\n")
	next, created, err := configure(original, testConnection)
	if err != nil || !created || !bytes.HasPrefix(next, original) {
		t.Fatalf("append failed: created=%v err=%v", created, err)
	}
	var parsed map[string]any
	if err := toml.Unmarshal(next, &parsed); err != nil {
		t.Fatal(err)
	}
	if parsed["model"] != "example" {
		t.Fatal("unrelated config changed")
	}
	repeated, created, err := configure(next, testConnection)
	if err != nil || created || !bytes.Equal(repeated, next) {
		t.Fatal("repeat changed matching configuration")
	}
	for name, change := range map[string]string{
		"url": "https://other.example.invalid/api/v1/mcp", "client": "other-client",
		"callback": "http://127.0.0.1/callback/other", "port": "different",
	} {
		t.Run(name, func(t *testing.T) {
			c := testConnection
			switch name {
			case "url":
				c.URL = change
			case "client":
				c.ClientID = change
			case "callback":
				c.CallbackURL = change
			case "port":
				c.CallbackPort++
			}
			if _, _, err := configure(next, c); err == nil {
				t.Fatal("conflict accepted")
			}
		})
	}
	for _, extra := range []string{"enabled = false\n", "bearer_token_env_var = 'TOKEN'\n", "http_headers = {}\n", "http_headers_helper = 'custom'\n", "command = 'custom'\n"} {
		dangerous := bytes.Replace(next, []byte("[mcp_servers.metricspire]\n"), []byte("[mcp_servers.metricspire]\n"+extra), 1)
		if _, _, err := configure(dangerous, testConnection); err == nil {
			t.Fatalf("accepted incompatible option %s", extra)
		}
	}
	for _, invalid := range []string{"invalid TOML", "mcp_servers = 'bad'", "mcp_servers = {}"} {
		if _, _, err := configure([]byte(invalid), testConnection); err == nil {
			t.Fatal("unsafe append accepted")
		}
	}
}

func TestInstallPreflightRepeatAndCheck(t *testing.T) {
	dir := t.TempDir()
	config := filepath.Join(dir, "codex", "config.toml")
	skillFile := filepath.Join(dir, "skills", "metricspire", "SKILL.md")
	if _, err := install(config, skillFile, testConnection, true); err == nil {
		t.Fatal("check accepted missing installation")
	}
	if _, err := os.Stat(filepath.Dir(config)); !os.IsNotExist(err) {
		t.Fatal("check created a directory")
	}
	created, err := install(config, skillFile, testConnection, false)
	if err != nil || !created {
		t.Fatalf("first install: %v", err)
	}
	before, _ := os.Stat(config)
	configData, _ := os.ReadFile(config)
	if before.Mode().Perm() != 0600 {
		t.Fatal("config must be private")
	}
	for _, check := range []bool{false, true} {
		if created, err = install(config, skillFile, testConnection, check); err != nil || created {
			t.Fatalf("repeat: %v", err)
		}
	}
	after, _ := os.Stat(config)
	if !before.ModTime().Equal(after.ModTime()) {
		t.Fatal("repeat rewrote config")
	}
	if err := os.WriteFile(skillFile, []byte("custom skill"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := install(config, skillFile, testConnection, false); err == nil {
		t.Fatal("custom skill overwritten")
	}
	actual, _ := os.ReadFile(config)
	if !bytes.Equal(actual, configData) {
		t.Fatal("failed preflight changed config")
	}
}

func TestConcurrentChangeAndLock(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "config.toml")
	if err := os.WriteFile(file, []byte("new content"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := writeUnchanged(file, []byte("old content"), []byte("replacement")); err == nil {
		t.Fatal("overwrote concurrent change")
	}
	if err := os.WriteFile(file+".metricspire-install.lock", nil, 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := install(file, filepath.Join(dir, "SKILL.md"), testConnection, false); err == nil {
		t.Fatal("ignored installer lock")
	}
	if data, _ := os.ReadFile(file); string(data) != "new content" {
		t.Fatal("content changed")
	}
}

func TestSymlinkAndCustomSkillDoNotModifyConfig(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "config.toml")
	target := filepath.Join(dir, "original.toml")
	_ = os.WriteFile(target, []byte("# original"), 0600)
	if err := os.Symlink(target, file); err != nil {
		t.Fatal(err)
	}
	if _, err := install(file, filepath.Join(dir, "SKILL.md"), testConnection, false); err == nil {
		t.Fatal("followed config symlink")
	}
	_ = os.Remove(file)
	skillFile := filepath.Join(dir, "SKILL.md")
	_ = os.WriteFile(skillFile, []byte("custom"), 0600)
	if _, err := install(file, skillFile, testConnection, false); err == nil {
		t.Fatal("accepted custom skill")
	}
	if _, err := os.Stat(file); !os.IsNotExist(err) {
		t.Fatal("created config before skill preflight")
	}
}

func TestConnectionValidation(t *testing.T) {
	if err := testConnection.validate(); err != nil {
		t.Fatal(err)
	}
	for _, raw := range []string{"http://metrics.example.invalid/mcp", "https://user:password@metrics.example.invalid/mcp", "https://metrics.example.invalid/mcp?token=secret", "https://metrics.example.invalid/mcp#fragment"} {
		c := testConnection
		c.URL = raw
		if c.validate() == nil {
			t.Fatal("accepted unsafe URL")
		}
	}
	for _, raw := range []string{"https://remote.example.invalid/callback", "http://127.0.0.1:12/callback", "http://127.0.0.1/callback?code=x"} {
		c := testConnection
		c.CallbackURL = raw
		if c.validate() == nil {
			t.Fatal("accepted unsafe callback")
		}
	}
	for _, version := range []string{"codex-cli 0.162.0", "codex-cli 1.0.0\n"} {
		if !supportedCodex(version) {
			t.Fatal("supported version rejected")
		}
	}
	for _, version := range []string{"codex-cli 0.161.9", "codex-cli unexpected", "0.162.1"} {
		if supportedCodex(version) {
			t.Fatal("unsupported version accepted")
		}
	}
	// Validation errors must not echo supplied connection values.
	c := testConnection
	c.URL = "https://secret-marker@example.invalid/mcp"
	if err := c.validate(); err == nil || strings.Contains(err.Error(), "secret-marker") {
		t.Fatal("unsafe error reporting")
	}
}
