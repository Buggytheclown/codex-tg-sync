package controlapi

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/mideco-tech/codex-tg/internal/model"
	"github.com/mideco-tech/codex-tg/internal/tasks"
)

// BoardBackend keeps HTTP mutations behind the daemon's launch gate.
// It intentionally has no browser Start or approval operation.
type BoardBackend interface {
	ListTasks(context.Context) ([]model.Task, error)
	GetTask(context.Context, string) (*model.Task, error)
	CreateTask(context.Context, model.CreateTaskInput) (*model.Task, error)
	UpdateTask(context.Context, string, model.UpdateTaskInput) (*model.Task, error)
	MoveTask(context.Context, string, model.MoveTaskInput) (*model.Task, error)
	SetTaskPolling(context.Context, string, model.SetTaskPollingInput) (*model.Task, error)
	AddChat(context.Context, string, string) (*model.Task, error)
	ListTaskEvents(context.Context, string) ([]model.TaskEvent, error)
}

type Options struct {
	Board         BoardBackend
	ListenAddress string
	StaticDir     string
	DevOrigin     string
	// EventInterval controls shared projection reconciliation, not source polling.
	EventInterval time.Duration
}

func registerBoard(mux *http.ServeMux, b BoardBackend, opts Options) {
	if b != nil {
		mux.HandleFunc("/v1/tasks", func(w http.ResponseWriter, r *http.Request) {
			switch r.Method {
			case http.MethodGet:
				value, err := b.ListTasks(r.Context())
				if err != nil {
					boardError(w, err)
					return
				}
				if value == nil {
					value = []model.Task{}
				}
				writeJSON(w, 200, value)
			case http.MethodPost:
				var input model.CreateTaskInput
				if !decodeCommand(w, r, &input) {
					return
				}
				value, err := b.CreateTask(r.Context(), input)
				boardResult(w, 201, value, err)
			default:
				writeError(w, 405, "method not allowed")
			}
		})
		mux.HandleFunc("/v1/tasks/add-chat", func(w http.ResponseWriter, r *http.Request) {
			if r.Method != http.MethodPost {
				writeError(w, 405, "method not allowed")
				return
			}
			var input struct {
				ThreadID string `json:"thread_id"`
				Title    string `json:"title"`
			}
			if !decodeCommand(w, r, &input) {
				return
			}
			value, err := b.AddChat(r.Context(), input.ThreadID, input.Title)
			boardResult(w, 200, value, err)
		})
		mux.HandleFunc("/v1/tasks/", func(w http.ResponseWriter, r *http.Request) {
			parts := strings.Split(strings.TrimPrefix(r.URL.Path, "/v1/tasks/"), "/")
			id := parts[0]
			if id == "" || len(parts) > 2 {
				writeError(w, 404, "not found")
				return
			}
			if len(parts) == 1 {
				switch r.Method {
				case http.MethodGet:
					value, err := b.GetTask(r.Context(), id)
					if err == nil && value == nil {
						err = tasks.ErrNotFound
					}
					boardResult(w, 200, value, err)
				case http.MethodPatch:
					var input model.UpdateTaskInput
					if !decodeCommand(w, r, &input) {
						return
					}
					value, err := b.UpdateTask(r.Context(), id, input)
					boardResult(w, 200, value, err)
				default:
					writeError(w, 405, "method not allowed")
				}
				return
			}
			switch parts[1] {
			case "move":
				if r.Method != http.MethodPost {
					writeError(w, 405, "method not allowed")
					return
				}
				var input model.MoveTaskInput
				if !decodeCommand(w, r, &input) {
					return
				}
				value, err := b.MoveTask(r.Context(), id, input)
				boardResult(w, 200, value, err)
			case "polling":
				if r.Method != http.MethodPut {
					writeError(w, 405, "method not allowed")
					return
				}
				var input model.SetTaskPollingInput
				if !decodeCommand(w, r, &input) {
					return
				}
				value, err := b.SetTaskPolling(r.Context(), id, input)
				boardResult(w, 200, value, err)
			case "events":
				if r.Method != http.MethodGet {
					writeError(w, 405, "method not allowed")
					return
				}
				value, err := b.ListTaskEvents(r.Context(), id)
				if err != nil {
					boardError(w, err)
					return
				}
				if value == nil {
					value = []model.TaskEvent{}
				}
				writeJSON(w, 200, value)
			default:
				writeError(w, 404, "not found")
			}
		})
		hub := newBoardHub(b, opts.EventInterval)
		mux.HandleFunc("/v1/task-events", hub.serve)
	}
	// Unknown API routes must never return the SPA document.
	mux.HandleFunc("/v1", func(w http.ResponseWriter, r *http.Request) { writeError(w, 404, "not found") })
	mux.HandleFunc("/v1/", func(w http.ResponseWriter, r *http.Request) { writeError(w, 404, "not found") })
	mux.Handle("/", boardStatic(opts.StaticDir))
}
func boardResult(w http.ResponseWriter, status int, value any, err error) {
	if err != nil {
		boardError(w, err)
		return
	}
	writeJSON(w, status, value)
}
func boardError(w http.ResponseWriter, err error) {
	status := 500
	switch {
	case errors.Is(err, tasks.ErrConflict):
		status = 409
	case errors.Is(err, tasks.ErrInvalid):
		status = 400
	case errors.Is(err, tasks.ErrNotFound):
		status = 404
	}
	writeError(w, status, err.Error())
}
func decodeCommand(w http.ResponseWriter, r *http.Request, target any) bool {
	kind, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil || kind != "application/json" {
		writeError(w, 415, "Content-Type must be application/json")
		return false
	}
	body := http.MaxBytesReader(w, r.Body, 1<<20)
	raw, err := io.ReadAll(body)
	if err != nil {
		writeError(w, 400, "command body exceeds limit or cannot be read")
		return false
	}
	if trimmed := strings.TrimSpace(string(raw)); !strings.HasPrefix(trimmed, "{") {
		writeError(w, 400, "command must be a JSON object")
		return false
	}
	decoder := json.NewDecoder(strings.NewReader(string(raw)))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		writeError(w, 400, "invalid command: "+err.Error())
		return false
	}
	if err := decoder.Decode(new(any)); err != io.EOF {
		writeError(w, 400, "command must contain exactly one JSON object")
		return false
	}
	return true
}

