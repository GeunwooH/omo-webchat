// Package api implements the HTTP API, WebSocket bridge, and static serving.
package api

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io/fs"
	"log/slog"
	"net"
	"net/http"
	"os"
	"path"
	"strings"
	"sync"

	"github.com/DevNewbie1826/omo-webchat/frontend"
	"github.com/DevNewbie1826/omo-webchat/internal/auth"
	"github.com/DevNewbie1826/omo-webchat/internal/config"
	"github.com/DevNewbie1826/omo-webchat/internal/cursorstore"
	"github.com/DevNewbie1826/omo-webchat/internal/omorpc"
	"github.com/DevNewbie1826/omo-webchat/internal/rpcwatch"
	"github.com/DevNewbie1826/omo-webchat/internal/sendqueue"
	"github.com/DevNewbie1826/omo-webchat/internal/session"
)

const maxChatLifecycleGenerationRecords = 1024

type chatLifecycleGenerationRecord struct {
	chatID     string
	generation uint64
}

// Server holds the single v2 stack shared by all HTTP handlers.
type Server struct {
	cfg        *config.Config
	cursors    *cursorstore.Store
	sessions   *auth.SessionStore
	manager    *session.Manager
	rpcWatcher *rpcwatch.Watcher
	queue      *sendqueue.Store
	bridge     http.Handler
	logger     *slog.Logger
	ctx        context.Context

	// Installation updates and engine restarts replace the same binary, so
	// both endpoints single-flight on one mutex and never overlap.
	updateMu           sync.Mutex
	updateInstallation func(context.Context) error
	// restartEngine stops every owned supervisor, retires an engine this
	// server does not own by terminating its whole process group,
	// re-establishes the shared client transport so the reconnect hook spawns
	// a successor engine, and reports the negotiated engine version before
	// and after the swap.
	// finished closes when the sequence ends, even after a caller timeout;
	// nil means the call completed synchronously.
	restartEngine func(ctx context.Context) (before, after string, finished <-chan struct{}, err error)

	chatLifecycleMu             sync.Mutex
	adoptionMu                  sync.Mutex
	activityCheck               sessionActivityCheck
	chatLifecycleGeneration     sync.Map
	chatLifecycleGenerationFIFO []chatLifecycleGenerationRecord
	chatDeleting                map[string]bool
}

// New creates the API server around the required v2 stack.
func New(ctx context.Context, cfg *config.Config, cursors *cursorstore.Store, sessions *auth.SessionStore, manager *session.Manager, bridge http.Handler, logger *slog.Logger) *Server {
	binary := os.Getenv("CHAT_PI_BINARY")
	return &Server{ctx: ctx, cfg: cfg, cursors: cursors, sessions: sessions, manager: manager, bridge: bridge, logger: logger, activityCheck: observeSessionActivity, chatDeleting: make(map[string]bool),
		updateInstallation: func(ctx context.Context) error { return omorpc.UpdateInstallation(ctx, binary) },
	}
}

