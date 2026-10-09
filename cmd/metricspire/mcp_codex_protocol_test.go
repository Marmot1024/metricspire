package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// A real subprocess exits immediately after sending a large final reply. The
// transport must drain it even when Wait has already reaped the child.
func TestNativeRPCDrainsReplyBeforeChildExit(t *testing.T) {
	for i := range 3 {
		t.Run(fmt.Sprint(i), func(t *testing.T) {
			ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
			t.Cleanup(cancel)
			command := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestNativeRPCExitHelper$")
			command.Env = append(os.Environ(), "METRICSPIRE_TEST_NATIVE_EXIT_HELPER=1")
			rpc := startCodexRPC(t, ctx, command)
			var result struct {
				Value string `json:"value"`
			}
			if err := rpc.call(ctx, "initialize", map[string]any{}, &result); err != nil {
				t.Fatal(err)
			}
			if result.Value != strings.Repeat("final-reply", 1<<16) {
				t.Fatal("child exit truncated the final native reply")
			}
		})
	}
}

func TestNativeRPCExitHelper(t *testing.T) {
	if os.Getenv("METRICSPIRE_TEST_NATIVE_EXIT_HELPER") != "1" {
		return
	}
	var request struct {
		ID json.RawMessage `json:"id"`
	}
	if json.NewDecoder(os.Stdin).Decode(&request) != nil {
		os.Exit(2)
	}
	if json.NewEncoder(os.Stdout).Encode(map[string]any{"id": request.ID, "result": map[string]string{"value": strings.Repeat("final-reply", 1<<16)}}) != nil {
		os.Exit(3)
	}
	os.Exit(0)
}

func TestNativeAcceptanceRefusesUnverifiedExpiry(t *testing.T) {
	now := time.Date(2026, 10, 9, 8, 0, 0, 0, time.UTC)
	for _, test := range []struct {
		phase, expiry string
		valid         bool
	}{
		{"first-login", "", true}, {"new-session", "", true},
		{"post-expiry", "", false}, {"post-expiry", "guessed-60-minutes", false},
		{"post-expiry", "2026-10-09T09:00:00Z", false},
		{"post-expiry", "2026-10-09T08:00:00Z", false},
		{"post-expiry", "2026-10-09T07:59:59Z", true},
		{"connected", "", false},
	} {
		if got := validateNativePhase(test.phase, test.expiry, now) == nil; got != test.valid {
			t.Fatalf("phase=%s expiry=%s accepted=%v, want %v", test.phase, test.expiry, got, test.valid)
		}
	}
}

func TestNativeRPCRejectsInteractiveAuthorizationAndPreservesResults(t *testing.T) {
	messages := make(chan json.RawMessage, 3)
	messages <- json.RawMessage(`{"method":"mcpServer/startupStatus/updated","params":{"status":"connected"}}`)
	messages <- json.RawMessage(`{"id":"approval-1","method":"mcpServer/elicitation/request","params":{"url":"https://identity.example.com/?secret=must-not-escape"}}`)
	messages <- json.RawMessage(`{"id":1,"result":{"content":[{"type":"text","text":"{\"amount\":\"228570.520000000000\",\"count\":9007199254740993}"}],"isError":false}}`)
	var written bytes.Buffer
	rpc := &codexRPC{input: &written, messages: messages, ctx: t.Context()}
	caller := &nativeToolCaller{rpc: rpc, threadID: "ephemeral-test", server: "metricspire"}
	result, err := caller.CallTool(t.Context(), &mcp.CallToolParams{Name: "get_query", Arguments: map[string]any{"job_id": "job_owned"}})
	if err != nil || result.IsError || len(result.Content) != 1 {
		t.Fatalf("native result failed: %v", err)
	}
	text, ok := result.Content[0].(*mcp.TextContent)
	if !ok || !strings.Contains(text.Text, "9007199254740993") || !strings.Contains(text.Text, "228570.520000000000") {
		t.Fatal("native transport lost exact result values")
	}
	decoder := json.NewDecoder(&written)
	var request, rejection map[string]any
	if decoder.Decode(&request) != nil || decoder.Decode(&rejection) != nil {
		t.Fatal("expected one tool request and one explicit interactive rejection")
	}
	if request["method"] != "mcpServer/tool/call" || rejection["id"] != "approval-1" || rejection["error"] == nil || rejection["result"] != nil {
		t.Fatal("interactive authorization was not rejected")
	}
}

func TestNativeRPCDoesNotExposeUpstreamErrorOrRetry(t *testing.T) {
	messages := make(chan json.RawMessage, 1)
	messages <- json.RawMessage(`{"id":1,"error":{"code":-32000,"message":"Bearer secret-must-not-escape","data":{"token":"secret-must-not-escape"}}}`)
	var written bytes.Buffer
	rpc := &codexRPC{input: &written, messages: messages, ctx: t.Context()}
	var result any
	err := rpc.call(t.Context(), "mcpServer/tool/call", map[string]any{}, &result)
	if err == nil || strings.Contains(err.Error(), "secret-must-not-escape") || !strings.Contains(err.Error(), "code=-32000") {
		t.Fatalf("unsafe native error: %v", err)
	}
	if bytes.Count(written.Bytes(), []byte("\n")) != 1 {
		t.Fatal("uncertain native call was retried")
	}
}

func TestNativeRPCDeadlineStopsWithoutRetry(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	var written bytes.Buffer
	rpc := &codexRPC{input: &written, messages: make(chan json.RawMessage), ctx: t.Context()}
	var result any
	if err := rpc.call(ctx, "mcpServer/tool/call", map[string]any{}, &result); err == nil || written.Len() != 0 {
		t.Fatal("cancelled native request was submitted")
	}
}
