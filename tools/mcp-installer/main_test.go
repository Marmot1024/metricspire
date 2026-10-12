package main

import (
	"bytes"
	"crypto/sha256"
	"fmt"
	"github.com/marmot1024/metricspire/internal/cliauth"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/pelletier/go-toml/v2"
)

var testConnection = connection{URL: "https://metrics.example.invalid/api/v1/mcp", WorkspaceHost: "https://workspace.example.invalid", Helper: "installed --headers"}

func localConnection(t *testing.T) connection {
	c := testConnection
	c.HelperPath = filepath.Join(t.TempDir(), "helper")
	c.HelperData = []byte("fixture executable")
	return c
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
	for _, change := range []string{"url", "helper"} {
		c := testConnection
		if change == "url" {
			c.URL = "https://other.example.invalid/api/v1/mcp"
		} else {
			c.Helper = "other"
		}
		if _, _, err := configure(next, c); err == nil {
			t.Fatal("conflict accepted")
		}
	}
	for _, extra := range []string{"enabled = false\n", "bearer_token_env_var = 'TOKEN'\n", "http_headers = {}\n", "oauth = {}\n", "command = 'custom'\n"} {
		dangerous := bytes.Replace(next, []byte("[mcp_servers.metricspire-cli]\n"), []byte("[mcp_servers.metricspire-cli]\n"+extra), 1)
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
	testConnection := localConnection(t)
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
	for _, raw := range []string{"http://workspace.example.invalid", "https://workspace.example.invalid/path", "https://user:pass@workspace.example.invalid"} {
		c := testConnection
		c.WorkspaceHost = raw
		if c.validate() == nil {
			t.Fatal("accepted unsafe workspace")
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

func TestMigrateOAuthPreservesOtherSettings(t *testing.T) {
	original := []byte("# personal\nmodel='example'\n[mcp_servers.metricspire]\nurl='https://metrics.example.invalid/api/v1/mcp'\n[mcp_servers.metricspire.oauth]\nclient_id='public'\n[mcp_servers.other]\ncommand='other'\n")
	if _, _, err := configure(original, testConnection); err == nil {
		t.Fatal("implicit migration")
	}
	c := testConnection
	c.Migrate = true
	next, changed, err := configure(original, c)
	if err != nil || !changed {
		t.Fatalf("migration: %v", err)
	}
	var config map[string]any
	_ = toml.Unmarshal(next, &config)
	servers := config["mcp_servers"].(map[string]any)
	if servers["metricspire"].(map[string]any)["enabled"] != false || config["model"] != "example" {
		t.Fatal("migration lost configuration")
	}
	if again, changed, err := configure(next, c); err != nil || changed || !bytes.Equal(again, next) {
		t.Fatal("migration not idempotent")
	}
	c = localConnection(t)
	c.Migrate = true
	dir := t.TempDir()
	path := filepath.Join(dir, "config.toml")
	_ = os.WriteFile(path, original, 0600)
	if _, err := install(path, filepath.Join(dir, "SKILL.md"), c, false); err != nil {
		t.Fatal(err)
	}
	backups, _ := filepath.Glob(path + ".metricspire-backup-*")
	if len(backups) != 1 {
		t.Fatal("backup missing")
	}
	saved, _ := os.ReadFile(backups[0])
	if !bytes.Equal(saved, original) {
		t.Fatal("backup altered")
	}
}

func TestExplicitManagedUpgrade(t *testing.T) {
	dir := t.TempDir()
	data := []byte("previous managed binary")
	oldPath := filepath.Join(dir, fmt.Sprintf("%x", sha256.Sum256(data)))
	_ = os.WriteFile(oldPath, data, 0700)
	c := testConnection
	c.Profile = "team"
	c.HelperPath = filepath.Join(dir, "new-version")
	c.Helper = cliauth.Command(oldPath, c.Profile, c.WorkspaceHost)
	original, _, err := configure(nil, c)
	if err != nil {
		t.Fatal(err)
	}
	c.Helper = cliauth.Command(c.HelperPath, c.Profile, c.WorkspaceHost)
	if _, _, err := configure(original, c); err == nil {
		t.Fatal("implicit upgrade")
	}
	c.Upgrade = true
	upgraded, changed, err := configure(original, c)
	if err != nil || !changed || bytes.Equal(original, upgraded) {
		t.Fatalf("managed upgrade failed: %v", err)
	}
	c.Profile = "another-user"
	if _, _, err := configure(original, c); err == nil {
		t.Fatal("changed profile accepted")
	}
	c.Profile = "team"
	_ = os.WriteFile(oldPath, []byte("tampered"), 0700)
	if _, _, err := configure(original, c); err == nil {
		t.Fatal("tampered helper accepted")
	}
}