func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("POST /api/login", s.handleLogin)

	protected := http.NewServeMux()
	protected.HandleFunc("POST /api/logout", s.handleLogout)
	protected.HandleFunc("GET /api/auth/check", s.handleAuthCheck)
	protected.HandleFunc("GET /api/workspaces", s.handleListWorkspaces)
	protected.HandleFunc("POST /api/workspaces", s.handleCreateWorkspace)
	protected.HandleFunc("DELETE /api/workspaces/{wsId}", s.handleDeleteWorkspace)
	protected.HandleFunc("PATCH /api/workspaces/{wsId}", s.handleRenameWorkspace)
	protected.HandleFunc("GET /api/workspaces/{wsId}/sessions", s.handleListWorkspaceSessions)
	protected.HandleFunc("POST /api/workspaces/{wsId}/sessions/adopt", s.handleAdoptWorkspaceSession)
	protected.HandleFunc("POST /api/workspaces/{wsId}/sessions/open", s.handleOpenWorkspaceSession)
	protected.HandleFunc("POST /api/workspaces/{wsId}/chats", s.handleCreateChat)
	protected.HandleFunc("DELETE /api/workspaces/{wsId}/chats/{chatId}", s.handleDeleteChat)
	protected.HandleFunc("PATCH /api/workspaces/{wsId}/chats/{chatId}", s.handleRenameChat)
	protected.HandleFunc("POST /api/workspaces/{wsId}/chats/{chatId}/touch", s.handleTouchChat)
	protected.HandleFunc("GET /api/workspaces/{wsId}/chats/{chatId}/activity", s.handleGetChatActivity)
	protected.HandleFunc("GET /api/workspaces/{wsId}/chats/{chatId}/history", s.handleGetChatHistory)
	protected.HandleFunc("GET /api/workspaces/{wsId}/chats/{chatId}/tasks", s.handleGetChatTasks)
	protected.HandleFunc("GET /api/workspaces/{wsId}/chats/{chatId}/dag-runs", s.handleListChatDagRuns)
	protected.HandleFunc("GET /api/workspaces/{wsId}/chats/{chatId}/dag-runs/{runId}", s.handleGetChatDagRun)
	protected.HandleFunc("GET /api/workspaces/{wsId}/chats/{chatId}/goal", s.handleGetChatGoal)
	protected.HandleFunc("GET /api/workspaces/{wsId}/chats/{chatId}/media", s.handleGetChatMedia)
	protected.HandleFunc("POST /api/workspaces/{wsId}/chats/{chatId}/upload", s.handleUpload)
	protected.HandleFunc("GET /api/providers", s.handleListProviders)
	protected.HandleFunc("GET /api/sessions/live", s.handleListLiveSessions)
	protected.Handle("GET /api/v2/ws", s.bridge)
	protected.HandleFunc("GET /api/fs/browse", s.handleBrowse)
	protected.HandleFunc("GET /api/fs/list", s.handleList)
	protected.HandleFunc("GET /api/fs/download", s.handleDownload)
	protected.HandleFunc("GET /api/fs/read", s.handleReadFile)
	protected.HandleFunc("GET /api/fs/search", s.handleSearch)
	protected.HandleFunc("POST /api/fs/write", s.handleWriteFile)
	protected.HandleFunc("POST /api/fs/mkdir", s.handleMakeDir)
	protected.HandleFunc("GET /api/layout", s.handleGetLayout)
	protected.HandleFunc("PUT /api/layout", s.handleSetLayout)
	protected.HandleFunc("GET /api/system/stats", s.handleSystemStats)
	protected.HandleFunc("POST /api/system/update", s.handleSystemUpdate)
	protected.HandleFunc("POST /api/system/engine/restart", s.handleSystemEngineRestart)

	mux.Handle("/api/", s.sessions.Middleware(protected))
	mux.Handle("/", s.staticHandler())
	return securityHeaders(mux)
}

func (s *Server) staticHandler() http.Handler {
	sub, err := fs.Sub(frontend.Dist, "dist")
	if err != nil {
		s.logger.Error("embedded frontend missing dist/", "err", err)
		return http.NotFoundHandler()
	}
	etags := embeddedETags(sub)
	fileServer := http.FileServer(http.FS(sub))
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		cleaned := path.Clean(strings.TrimPrefix(r.URL.Path, "/"))
		if info, err := fs.Stat(sub, cleaned); err == nil {
			if info.IsDir() && cleaned != "." {
				// Never auto-index embedded build directories (e.g. /assets/).
				notFound(w)
				return
			}
			if strings.HasPrefix(cleaned, "assets/") {
				w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
			} else {
				w.Header().Set("Cache-Control", "no-cache")
			}
			if tag, ok := etags[cleaned]; ok {
				w.Header().Set("ETag", tag)
			}
			fileServer.ServeHTTP(w, r)
			return
		}
		if !isAppRoute(cleaned) {
			notFound(w)
			return
		}
		index, err := fs.ReadFile(sub, "index.html")
		if err != nil {
			http.NotFound(w, r)
			return
		}
		if cleaned == "ko" || strings.HasPrefix(cleaned, "ko/") {
			// The inline script also sets lang before first paint; serving it
			// here gives crawlers and no-JS clients the right document language.
			index = bytes.Replace(index, []byte(`<html lang="en">`), []byte(`<html lang="ko">`), 1)
		}
		sum := sha256.Sum256(index)
		tag := `"` + hex.EncodeToString(sum[:16]) + `"`
		w.Header().Set("ETag", tag)
		w.Header().Set("Cache-Control", "no-cache")
		if r.Header.Get("If-None-Match") == tag {
			w.WriteHeader(http.StatusNotModified)
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = w.Write(index)
	})
}

