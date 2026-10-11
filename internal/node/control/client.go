package control

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
)

// Client talks to a daemon over its local socket. It is safe for concurrent use.
type Client struct {
	http *http.Client
}

// NewClient creates a client without opening a connection.
func NewClient(socketPath string) *Client {
	return &Client{http: &http.Client{Transport: &http.Transport{
		DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			return dialSocket(ctx, socketPath)
		},
		// There is no client Close method; each call owns its connection.
		DisableKeepAlives: true,
	}, CheckRedirect: func(*http.Request, []*http.Request) error {
		return http.ErrUseLastResponse
	}}}
}

// Status returns a snapshot of the daemon.
func (c *Client) Status(ctx context.Context) (Status, error) {
	resp, err := c.request(ctx, http.MethodGet, "/v1/status")
	if err != nil {
		return Status{}, err
	}
	defer resp.Body.Close()
	var status Status
	if err := json.NewDecoder(resp.Body).Decode(&status); err != nil {
		return Status{}, fmt.Errorf("decode daemon status: %w", err)
	}
	return status, nil
}

// Logs returns the MCP's stderr tail. A zero maxBytes uses the server default.
func (c *Client) Logs(ctx context.Context, mcp string, maxBytes int) ([]byte, error) {
	path := "/v1/mcps/" + url.PathEscape(mcp) + "/logs"
	if maxBytes != 0 {
		path += "?max=" + strconv.Itoa(maxBytes)
	}
	resp, err := c.request(ctx, http.MethodGet, path)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	logs, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("read daemon logs: %w", err)
	}
	return logs, nil
}

// Tools returns the MCP's last observed or cached tool list.
func (c *Client) Tools(ctx context.Context, mcp string) ([]Tool, error) {
	resp, err := c.request(ctx, http.MethodGet, "/v1/mcps/"+url.PathEscape(mcp)+"/tools")
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	var tools []Tool
	if err := json.NewDecoder(resp.Body).Decode(&tools); err != nil {
		return nil, fmt.Errorf("decode daemon tools: %w", err)
	}
	return tools, nil
}

// Restart restarts a configured MCP.
func (c *Client) Restart(ctx context.Context, mcp string) error {
	return c.command(ctx, "/v1/mcps/"+url.PathEscape(mcp)+"/restart")
}

// Reload asks the daemon to re-read and apply its config.
func (c *Client) Reload(ctx context.Context) error {
	return c.command(ctx, "/v1/reload")
}

// Shutdown asks the daemon to stop after accepting the request.
func (c *Client) Shutdown(ctx context.Context) error {
	return c.command(ctx, "/v1/shutdown")
}

func (c *Client) command(ctx context.Context, path string) error {
	resp, err := c.request(ctx, http.MethodPost, path)
	if err != nil {
		return err
	}
	return resp.Body.Close()
}

func (c *Client) request(ctx context.Context, method, path string) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, method, "http://daemon"+path, nil)
	if err != nil {
		return nil, fmt.Errorf("create daemon request: %w", err)
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode >= 200 && resp.StatusCode < 300 {
		return resp, nil
	}
	defer resp.Body.Close()
	var body struct {
		Error string `json:"error"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		return nil, fmt.Errorf("daemon returned %s: decode error response: %w", resp.Status, err)
	}
	var sentinel error
	switch {
	case resp.StatusCode == http.StatusNotFound:
		sentinel = ErrUnknownMCP
	case path == "/v1/reload" && resp.StatusCode == http.StatusBadRequest:
		sentinel = ErrInvalidConfig
	}
	return nil, &responseError{message: body.Error, sentinel: sentinel}
}

type responseError struct {
	message  string
	sentinel error
}

func (e *responseError) Error() string { return e.message }
func (e *responseError) Unwrap() error { return e.sentinel }

// Events streams events until ctx ends or the daemon goes away. Slow consumers
// may miss events, as with Backend.Subscribe. The returned channel is closed
// when the stream ends; an error is returned if it cannot be opened.
func (c *Client) Events(ctx context.Context) (<-chan Event, error) {
	resp, err := c.request(ctx, http.MethodGet, "/v1/events")
	if err != nil {
		return nil, err
	}
	if !strings.HasPrefix(resp.Header.Get("Content-Type"), "text/event-stream") {
		_ = resp.Body.Close()
		return nil, errors.New("daemon event response is not text/event-stream")
	}
	events := make(chan Event, 64)
	go func() {
		defer close(events)
		defer resp.Body.Close()
		reader := bufio.NewReader(resp.Body)
		var data strings.Builder
		for {
			line, err := reader.ReadString('\n')
			if err != nil {
				return
			}
			line = strings.TrimSuffix(strings.TrimSuffix(line, "\n"), "\r")
			if line == "" {
				if data.Len() == 0 {
					continue
				}
				var event Event
				if err := json.Unmarshal([]byte(data.String()), &event); err != nil {
					return
				}
				data.Reset()
				select {
				case <-ctx.Done():
					return
				case events <- event:
				default:
					// Keep reading so EOF and shutdown are noticed even when the
					// consumer stops receiving without canceling its context.
				}
			} else if value, ok := strings.CutPrefix(line, "data:"); ok {
				if data.Len() > 0 {
					data.WriteByte('\n')
				}
				data.WriteString(strings.TrimPrefix(value, " "))
			}
		}
	}()
	return events, nil
}
