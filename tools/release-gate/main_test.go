package main

import (
	"encoding/json"
	"testing"
)

func TestScopesFailClosed(t *testing.T) {
	endpoint, issuer := "https://app.example/api/v1/mcp", "https://workspace.example/oidc"
	a := appInfo{URL: "https://app.example", Scopes: []string{"iam.current-user:read", "sql"}}
	a.Deployment.ID = "deployment"
	a.Deployment.Status.State = "SUCCEEDED"
	m := metadata{Resource: endpoint, Servers: []string{issuer}, Scopes: []string{"sql", "iam.current-user:read"}}
	if err := verifyScopes(a, m, endpoint, issuer); err != nil {
		t.Fatal(err)
	}
	for _, mode := range []string{"missing_sql", "disagree", "resource", "issuer", "duplicate", "deployment"} {
		t.Run(mode, func(t *testing.T) {
			aa, mm := a, m
			switch mode {
			case "missing_sql":
				aa.Scopes = []string{"iam.current-user:read"}
				mm.Scopes = aa.Scopes
			case "disagree":
				mm.Scopes = []string{"sql"}
			case "resource":
				mm.Resource = "https://other.example/api/v1/mcp"
			case "issuer":
				mm.Servers = []string{"https://other.example/oidc"}
			case "duplicate":
				mm.Scopes = []string{"sql", "sql", "iam.current-user:read"}
				aa.Scopes = mm.Scopes
			case "deployment":
				aa.Deployment.Status.State = "FAILED"
			}
			if verifyScopes(aa, mm, endpoint, issuer) == nil {
				t.Fatal("invalid release environment accepted")
			}
		})
	}
}

func TestExactResultComparison(t *testing.T) {
	a := json.RawMessage(`{"rows":[[9007199254740993,"12.3400"]],"columns":[{"name":"a","type_name":"LONG"}],"truncated":false}`)
	if !sameResult(a, a) {
		t.Fatal("same result rejected")
	}
	for _, bad := range []string{
		`{"rows":[[9007199254740992,"12.3400"]],"columns":[{"name":"a","type_name":"LONG"}],"truncated":false}`,
		`{"rows":[[9007199254740993,12.34]],"columns":[{"name":"a","type_name":"LONG"}],"truncated":false}`,
		`{"rows":[[9007199254740993,"12.3400"]],"columns":[{"name":"a","type_name":"STRING"}],"truncated":false}`,
		`null`, `{}`, `{"rows":[]}`,
	} {
		if sameResult(a, json.RawMessage(bad)) {
			t.Fatal("different result accepted")
		}
	}
}
