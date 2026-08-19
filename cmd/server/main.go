// Command server runs Mergebase: the JSON API plus the embedded frontend,
// backed by one SQLite file. Configuration is PORT and DATABASE_PATH only —
// the same binary runs identically on a laptop and on any container host.
package main

import (
	"io/fs"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"mergebase/internal/api"
	"mergebase/internal/seed"
	"mergebase/internal/store"
	"mergebase/web"
)

func main() {
	log := slog.New(slog.NewTextHandler(os.Stderr, nil))

	port := envOr("PORT", "8080")
	dbPath := envOr("DATABASE_PATH", "./data/mergebase.db")
	if err := os.MkdirAll(filepath.Dir(dbPath), 0o755); err != nil {
		log.Error("creating data directory", "path", dbPath, "err", err)
		os.Exit(1)
	}

	st, err := store.Open(dbPath)
	if err != nil {
		log.Error("opening store", "path", dbPath, "err", err)
		os.Exit(1)
	}
	defer st.Close()

	if err := seed.Ensure(st); err != nil {
		log.Error("seeding demo workspace", "err", err)
		os.Exit(1)
	}

	mux := http.NewServeMux()
	api.New(st, log).Register(mux)
	mux.Handle("/", spaHandler())

	srv := &http.Server{
		Addr:              ":" + port,
		Handler:           requestLog(log, mux),
		ReadHeaderTimeout: 5 * time.Second,
	}
	log.Info("mergebase listening", "port", port, "db", dbPath)
	if err := srv.ListenAndServe(); err != nil {
		log.Error("server stopped", "err", err)
		os.Exit(1)
	}
}

func envOr(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

// spaHandler serves the embedded frontend; unknown paths fall back to
// index.html so client-side routes survive a full page reload.
func spaHandler() http.Handler {
	dist, err := fs.Sub(web.Dist, "dist")
	if err != nil {
		panic(err) // embed guarantees dist exists at build time
	}
	fileServer := http.FileServer(http.FS(dist))
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path := strings.TrimPrefix(r.URL.Path, "/")
		if path != "" {
			if f, err := dist.Open(path); err == nil {
				f.Close()
				fileServer.ServeHTTP(w, r)
				return
			}
		}
		index, err := fs.ReadFile(dist, "index.html")
		if err != nil {
			http.Error(w, "frontend not built", http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Write(index)
	})
}

func requestLog(log *slog.Logger, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		next.ServeHTTP(w, r)
		if strings.HasPrefix(r.URL.Path, "/api/") || r.URL.Path == "/healthz" {
			log.Info("request", "method", r.Method, "path", r.URL.Path, "dur", time.Since(start).Round(time.Millisecond))
		}
	})
}
