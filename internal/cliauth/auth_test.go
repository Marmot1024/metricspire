package cliauth

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func fakeCLI(t *testing.T, body string) {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "databricks"), []byte("#!/bin/sh\n"+body), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir)
}
func TestHeadersUsesNamedU2MAndSanitizesEnvironment(t *testing.T) {
	t.Setenv("DATABRICKS_TOKEN", "must-not-use")
	t.Setenv("DATABRICKS_HOST", "wrong")
	t.Setenv("DATABRICKS_CLIENT_SECRET", "must-not-use")
	fakeCLI(t, `test -z "$DATABRICKS_TOKEN$DATABRICKS_HOST$DATABRICKS_CLIENT_SECRET" || exit 8
 test "$3" = '--profile' && test "$4" = 'team' || exit 9
 if test "$2" = describe; then
 echo '{"details":{"host":"https://workspace.example.invalid","auth_type":"databricks-cli"}}'
 else echo '{"access_token":"fixture-not-a-real-token"}'; fi
 `)
	h, err := Headers("team", "https://workspace.example.invalid")
	if err != nil || h["Authorization"] != "Bearer fixture-not-a-real-token" {
		t.Fatalf("headers: %v", err)
	}
}
func TestWrongWorkspaceOrAuthFailsBeforeToken(t *testing.T) {
	for _, desc := range []string{`{"host":"https://wrong.example.invalid","auth_type":"databricks-cli"}`, `{"host":"https://workspace.example.invalid","auth_type":"oauth-m2m"}`} {
		t.Run(desc, func(t *testing.T) {
			fakeCLI(t, "test \"$2\" = describe || exit 99\necho '{\"details\":"+desc+"}'\n")
			if _, err := Headers("team", "https://workspace.example.invalid"); err == nil {
				t.Fatal("wrong identity accepted")
			}
		})
	}
}
func TestErrorsNeverExposeCLIOutput(t *testing.T) {
	fakeCLI(t, "echo 'secret-marker'\necho 'secret-marker' >&2\nexit 1\n")
	_, err := Headers("team", "https://workspace.example.invalid")
	if err == nil || strings.Contains(err.Error(), "secret-marker") {
		t.Fatal("unsafe error")
	}
}
func TestBadHeaderAndInput(t *testing.T) {
	fakeCLI(t, `if test "$2" = describe; then echo '{"details":{"host":"https://workspace.example.invalid","auth_type":"databricks-cli"}}'; else echo '{"access_token":"bad\r\nheader"}'; fi`)
	if _, err := Headers("team", "https://workspace.example.invalid"); err == nil {
		t.Fatal("invalid header accepted")
	}
	for _, profile := range []string{"", "--host", "bad\nvalue"} {
		if Validate(profile, "https://workspace.example.invalid") == nil {
			t.Fatal("invalid profile")
		}
	}
	for _, data := range []string{`{"url":"x","secret":"bad"}`, `{"url":"x"} {}`, `{"url":"x"} trailing`} {
		var c struct {
			URL string `json:"url"`
		}
		if DecodeBundle([]byte(data), &c) == nil {
			t.Fatal("bad bundle")
		}
	}
}