func boardGuard(next http.Handler, opts Options) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		host, port, err := net.SplitHostPort(r.Host)
		if err != nil || !isLoopbackHost(host) || port == "" {
			writeError(w, 403, "loopback Host and port required")
			return
		}
		if opts.ListenAddress != "" && !strings.EqualFold(r.Host, opts.ListenAddress) {
			writeError(w, 403, "Host does not match control API address")
			return
		}
		origin := r.Header.Get("Origin")
		if origin != "" {
			parsed, err := url.Parse(origin)
			exact := err == nil && parsed.Scheme == "http" && parsed.Host == r.Host && parsed.User == nil && parsed.Path == "" && parsed.RawQuery == "" && parsed.Fragment == ""
			dev := validDevOrigin(opts.DevOrigin) && origin == opts.DevOrigin
			if !exact && !dev {
				writeError(w, 403, "Origin does not match control API")
				return
			}
			if dev {
				w.Header().Set("Access-Control-Allow-Origin", origin)
				w.Header().Set("Vary", "Origin")
			}
		}
		if r.Method == http.MethodOptions {
			if origin == "" {
				writeError(w, 403, "Origin required for preflight")
				return
			}
			method := r.Header.Get("Access-Control-Request-Method")
			if method != "GET" && method != "POST" && method != "PUT" && method != "PATCH" && method != "DELETE" {
				writeError(w, 405, "method not allowed")
				return
			}
			if headers := r.Header.Get("Access-Control-Request-Headers"); headers != "" && !strings.EqualFold(headers, "content-type") {
				writeError(w, 403, "unsupported request headers")
				return
			}
			w.Header().Set("Access-Control-Allow-Methods", "GET, POST, PUT, PATCH, DELETE")
			w.Header().Set("Access-Control-Allow-Headers", "Content-Type")
			w.WriteHeader(204)
			return
		}
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Cache-Control", "no-store")
		next.ServeHTTP(w, r)
	})
}
func validDevOrigin(origin string) bool {
	u, err := url.Parse(origin)
	if err != nil || u.Scheme != "http" || u.Path != "" || u.RawQuery != "" || u.Fragment != "" || u.User != nil {
		return false
	}
	host, port, err := net.SplitHostPort(u.Host)
	return err == nil && isLoopbackHost(host) && port != ""
}
func boardStatic(directory string) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			writeError(w, 405, "method not allowed")
			return
		}
		if r.URL.Path == "/v1" {
			writeError(w, 404, "not found")
			return
		}
		if directory == "" {
			writeError(w, 404, "board static directory is not configured")
			return
		}
		index := filepath.Join(directory, "index.html")
		if _, err := os.Stat(index); err != nil {
			writeError(w, 503, "Task board build is missing. Run npm ci and npm run build in ui, then set the explicit static directory to ui/dist.")
			return
		}
		path := filepath.Clean("/" + r.URL.Path)
		target := filepath.Join(directory, filepath.FromSlash(strings.TrimPrefix(path, "/")))
		if info, err := os.Stat(target); err == nil && !info.IsDir() {
			http.ServeFile(w, r, target)
			return
		}
		if strings.HasPrefix(path, "/assets/") || filepath.Ext(path) != "" {
			writeError(w, 404, "asset not found")
			return
		}
		http.ServeFile(w, r, index)
	})
}

