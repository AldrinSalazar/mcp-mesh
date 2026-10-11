package control

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"strconv"
	"strings"
	"time"
)

const defaultLogBytes = 64 * 1024
const pingInterval = 15 * time.Second

// Handler serves the daemon's control API.
func Handler(b Backend) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /v1/status", func(w http.ResponseWriter, r *http.Request) {
		data, err := json.Marshal(b.Status())
		if err != nil {
			writeBackendError(w, fmt.Errorf("encode daemon status: %w", err), false)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(append(data, '\n'))
	})
	mux.HandleFunc("GET /v1/mcps/{name}/logs", func(w http.ResponseWriter, r *http.Request) {
		maxBytes := defaultLogBytes
		if raw, ok := r.URL.Query()["max"]; ok {
			var err error
			maxBytes, err = strconv.Atoi(raw[0])
			if err != nil || maxBytes < 0 {
				writeError(w, http.StatusBadRequest, errors.New("max must be a non-negative integer"))
				return
			}
		}
		logs, err := b.Logs(r.PathValue("name"), maxBytes)
		if err != nil {
			writeBackendError(w, err, false)
			return
		}
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		_, _ = w.Write(logs)
	})
	mux.HandleFunc("POST /v1/mcps/{name}/restart", func(w http.ResponseWriter, r *http.Request) {
		if err := b.Restart(r.Context(), r.PathValue("name")); err != nil {
			writeBackendError(w, err, false)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	})
	mux.HandleFunc("GET /v1/mcps/{name}/tools", func(w http.ResponseWriter, r *http.Request) {
		tools, err := b.Tools(r.PathValue("name"))
		if err != nil {
			writeBackendError(w, err, false)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(tools)
	})
	mux.HandleFunc("POST /v1/reload", func(w http.ResponseWriter, r *http.Request) {
		if err := b.Reload(r.Context()); err != nil {
			writeBackendError(w, err, true)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	})
	mux.HandleFunc("GET /v1/events", func(w http.ResponseWriter, r *http.Request) {
		serveEvents(w, r, b)
	})
	mux.HandleFunc("POST /v1/shutdown", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Length", "0")
		w.WriteHeader(http.StatusAccepted)
		// Commit the response before the backend can stop the HTTP server.
		_ = http.NewResponseController(w).Flush()
		b.Shutdown()
	})
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, pattern := mux.Handler(r)
		if pattern != "" {
			mux.ServeHTTP(w, r)
			return
		}
		// ServeMux's default 404/405 bodies are plain text; keep all API errors
		// in the same JSON format while retaining method checks and Allow.
		var allowed []string
		for _, method := range []string{http.MethodGet, http.MethodHead, http.MethodPost} {
			probe := *r
			probe.Method = method
			if _, pattern := mux.Handler(&probe); pattern != "" {
				allowed = append(allowed, method)
			}
		}
		if len(allowed) > 0 {
			w.Header().Set("Allow", strings.Join(allowed, ", "))
			writeError(w, http.StatusMethodNotAllowed, errors.New("method not allowed"))
			return
		}
		writeError(w, http.StatusNotFound, errors.New("endpoint not found"))
	})
}

func writeBackendError(w http.ResponseWriter, err error, reload bool) {
	status := http.StatusInternalServerError
	switch {
	case errors.Is(err, ErrUnknownMCP):
		status = http.StatusNotFound
	case reload && errors.Is(err, ErrInvalidConfig):
		status = http.StatusBadRequest
	}
	writeError(w, status, err)
}

func writeError(w http.ResponseWriter, status int, err error) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(struct {
		Error string `json:"error"`
	}{Error: err.Error()})
}

func serveEvents(w http.ResponseWriter, r *http.Request, b Backend) {
	if r.Method == http.MethodHead {
		w.Header().Set("Content-Type", "text/event-stream")
		return
	}
	ctx, cancel := context.WithCancel(r.Context())
	defer cancel()
	events := b.Subscribe(ctx)
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	rc := http.NewResponseController(w)
	// Flush headers even before the first event so opening a stream never
	// depends on the daemon changing state.
	if err := rc.Flush(); err != nil {
		return
	}
	ticker := time.NewTicker(pingInterval)
	defer ticker.Stop()
	for {
		var data []byte
		select {
		case <-ctx.Done():
			return
		case event, ok := <-events:
			if !ok {
				return
			}
			encoded, err := json.Marshal(event)
			if err != nil {
				return
			}
			data = append(append([]byte("data: "), encoded...), '\n', '\n')
		case <-ticker.C:
			data = []byte(": ping\n\n")
		}
		// Bound writes as well as sending pings, so a client that stops reading
		// cannot keep a subscription alive indefinitely.
		if err := rc.SetWriteDeadline(time.Now().Add(pingInterval)); err != nil && !errors.Is(err, http.ErrNotSupported) {
			return
		}
		if _, err := w.Write(data); err != nil {
			return
		}
		if err := rc.Flush(); err != nil {
			return
		}
	}
}

// Serve serves the API until ctx ends, then closes connections and removes
// the socket. Cancellation of ctx is a normal shutdown and returns nil.
func Serve(ctx context.Context, l net.Listener, b Backend) (err error) {
	cleanup := socketCleanup(l)
	defer func() {
		_ = l.Close()
		err = errors.Join(err, cleanup())
	}()
	serverCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	server := &http.Server{
		Handler:           Handler(b),
		ReadHeaderTimeout: 5 * time.Second,
		BaseContext:       func(net.Listener) context.Context { return serverCtx },
	}
	shutdown := make(chan error, 1)
	go func() {
		<-serverCtx.Done()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		shutdownErr := server.Shutdown(shutdownCtx)
		if shutdownErr != nil {
			_ = server.Close()
		}
		shutdown <- shutdownErr
	}()
	serveErr := server.Serve(l)
	cancel()
	shutdownErr := <-shutdown
	if serveErr != nil && !errors.Is(serveErr, http.ErrServerClosed) {
		err = fmt.Errorf("serve control API: %w", serveErr)
	}
	if shutdownErr != nil {
		err = errors.Join(err, fmt.Errorf("shut down control API: %w", shutdownErr))
	}
	return err
}
