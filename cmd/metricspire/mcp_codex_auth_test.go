package main

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// Opt-in maintainer acceptance, not a user onboarding requirement. Codex owns
// its existing OAuth connection. No login, config mutation, credential read,
// inference turn, static token, or CLI-profile fallback is performed here.
func TestMCPRemoteStagingCodexNative(t *testing.T) {
	if os.Getenv("METRICSPIRE_RUN_MCP_CODEX_ACCEPTANCE") != "staging-read-only" {
		t.Skip("native Codex OAuth acceptance requires explicit staging-read-only opt-in")
	}
	origin := strings.TrimSuffix(os.Getenv("METRICSPIRE_MCP_TEST_URL"), "/")
	parsed, err := url.Parse(origin)
	if err != nil || parsed.Scheme != "https" || parsed.User != nil || parsed.Path != "" || parsed.RawQuery != "" || parsed.Fragment != "" ||
		!strings.HasSuffix(parsed.Hostname(), ".databricksapps.com") || !strings.Contains(parsed.Hostname(), "-staging-") {
		t.Fatal("an explicit HTTPS staging Databricks App origin is required")
	}
	phase := os.Getenv("METRICSPIRE_MCP_NATIVE_PHASE")
	if err := validateNativePhase(phase, os.Getenv("METRICSPIRE_MCP_NATIVE_EXPIRED_AT"), time.Now()); err != nil {
		t.Fatal(err)
	}
	binary := os.Getenv("METRICSPIRE_MCP_TEST_CODEX_BIN")
	if binary == "" {
		binary = "codex"
	}
	server := os.Getenv("METRICSPIRE_MCP_TEST_SERVER")
	if server == "" {
		server = "metricspire"
	}
	cwd, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatal("cannot resolve acceptance working directory")
	}
	for _, attempt := range []string{"fresh-process", "process-restart"} {
		t.Run(attempt, func(t *testing.T) {
			// Keep the transport alive during cleanup so a failed query check
			// can cancel only its own accepted job before shutting down Codex.
			ctx, cancel := context.WithTimeout(context.Background(), 150*time.Second)
			t.Cleanup(cancel)
			rpc := startCodexRPC(t, ctx, exec.CommandContext(ctx, binary, "app-server", "--listen", "stdio://"))
			var initialized json.RawMessage
			mustNativeRPC(t, rpc, "initialize", map[string]any{"clientInfo": map[string]string{"name": "metricspire-native-acceptance", "version": "1"}}, &initialized)
			if err := rpc.send(map[string]any{"method": "initialized", "params": map[string]any{}}); err != nil {
				t.Fatal("cannot notify initialized")
			}
			var thread struct {
				Thread struct {
					ID string `json:"id"`
				} `json:"thread"`
			}
			mustNativeRPC(t, rpc, "thread/start", map[string]any{"cwd": cwd, "ephemeral": true}, &thread)
			if thread.Thread.ID == "" {
				t.Fatal("native client did not create an ephemeral tool context")
			}
			var inventory struct {
				Data []struct {
					Name          string                     `json:"name"`
					HTTPOrigin    string                     `json:"httpOrigin"`
					AuthStatus    string                     `json:"authStatus"`
					RuntimeStatus string                     `json:"runtimeStatus"`
					ToolsError    *string                    `json:"toolsError"`
					Tools         map[string]json.RawMessage `json:"tools"`
				} `json:"data"`
			}
			// Status and tool calls use the same thread connection. A separate
			// diagnostic process/runtime is not substituted for that evidence.
			mustNativeRPC(t, rpc, "mcpServerStatus/list", map[string]any{"serverName": server, "threadId": thread.Thread.ID, "detail": "toolsAndAuthOnly", "limit": 1}, &inventory)
			if len(inventory.Data) != 1 || inventory.Data[0].Name != server {
				t.Fatal("current native context does not expose the requested MCP server")
			}
			status := inventory.Data[0]
			if status.HTTPOrigin != origin {
				t.Fatal("native connection origin does not match the reviewed staging origin (or client cannot report its origin)")
			}
			if status.AuthStatus != "oAuth" || status.ToolsError != nil {
				t.Fatalf("native discovery not accepted: auth=%s runtime=%s tool_count=%d; no scope change or alternative credential was attempted", status.AuthStatus, status.RuntimeStatus, len(status.Tools))
			}
			names := make([]string, 0, len(status.Tools))
			for name := range status.Tools {
				names = append(names, name)
			}
			slices.Sort(names)
			if !reflect.DeepEqual(names, []string{"cancel_query", "explain_query", "get_query", "list_namespaces", "plan_query", "search_metrics", "submit_query"}) {
				t.Fatal("native connection does not expose the seven-tool contract")
			}
			caller := &nativeToolCaller{rpc: rpc, threadID: thread.Thread.ID, server: server}
			verifyMCPStagingAcceptance(t, caller, "Codex native OAuth / "+phase+" / "+attempt)
		})
		if t.Failed() {
			break // One failure ends the run; never enter a re-login/retry loop.
		}
	}
}