// One reconciler is shared by SSE clients. It hashes the existing read model;
// no notification cursor or duplicate database projection is introduced.
type boardHub struct {
	backend  BoardBackend
	interval time.Duration
	mu       sync.Mutex
	clients  map[chan string]struct{}
	cancel   context.CancelFunc
}

func newBoardHub(b BoardBackend, interval time.Duration) *boardHub {
	if interval <= 0 {
		interval = 2 * time.Second
	}
	return &boardHub{backend: b, interval: interval, clients: make(map[chan string]struct{})}
}
func (h *boardHub) subscribe() chan string {
	h.mu.Lock()
	defer h.mu.Unlock()
	ch := make(chan string, 1)
	h.clients[ch] = struct{}{}
	if h.cancel == nil {
		ctx, cancel := context.WithCancel(context.Background())
		h.cancel = cancel
		go h.reconcile(ctx)
	}
	ch <- "initial"
	return ch
}
func (h *boardHub) unsubscribe(ch chan string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	delete(h.clients, ch)
	if len(h.clients) == 0 && h.cancel != nil {
		h.cancel()
		h.cancel = nil
	}
}
func (h *boardHub) reconcile(ctx context.Context) {
	ticker := time.NewTicker(h.interval)
	defer ticker.Stop()
	last := ""
	for {
		callCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
		ts, err := h.backend.ListTasks(callCtx)
		cancel()
		if err == nil {
			data, _ := json.Marshal(ts)
			hash := sha256.Sum256(data)
			revision := hex.EncodeToString(hash[:])
			if revision != last {
				last = revision
				h.mu.Lock()
				if ctx.Err() == nil {
					for ch := range h.clients {
						select {
						case ch <- revision:
						default:
						}
					}
				}
				h.mu.Unlock()
			}
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}
func (h *boardHub) serve(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeError(w, 405, "method not allowed")
		return
	}
	flush, ok := w.(http.Flusher)
	if !ok {
		writeError(w, 500, "streaming unavailable")
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("X-Accel-Buffering", "no")
	ch := h.subscribe()
	defer h.unsubscribe(ch)
	heartbeat := time.NewTicker(15 * time.Second)
	defer heartbeat.Stop()
	for {
		select {
		case <-r.Context().Done():
			return
		case revision := <-ch:
			data, _ := json.Marshal(map[string]string{"revision": revision})
			if _, err := fmt.Fprintf(w, "event: invalidate\ndata: %s\n\n", data); err != nil {
				return
			}
			flush.Flush()
		case <-heartbeat.C:
			if _, err := io.WriteString(w, ": heartbeat\n\n"); err != nil {
				return
			}
			flush.Flush()
		}
	}
}
