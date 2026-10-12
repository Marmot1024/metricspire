// release-gate verifies a deployed App using management metadata and the user's
// existing Codex MCP connection. CLI supplies authentication; data calls stay MCP.
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/marmot1024/metricspire/internal/cliauth"
	"github.com/marmot1024/metricspire/internal/codexmcp"
)

type metadata struct {
	Resource string   `json:"resource"`
	Servers  []string `json:"authorization_servers"`
	Scopes   []string `json:"scopes_supported"`
}
type appInfo struct {
	URL        string   `json:"url"`
	Scopes     []string `json:"effective_user_api_scopes"`
	Deployment struct {
		ID     string `json:"deployment_id"`
		Status struct {
			State string `json:"state"`
		} `json:"status"`
	} `json:"active_deployment"`
}

func verifyScopes(a appInfo, m metadata, endpoint, issuer string) error {
	if strings.TrimRight(a.URL, "/")+"/api/v1/mcp" != endpoint || m.Resource != endpoint || !reflect.DeepEqual(m.Servers, []string{issuer}) {
		return errors.New("App URL or OAuth resource/issuer does not match the release target")
	}
	aScopes, mScopes := slices.Clone(a.Scopes), slices.Clone(m.Scopes)
	slices.Sort(aScopes)
	slices.Sort(mScopes)
	if !reflect.DeepEqual(aScopes, mScopes) || !slices.Contains(aScopes, "sql") || len(slices.Compact(slices.Clone(aScopes))) != len(aScopes) {
		return errors.New("App effective scopes and OAuth metadata must agree and include sql")
	}
	if a.Deployment.ID == "" || a.Deployment.Status.State != "SUCCEEDED" {
		return errors.New("App has no successful active deployment")
	}
	return nil
}

func sameResult(actual, expected json.RawMessage) bool {
	decode := func(raw json.RawMessage) (any, error) {
		d := json.NewDecoder(bytes.NewReader(raw))
		d.UseNumber()
		var v any
		err := d.Decode(&v)
		return v, err
	}
	a, ae := decode(actual)
	e, ee := decode(expected)
	return ae == nil && ee == nil && a != nil && reflect.DeepEqual(a, e)
}

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "release gate FAILED:", err)
		os.Exit(1)
	}
}

