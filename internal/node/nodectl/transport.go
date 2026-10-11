package nodectl

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/AldrinSalazar/mcp-mesh/internal/node/upstream"
	"github.com/AldrinSalazar/mcp-mesh/internal/proto"
)

// DetectTransport asks the server at url which HTTP transport it speaks:
// proto.TransportSSE if a GET opens an event stream whose first event is
// "endpoint", otherwise proto.TransportHTTP. It never sends initialize.
func (n *Node) DetectTransport(ctx context.Context, endpoint string, headers map[string]string) (proto.Transport, error) {
	ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return "", fmt.Errorf("check %s: %w", endpoint, err)
	}
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	req.Header.Set("Accept", "text/event-stream")
	client := &http.Client{Transport: upstream.NewHTTPTransport()}
	defer client.CloseIdleConnections()
	resp, err := client.Do(req)
	if err != nil {
		if upstream.Unreachable(err) {
			err = upstream.ErrUnreachable
		}
		return "", fmt.Errorf("connect %s: %w", endpoint, err)
	}
	defer resp.Body.Close()
	// A Streamable HTTP endpoint may reject a GET before initialization.
	typ, _, _ := mime.ParseMediaType(resp.Header.Get("Content-Type"))
	if resp.StatusCode < 200 || resp.StatusCode >= 300 || typ != "text/event-stream" {
		return proto.TransportHTTP, nil
	}
	const limit = 64 << 10
	scanner := bufio.NewScanner(io.LimitReader(resp.Body, limit+1))
	scanner.Buffer(make([]byte, 4096), limit+1)
	event, size, seen := "", 0, false
	for scanner.Scan() {
		line := scanner.Text()
		size += len(line) + 1
		if size > limit {
			return "", fmt.Errorf("check %s: first event exceeds size limit", endpoint)
		}
		if line == "" {
			if !seen {
				continue
			}
			if event == "endpoint" {
				return proto.TransportSSE, nil
			}
			return proto.TransportHTTP, nil
		}
		key, value, _ := strings.Cut(line, ":")
		switch key {
		case "event":
			event = strings.TrimPrefix(value, " ")
			seen = true
		case "data":
			seen = true
		}
	}
	// An HTTP+SSE server sends "endpoint" right away. A Streamable HTTP server
	// may accept the GET and stay silent until it has something to say, so
	// running out of time on an open stream still means HTTP.
	if err := scanner.Err(); err != nil && !errors.Is(err, context.DeadlineExceeded) {
		return "", fmt.Errorf("check %s: %w", endpoint, err)
	}
	return proto.TransportHTTP, nil
}

// TransportFallback is used when detection cannot reach the running app.
func TransportFallback(endpoint string) (proto.Transport, string) {
	parsed, err := url.Parse(endpoint)
	if err == nil && strings.HasSuffix(parsed.Path, "/sse") {
		return proto.TransportSSE, "sse because the path ends in /sse"
	}
	return proto.TransportHTTP, "http (Streamable HTTP)"
}
