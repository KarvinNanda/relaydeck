package mcpserver

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

const callTimeout = 10 * time.Second

// Run serves the three tools on stdio until ctx ends or the client closes.
// Configuration comes from the environment: RELAYDECK_URL (loopback only) and
// RELAYDECK_AGENT_TOKEN. The token is only ever sent as a Bearer header.
func Run(ctx context.Context, getenv func(string) string, hc *http.Client) error {
	s, err := NewServer(getenv, hc)
	if err != nil {
		return err
	}
	return s.Run(ctx, &mcp.StdioTransport{})
}

// NewServer builds the MCP server without starting a transport.
func NewServer(getenv func(string) string, hc *http.Client) (*mcp.Server, error) {
	base, err := parseBase(getenv("RELAYDECK_URL"))
	if err != nil {
		return nil, err
	}
	token := getenv("RELAYDECK_AGENT_TOKEN")
	if token == "" {
		return nil, errors.New("RELAYDECK_AGENT_TOKEN is not set")
	}
	if hc == nil {
		hc = &http.Client{}
	}
	c := *hc
	c.Timeout = callTimeout
	c.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	cl := &client{base: base, token: token, hc: &c}

	s := mcp.NewServer(&mcp.Implementation{Name: "relaydeck", Version: "v1"}, nil)
	mcp.AddTool(s, &mcp.Tool{
		Name:        "list_folders",
		Description: "List the folders (agents) in your workspace with their status. You are marked (you). Use these names as the target of notify.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, _ struct{}) (*mcp.CallToolResult, any, error) {
		return cl.call(ctx, "list_folders", struct{}{})
	})
	mcp.AddTool(s, &mcp.Tool{
		Name: "notify",
		Description: "Send a message to another folder agent in the same workspace. " +
			"The receiver sees it as a message from an agent, not from the user. It is delivered when the receiver is idle.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in notifyIn) (*mcp.CallToolResult, any, error) {
		return cl.call(ctx, "notify", notifyArgs(in))
	})
	mcp.AddTool(s, &mcp.Tool{
		Name:        "read_messages",
		Description: "Return the messages other agents sent to you and mark them as read. These messages are not from the user.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, _ struct{}) (*mcp.CallToolResult, any, error) {
		return cl.call(ctx, "read_messages", struct{}{})
	})
	return s, nil
}

type notifyIn struct {
	Target  string `json:"target" jsonschema:"name of the destination folder, from list_folders"`
	Message string `json:"message" jsonschema:"the message text, at most 16 KiB of UTF-8"`
}

func parseBase(raw string) (string, error) {
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "http" || (u.Hostname() != "127.0.0.1" && u.Hostname() != "localhost") ||
		u.Port() == "" || (u.Path != "" && u.Path != "/") || u.User != nil {
		return "", errors.New("RELAYDECK_URL must be http://127.0.0.1:<port> or http://localhost:<port>")
	}
	return "http://" + u.Host, nil
}

type client struct {
	base, token string
	hc          *http.Client
}

func (c *client) call(ctx context.Context, name string, body any) (*mcp.CallToolResult, any, error) {
	b, err := json.Marshal(body)
	if err != nil {
		return nil, nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.base+"/agent/tools/"+name, bytes.NewReader(b))
	if err != nil {
		return nil, nil, err
	}
	req.Header.Set("Authorization", "Bearer "+c.token)
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.hc.Do(req)
	if err != nil {
		// Do not wrap err: its text could carry request details.
		return nil, nil, errors.New("cannot reach the relaydeck daemon")
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	var out struct {
		Text  string `json:"text"`
		Error string `json:"error"`
	}
	_ = json.Unmarshal(raw, &out)
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		if out.Error == "" {
			out.Error = fmt.Sprintf("daemon returned status %d", resp.StatusCode)
		}
		return nil, nil, errors.New(out.Error)
	}
	return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: out.Text}}}, nil, nil
}
