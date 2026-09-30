package wisp

import (
	"embed"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"time"
)

//go:embed web/*
var assets embed.FS

// Handler exposes read-only capability and execution inspection. Mount on a
// localhost server; histories may contain sensitive event and tool data. The
// handler does not grant the agent any event or communication capability.
func (r *Runtime) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/agent", func(w http.ResponseWriter, req *http.Request) { writeJSON(w, r.Describe()) })
	mux.HandleFunc("GET /api/runs", func(w http.ResponseWriter, req *http.Request) {
		runs, err := r.ListRuns(req.Context(), 100)
		if err != nil {
			http.Error(w, "history unavailable", http.StatusInternalServerError)
			return
		}
		writeJSON(w, runs)
	})
	mux.HandleFunc("GET /api/runs/{id}", func(w http.ResponseWriter, req *http.Request) {
		history, err := r.History(req.Context(), req.PathValue("id"))
		if errors.Is(err, ErrNotFound) {
			http.NotFound(w, req)
			return
		}
		if err != nil {
			http.Error(w, "history unavailable", http.StatusInternalServerError)
			return
		}
		writeJSON(w, history)
	})
	mux.HandleFunc("GET /api/stream", r.stream)
	for _, name := range []string{"index.html", "app.js", "style.css"} {
		path := "/" + name
		if name == "index.html" {
			path = "/{$}"
		}
		mux.HandleFunc("GET "+path, func(w http.ResponseWriter, req *http.Request) {
			content, err := assets.ReadFile("web/" + name)
			if err != nil {
				http.Error(w, "asset unavailable", 500)
				return
			}
			mime := map[string]string{"index.html": "text/html; charset=utf-8", "app.js": "text/javascript; charset=utf-8", "style.css": "text/css; charset=utf-8"}
			w.Header().Set("Content-Type", mime[name])
			_, _ = w.Write(content)
		})
	}
	return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Content-Security-Policy", "default-src 'self'; frame-ancestors 'none'; base-uri 'none'")
		mux.ServeHTTP(w, req)
	})
}

func writeJSON(w http.ResponseWriter, value any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(value)
}

// SSE carries durable revision notifications, not an ephemeral copy of history.
// A reconnect always receives the current revision and clients refetch snapshots.
func (r *Runtime) stream(w http.ResponseWriter, req *http.Request) {
	if _, ok := w.(http.Flusher); !ok {
		http.Error(w, "streaming unavailable", 500)
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("X-Accel-Buffering", "no")
	controller := http.NewResponseController(w)
	ticker := time.NewTicker(500 * time.Millisecond)
	defer ticker.Stop()
	var previous int64 = -1
	var ticks int
	for {
		var revision int64
		if err := r.store.db.QueryRowContext(req.Context(), `SELECT COALESCE(MAX(sequence),0) FROM activity`).Scan(&revision); err != nil {
			return
		}
		if revision != previous || ticks%30 == 0 {
			_ = controller.SetWriteDeadline(time.Now().Add(5 * time.Second))
			if _, err := fmt.Fprintf(w, "id: %d\ndata: {\"sequence\":%d}\n\n", revision, revision); err != nil {
				return
			}
			if err := controller.Flush(); err != nil {
				return
			}
			previous = revision
		}
		ticks++
		select {
		case <-req.Context().Done():
			return
		case <-ticker.C:
		}
	}
}
