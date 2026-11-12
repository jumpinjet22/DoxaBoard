// Command doxacast-server runs the DoxaCast control plane and admin console.
package main

import (
	"context"
	"errors"
	"flag"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/jumpinjet22/doxacast/internal/server"
)

func env(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

func main() {
	addr := flag.String("addr", env("DOXACAST_ADDR", ":8080"), "listen address")
	dbPath := flag.String("db", env("DOXACAST_DB", "doxacast.db"), "SQLite database path")
	photoDir := flag.String("photos", env("DOXACAST_PHOTO_DIR", ""), "photo directory (one subfolder per album)")
	debug := flag.Bool("debug", os.Getenv("DOXACAST_DEBUG") != "", "verbose logging")
	flag.Parse()

	level := slog.LevelInfo
	if *debug {
		level = slog.LevelDebug
	}
	log := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: level}))

	cfg := server.Config{
		AdminToken:      os.Getenv("DOXACAST_ADMIN_TOKEN"),
		EnrollmentToken: os.Getenv("DOXACAST_ENROLLMENT_TOKEN"),
		PhotoDir:        *photoDir,
	}
	if len(cfg.AdminToken) < 16 || len(cfg.EnrollmentToken) < 16 {
		log.Error("DOXACAST_ADMIN_TOKEN and DOXACAST_ENROLLMENT_TOKEN must be set (16+ characters each)")
		os.Exit(2)
	}

	store, err := server.OpenStore(*dbPath)
	if err != nil {
		log.Error("open database", "err", err)
		os.Exit(1)
	}
	defer store.Close()

	srv := &http.Server{
		Addr:              *addr,
		Handler:           server.New(cfg, store, log).Handler(),
		ReadHeaderTimeout: 10 * time.Second,
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	go func() {
		<-ctx.Done()
		shutdown, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_ = srv.Shutdown(shutdown)
	}()

	log.Info("doxacast server listening", "addr", *addr, "db", *dbPath, "photos", *photoDir)
	if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		log.Error("server", "err", err)
		os.Exit(1)
	}
}
