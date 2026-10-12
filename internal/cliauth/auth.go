// Package cliauth delegates user token acquisition and refresh to Databricks CLI.
// It does not implement OAuth, read credential files, or persist tokens.
package cliauth

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/url"
	"os"
	"os/exec"
	"strings"
	"time"
)

const Server = "metricspire-cli"

func Validate(profile, host string) error {
	u, err := url.Parse(host)
	if profile == "" || strings.HasPrefix(profile, "-") || len(profile) > 128 || strings.ContainsAny(profile, "\r\n\x00") || err != nil || u.Scheme != "https" || u.Hostname() == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || u.Path != "" || u.Port() != "" {
		return errors.New("explicit Databricks CLI profile and HTTPS workspace origin required")
	}
	return nil
}

// Environment isolates the explicitly selected U2M profile from credentials and
// host overrides in the invoking shell. Config location remains user-controlled.
func Environment() []string {
	out := []string{}
	for _, value := range os.Environ() {
		key, _, _ := strings.Cut(value, "=")
		if strings.HasPrefix(key, "DATABRICKS_") && key != "DATABRICKS_CONFIG_FILE" {
			continue
		}
		out = append(out, value)
	}
	return out
}

func Headers(profile, host string) (map[string]string, error) {
	if err := Validate(profile, host); err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 25*time.Second)
	defer cancel()
	run := func(args ...string) ([]byte, error) {
		cmd := exec.CommandContext(ctx, "databricks", args...)
		cmd.Env = Environment()
		// Neither stderr nor raw JSON may reach diagnostics.
		data, err := cmd.Output()
		if err != nil {
			return nil, errors.New("Databricks CLI U2M authorization unavailable; check the selected profile; no login attempted")
		}
		return data, nil
	}
	data, err := run("auth", "describe", "--profile", profile, "--output", "json")
	if err != nil {
		return nil, err
	}
	var description struct {
		Details struct {
			Host     string `json:"host"`
			AuthType string `json:"auth_type"`
		} `json:"details"`
	}
	if json.Unmarshal(data, &description) != nil || description.Details.Host != host || description.Details.AuthType != "databricks-cli" {
		return nil, errors.New("Databricks profile must use CLI user OAuth for the expected workspace")
	}
	data, err = run("auth", "token", "--profile", profile)
	if err != nil {
		return nil, err
	}
	var token struct {
		AccessToken string `json:"access_token"`
	}
	if json.Unmarshal(data, &token) != nil || token.AccessToken == "" || strings.ContainsAny(token.AccessToken, "\r\n\x00") {
		return nil, errors.New("Databricks CLI did not return a usable token")
	}
	return map[string]string{"Authorization": "Bearer " + token.AccessToken}, nil
}

func Quote(s string) string { return "'" + strings.ReplaceAll(s, "'", "'\"'\"'") + "'" }
func Command(binary, profile, host string) string {
	return Quote(binary) + " --headers --profile " + Quote(profile) + " --workspace-host " + Quote(host)
}

// DecodeBundle rejects unknown fields and trailing values, including secrets.
func DecodeBundle(data []byte, target any) error {
	d := json.NewDecoder(bytes.NewReader(data))
	d.DisallowUnknownFields()
	if d.Decode(target) != nil {
		return errors.New("invalid public connection bundle")
	}
	var extra any
	if d.Decode(&extra) != io.EOF {
		return errors.New("unexpected connection bundle content")
	}
	return nil
}