func validateNativePhase(phase, expiredAt string, now time.Time) error {
	switch phase {
	case "first-login", "new-session", "post-expiry":
	default:
		return errors.New("native phase must be first-login, new-session, or post-expiry")
	}
	if phase == "post-expiry" {
		expiry, err := time.Parse(time.RFC3339, expiredAt)
		if err != nil || !now.After(expiry) {
			return errors.New("post-expiry acceptance requires an operator-verified access-token expiry in the past; do not assume a TTL or re-login between phases")
		}
	}
	return nil
}

type nativeToolCaller struct {
	rpc              *codexRPC
	threadID, server string
}

func (caller *nativeToolCaller) CallTool(ctx context.Context, input *mcp.CallToolParams) (*mcp.CallToolResult, error) {
	var result mcp.CallToolResult
	err := caller.rpc.call(ctx, "mcpServer/tool/call", map[string]any{
		"threadId": caller.threadID, "server": caller.server, "tool": input.Name, "arguments": input.Arguments,
	}, &result)
	return &result, err
}

type codexRPC struct {
	input    io.Writer
	messages <-chan json.RawMessage
	exitCode <-chan int
	ctx      context.Context
	nextID   int
}

func startCodexRPC(t *testing.T, ctx context.Context, command *exec.Cmd) *codexRPC {
	t.Helper()
	input, err := command.StdinPipe()
	if err != nil {
		t.Fatal("cannot open native client input")
	}
	// Own the pipe: Cmd.Wait closes StdoutPipe before a concurrent reader
	// necessarily drains the final reply. That can turn a successful response
	// into a spurious client-output-closed diagnosis when the child exits.
	output, childOutput, err := os.Pipe()
	if err != nil {
		_ = input.Close()
		t.Fatal("cannot open native client output")
	}
	command.Stdout = childOutput
	command.Stderr = io.Discard // No raw OAuth errors, URLs or credentials in logs.
	if err := command.Start(); err != nil {
		_ = input.Close()
		_ = output.Close()
		_ = childOutput.Close()
		t.Fatal("cannot start configured Codex binary")
	}
	_ = childOutput.Close() // Only the child owns the writing end after Start.
	readCtx, cancelReading := context.WithCancel(ctx)
	done := make(chan struct{})
	exitCode := make(chan int, 1)
	messages := make(chan json.RawMessage)
	go func() {
		defer close(messages)
		defer output.Close()
		scanner := bufio.NewScanner(output)
		scanner.Buffer(make([]byte, 4096), 16<<20)
		for scanner.Scan() {
			message := append(json.RawMessage(nil), scanner.Bytes()...)
			select {
			case messages <- message:
			case <-readCtx.Done():
				return
			}
		}
	}()
	go func() {
		_ = command.Wait()
		exitCode <- command.ProcessState.ExitCode()
		close(done)
	}()
	t.Cleanup(func() {
		defer cancelReading()
		defer output.Close()
		_ = input.Close()
		select {
		case <-done:
		case <-time.After(2 * time.Second):
			_ = command.Process.Kill()
			<-done
		}
	})
	return &codexRPC{input: input, messages: messages, exitCode: exitCode, ctx: ctx}
}

func (rpc *codexRPC) send(message any) error {
	return json.NewEncoder(rpc.input).Encode(message)
}

// Sequential requests only. Notifications are ignored, and interactive server
// requests are rejected rather than opening authorization or running a model.
func (rpc *codexRPC) call(ctx context.Context, method string, params, out any) error {
	if ctx.Err() != nil || rpc.ctx.Err() != nil {
		return errors.New("native request cancelled before submission")
	}
	rpc.nextID++
	id := rpc.nextID
	if err := rpc.send(map[string]any{"id": id, "method": method, "params": params}); err != nil {
		return errors.New("native client input closed")
	}
	for {
		select {
		case <-ctx.Done():
			return errors.New("native request deadline exceeded")
		case <-rpc.ctx.Done():
			return errors.New("native process deadline exceeded")
		case raw, ok := <-rpc.messages:
			if !ok {
				select {
				case code := <-rpc.exitCode:
					return fmt.Errorf("native client exited before reply (exit_code=%d); startup cause not established", code)
				default:
				}
				return errors.New("native client output closed or exceeded the frame limit")
			}
			var message struct {
				ID     json.RawMessage `json:"id"`
				Method string          `json:"method"`
				Result json.RawMessage `json:"result"`
				Error  *struct {
					Code int `json:"code"`
				} `json:"error"`
			}
			if json.Unmarshal(raw, &message) != nil {
				return errors.New("invalid native protocol response")
			}
			if message.Method != "" {
				if len(message.ID) != 0 {
					if err := rpc.send(map[string]any{"id": message.ID, "error": map[string]any{"code": -32601, "message": "interactive requests are not authorized by this acceptance runner"}}); err != nil {
						return errors.New("cannot reject interactive native request")
					}
				}
				continue
			}
			if string(message.ID) != fmt.Sprint(id) {
				continue
			}
			if message.Error != nil {
				return fmt.Errorf("native RPC rejected %s (code=%d); upstream detail withheld", method, message.Error.Code)
			}
			if len(message.Result) == 0 || json.Unmarshal(message.Result, out) != nil {
				return errors.New("invalid native result")
			}
			return nil
		}
	}
}

func mustNativeRPC(t *testing.T, rpc *codexRPC, method string, params, out any) {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 40*time.Second)
	defer cancel()
	if err := rpc.call(ctx, method, params, out); err != nil {
		t.Fatalf("%s: %v", method, err)
	}
}