// isAppRoute reports whether a path that is not an embedded file is a route
// the SPA renders: the root, /login and the locale-prefixed shells
// (/en, /ko/login, ...).
// Anything else, notably file-like misses (robots.txt, stale assets/*.js),
// is a real 404 instead of the index document.
func isAppRoute(cleaned string) bool {
	if cleaned == "." || cleaned == "login" {
		return true
	}
	first, rest, _ := strings.Cut(cleaned, "/")
	if first != "en" && first != "ko" {
		return false
	}
	return path.Ext(rest) == ""
}

func notFound(w http.ResponseWriter) {
	// no-store keeps CDNs from caching a miss under an asset URL.
	w.Header().Set("Cache-Control", "no-store")
	http.Error(w, "404 page not found", http.StatusNotFound)
}

// embeddedETags hashes every embedded file once so repeat visits revalidate
// with 304 (embed.FS carries no modification times).
func embeddedETags(sub fs.FS) map[string]string {
	tags := make(map[string]string)
	_ = fs.WalkDir(sub, ".", func(name string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return nil
		}
		data, err := fs.ReadFile(sub, name)
		if err != nil {
			return nil
		}
		sum := sha256.Sum256(data)
		tags[name] = `"` + hex.EncodeToString(sum[:16]) + `"`
		return nil
	})
	return tags
}

// securityHeaders sets baseline browser hardening headers on every response.
// Behind a TLS-terminating proxy that reports X-Forwarded-Proto, plain-HTTP
// requests are redirected to HTTPS and HTTPS responses carry HSTS. Without
// that header (direct loopback use) nothing changes.
func securityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.Header.Get("X-Forwarded-Proto") {
		case "http":
			if r.Host != "" && (r.Method == http.MethodGet || r.Method == http.MethodHead) {
				http.Redirect(w, r, "https://"+r.Host+r.URL.RequestURI(), http.StatusPermanentRedirect)
				return
			}
		case "https":
			w.Header().Set("Strict-Transport-Security", "max-age=31536000")
		}
		h := w.Header()
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("X-Frame-Options", "DENY")
		h.Set("Content-Security-Policy", "frame-ancestors 'none'; base-uri 'self'; object-src 'none'")
		h.Set("Referrer-Policy", "same-origin")
		h.Set("Permissions-Policy", "camera=(), microphone=(), geolocation=()")
		next.ServeHTTP(w, r)
	})
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if v != nil {
		_ = json.NewEncoder(w).Encode(v)
	}
}
func writeError(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, map[string]string{"error": msg})
}
func decodeJSON(r *http.Request, v any) error {
	r.Body = http.MaxBytesReader(nil, r.Body, 1<<20)
	return json.NewDecoder(r.Body).Decode(v)
}
func (s *Server) writeStoreError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, cursorstore.ErrNotFound):
		writeError(w, http.StatusNotFound, "not found")
	default:
		s.logger.Error("store operation failed", "err", err)
		writeError(w, http.StatusInternalServerError, "internal server error")
	}
}
func clientIP(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}
