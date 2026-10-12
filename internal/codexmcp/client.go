// Package codexmcp checks the configured native Codex MCP connection. It never
// reads tokens or implements an alternative HTTP authentication path.
package codexmcp

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"os/exec"
	"time"
)

var RequiredTools = []string{"list_namespaces", "search_metrics", "explain_query", "plan_query", "submit_query", "get_query", "cancel_query"}

type Status struct {
	Name       string                     `json:"name"`
	AuthStatus string                     `json:"authStatus"`
	ToolsError json.RawMessage            `json:"toolsError"`
	Tools      map[string]json.RawMessage `json:"tools"`
	ServerInfo struct {
		Name    string `json:"name"`
		Version string `json:"version"`
	} `json:"serverInfo"`
}

type message struct {
	ID     int             `json:"id"`
	Result json.RawMessage `json:"result"`
	Error  json.RawMessage `json:"error"`
	Method string          `json:"method"`
}

type Client struct {
	ctx    context.Context
	stop   context.CancelFunc
	cmd    *exec.Cmd
	in     *json.Encoder
	out    chan message
	id     int
	thread string
}

func Start(ctx context.Context, binary, directory string, env []string) (*Client, error) {
	ctx, stop := context.WithCancel(ctx)
	cmd := exec.CommandContext(ctx, binary, "app-server", "--listen", "stdio://")
	cmd.Cancel = func() error { return cmd.Process.Signal(os.Interrupt) }
	cmd.WaitDelay = 5 * time.Second
	cmd.Dir, cmd.Env = directory, env
	stdin, err := cmd.StdinPipe()
	if err != nil {
		stop()
		return nil, errors.New("native MCP input unavailable")
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		stop()
		return nil, errors.New("native MCP output unavailable")
	}
	// Discard subprocess diagnostics: they can contain authentication material.
	cmd.Stderr = io.Discard
	if cmd.Start() != nil {
		stop()
		return nil, errors.New("cannot start Codex app-server")
	}
	c := &Client{ctx: ctx, stop: stop, cmd: cmd, in: json.NewEncoder(stdin), out: make(chan message)}
	go func() {
		defer close(c.out)
		decoder := json.NewDecoder(stdout)
		for {
			var m message
			if decoder.Decode(&m) != nil {
				return
			}
			select {
			case c.out <- m:
			case <-ctx.Done():
				return
			}
		}
	}()
	var result json.RawMessage
	if err := c.rpc("initialize", map[string]any{"clientInfo": map[string]string{"name": "metricspire-verification", "version": "1"}, "capabilities": map[string]bool{"experimentalApi": true}}, &result); err != nil {
		c.Close()
		return nil, err
	}
	if c.in.Encode(map[string]any{"jsonrpc": "2.0", "method": "initialized", "params": map[string]any{}}) != nil {
		c.Close()
		return nil, errors.New("native MCP initialization failed")
	}
	return c, nil
}

func (c *Client) Close() { c.stop(); _ = c.cmd.Wait() }

func (c *Client) rpc(method string, params, result any) error {
	c.id++
	if c.in.Encode(map[string]any{"jsonrpc": "2.0", "id": c.id, "method": method, "params": params}) != nil {
		return errors.New("native MCP request failed")
	}
	for {
		select {
		case <-c.ctx.Done():
			return errors.New("native MCP verification timed out")
		case m, ok := <-c.out:
			if !ok {
				return errors.New("native MCP client closed unexpectedly")
			}
			if m.Method != "" {
				if m.ID != 0 {
					_ = c.in.Encode(map[string]any{"jsonrpc": "2.0", "id": m.ID, "error": map[string]any{"code": -32601, "message": "verification does not approve interactive requests"}})
				}
				continue
			}
			if m.ID != c.id {
				continue
			}
			if len(m.Error) > 0 && string(m.Error) != "null" {
				return errors.New("native MCP request rejected: " + method)
			}
			if json.Unmarshal(m.Result, result) != nil {
				return errors.New("invalid native MCP response: " + method)
			}
			return nil
		}
	}
}

func (c *Client) Verify(required []string) (Status, error) {
	var reply struct {
		Data []Status `json:"data"`
	}
	err := c.rpc("mcpServerStatus/list", map[string]any{"serverName": "metricspire", "detail": "toolsAndAuthOnly", "limit": 10}, &reply)
	if err != nil {
		return Status{}, err
	}
	for _, s := range reply.Data {
		if s.Name != "metricspire" {
			continue
		}
		if s.AuthStatus != "oAuth" || (len(s.ToolsError) > 0 && string(s.ToolsError) != "null") || s.ServerInfo.Name != "metricspire" {
			return s, errors.New("native MCP handshake failed; preserve authorization and check server configuration")
		}
		for _, name := range required {
			if _, ok := s.Tools[name]; !ok {
				return s, errors.New("required MCP tool missing: " + name)
			}
		}
		return s, nil
	}
	return Status{}, errors.New("MetricSpire is absent from native MCP status")
}

func (c *Client) Call(tool string, args any, result any) error {
	if c.thread == "" {
		var reply struct {
			Thread struct {
				ID string `json:"id"`
			} `json:"thread"`
		}
		if err := c.rpc("thread/start", map[string]any{"ephemeral": true, "cwd": c.cmd.Dir, "sandbox": "read-only"}, &reply); err != nil {
			return err
		}
		c.thread = reply.Thread.ID
		if c.thread == "" {
			return errors.New("native MCP verification thread missing")
		}
	}
	var reply struct {
		IsError    bool            `json:"isError"`
		Structured json.RawMessage `json:"structuredContent"`
		Content    []struct {
			Type string `json:"type"`
			Text string `json:"text"`
		} `json:"content"`
	}
	if err := c.rpc("mcpServer/tool/call", map[string]any{"threadId": c.thread, "server": "metricspire", "tool": tool, "arguments": args}, &reply); err != nil {
		return err
	}
	if reply.IsError {
		return errors.New("MCP tool failed: " + tool)
	}
	data := reply.Structured
	if len(data) == 0 || string(data) == "null" {
		for _, content := range reply.Content {
			if content.Type == "text" {
				data = []byte(content.Text)
				break
			}
		}
	}
	if json.Unmarshal(data, result) != nil {
		return errors.New("invalid MCP tool result: " + tool)
	}
	return nil
}

func (c *Client) Namespaces() ([]string, error) {
	var result struct {
		Namespaces []string `json:"namespaces"`
	}
	if err := c.Call("list_namespaces", map[string]any{}, &result); err != nil {
		return nil, err
	}
	if len(result.Namespaces) == 0 {
		return nil, errors.New("MCP connected but no accessible namespaces")
	}
	return result.Namespaces, nil
}