func run() error {
	app := flag.String("app", "", "Databricks App name")
	profile := flag.String("profile", "", "existing CLI management profile")
	authProfile := flag.String("auth-profile", "", "installed CLI U2M profile for MCP")
	endpoint := flag.String("mcp-url", "", "expected native MCP endpoint")
	issuer := flag.String("issuer", "", "expected OAuth issuer")
	commit := flag.String("commit", "", "full deployed Git commit")
	namespace := flag.String("namespace", "acceptance", "fixture namespace")
	queryFile := flag.String("query", "", "bounded read-only SemanticQuery or AnalysisQuery fixture")
	expectedFile := flag.String("expected", "", "exact TypedResult fixture including schema")
	preflight := flag.Bool("preflight", false, "verify the old deployment before release; do not attest target revision")
	flag.Parse()
	if *authProfile == "" || *app == "" || *profile == "" || *issuer == "" || *queryFile == "" || *expectedFile == "" || !regexp.MustCompile(`^[0-9a-f]{40}$`).MatchString(*commit) {
		return errors.New("explicit App, management/auth profiles, issuer, full commit and query/result fixtures required")
	}
	u, err := url.Parse(*endpoint)
	if err != nil || u.Scheme != "https" || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || u.Path != "/api/v1/mcp" {
		return errors.New("invalid MCP endpoint")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Minute)
	defer cancel()
	management := exec.CommandContext(ctx, "databricks", "apps", "get", *app, "--profile", *profile, "--output", "json")
	data, err := management.Output()
	if err != nil {
		return errors.New("cannot read App using the existing CLI profile; no login attempted")
	}
	var a appInfo
	if json.Unmarshal(data, &a) != nil {
		return errors.New("invalid App metadata")
	}
	metadataURL := u.Scheme + "://" + u.Host + "/.well-known/oauth-protected-resource" + u.Path
	request, _ := http.NewRequestWithContext(ctx, "GET", metadataURL, nil)
	httpClient := &http.Client{Timeout: 20 * time.Second, CheckRedirect: func(_ *http.Request, _ []*http.Request) error { return http.ErrUseLastResponse }}
	response, err := httpClient.Do(request)
	if err != nil {
		return errors.New("OAuth metadata unavailable")
	}
	defer response.Body.Close()
	var m metadata
	if response.StatusCode != 200 || json.NewDecoder(io.LimitReader(response.Body, 64<<10)).Decode(&m) != nil {
		return errors.New("OAuth metadata is not a JSON success response")
	}
	if err := verifyScopes(a, m, *endpoint, *issuer); err != nil {
		return err
	}
	fmt.Println("PASS App scopes, resource and issuer")
	home, err := os.UserHomeDir()
	if err != nil {
		return errors.New("cannot resolve user directory")
	}
	config := exec.CommandContext(ctx, "codex", "mcp", "get", cliauth.Server, "--json")
	config.Dir = home
	data, err = config.Output()
	if err != nil {
		return errors.New("cannot inspect native MCP target")
	}
	var configured struct {
		Enabled   bool `json:"enabled"`
		Transport struct {
			Type       string         `json:"type"`
			URL        string         `json:"url"`
			Bearer     *string        `json:"bearer_token_env_var"`
			Headers    map[string]any `json:"http_headers"`
			EnvHeaders map[string]any `json:"env_http_headers"`
			Helper     any            `json:"http_headers_helper"`
		} `json:"transport"`
	}
	if json.Unmarshal(data, &configured) != nil || !configured.Enabled || configured.Transport.Type != "streamable_http" || configured.Transport.URL != *endpoint || configured.Transport.Bearer != nil || len(configured.Transport.Headers) > 0 || len(configured.Transport.EnvHeaders) > 0 || configured.Transport.Helper == nil {
		return errors.New("native MCP configuration is not the expected CLI helper target")
	}
	codexHome := os.Getenv("CODEX_HOME")
	if codexHome == "" {
		codexHome = filepath.Join(home, ".codex")
	}
	helper, err := cliauth.VerifyConfig(codexHome, *endpoint, *authProfile, strings.TrimSuffix(*issuer, "/oidc"))
	if err != nil {
		return err
	}
	query, err := os.ReadFile(*queryFile)
	if err != nil {
		return errors.New("cannot read query fixture")
	}
	expected, err := os.ReadFile(*expectedFile)
	if err != nil || !json.Valid(expected) {
		return errors.New("cannot read expected result fixture")
	}
	var q struct {
		Kind string `json:"kind"`
	}
	if json.Unmarshal(query, &q) != nil || (q.Kind != "SemanticQuery" && q.Kind != "AnalysisQuery") {
		return errors.New("unsupported acceptance query")
	}
	client, err := codexmcp.StartServer(ctx, "codex", home, os.Environ(), cliauth.Server, "unknown")
	if err != nil {
		return err
	}
	defer client.Close()
	if err := client.VerifyConnection(*endpoint, helper); err != nil {
		return err
	}
	required := slices.Clone(codexmcp.RequiredTools)
	if q.Kind == "AnalysisQuery" {
		required = append(required, "discover_analysis", "plan_analysis", "submit_analysis", "get_analysis", "cancel_analysis")
	}
	status, err := client.Verify(required)
	if err != nil {
		return err
	}
	if !*preflight && status.ServerInfo.Version != "0.1.0-dev+"+*commit {
		return errors.New("MCP serving binary does not match the target commit")
	}
	namespaces, err := client.Namespaces()
	if err != nil {
		return err
	}
	if !slices.Contains(namespaces, *namespace) {
		return errors.New("acceptance namespace not accessible")
	}
	fmt.Println("PASS native MCP initialize, required tools and namespace call")
	if !*preflight {
		fmt.Println("PASS serving revision", *commit)
	}
	suffix := "query"
	if q.Kind == "AnalysisQuery" {
		suffix = "analysis"
	}
	args := map[string]any{"namespace": *namespace, "query": json.RawMessage(query)}
	var plan json.RawMessage
	if err := client.Call("plan_"+suffix, args, &plan); err != nil {
		return err
	}
	type jobView struct {
		ID     string          `json:"id"`
		Status string          `json:"status"`
		Result json.RawMessage `json:"result"`
		Job    *struct {
			ID     string `json:"id"`
			Status string `json:"status"`
		} `json:"job"`
	}
	var job jobView
	// Exactly one submission. A transport timeout is never automatically retried.
	if err := client.Call("submit_"+suffix, args, &job); err != nil {
		return err
	}
	if job.Job != nil {
		job.ID, job.Status = job.Job.ID, job.Job.Status
	}
	if job.ID == "" {
		return errors.New("submitted query did not return a job ID; do not resubmit automatically")
	}
	id := job.ID
	fmt.Println("SUBMITTED", id)
	jobArgs := map[string]any{"job_id": id}
	if suffix == "analysis" {
		jobArgs["namespace"] = *namespace
	}
	for job.Status == "pending" || job.Status == "running" {
		select {
		case <-ctx.Done():
			return errors.New("query verification timed out; inspect the reported job ID")
		case <-time.After(time.Second):
		}
		job = jobView{}
		if err := client.Call("get_"+suffix, jobArgs, &job); err != nil {
			return err
		}
		if job.Job != nil {
			job.ID, job.Status = job.Job.ID, job.Job.Status
		}
	}
	if job.Status != "succeeded" {
		return errors.New("acceptance job did not succeed; inspect the reported job ID")
	}
	if !sameResult(job.Result, expected) {
		return errors.New("acceptance result differs from the expected rows, exact values or schema")
	}
	if *preflight {
		fmt.Println("PREFLIGHT PASSED (target revision not attested)")
	} else {
		fmt.Println("RELEASE GATE PASSED", a.Deployment.ID, *commit, id)
	}
	return nil
}
