// Command doxaboard-server runs the DoxaBoard control plane and admin console.
package main

import (
	"context"
	"errors"
	"flag"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	_ "github.com/jumpinjet22/doxaboard/connectors/all" // register content connectors
	"github.com/jumpinjet22/doxaboard/internal/content"
	"github.com/jumpinjet22/doxaboard/internal/logsafe"
	"github.com/jumpinjet22/doxaboard/internal/server"
)

func env(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

func main() {
	addr := flag.String("addr", env("DOXABOARD_ADDR", ":8080"), "listen address")
	dbPath := flag.String("db", env("DOXABOARD_DB", "doxaboard.db"), "SQLite database path")
	photoDir := flag.String("photos", env("DOXABOARD_PHOTO_DIR", ""), "photo directory (one subfolder per album)")
	dataDir := flag.String("data", env("DOXABOARD_DATA_DIR", ""), "directory for the content cache and secret key (default: the database's directory)")
	keyFile := flag.String("secret-key", env("DOXABOARD_SECRET_KEY_FILE", ""), "file holding the key that encrypts connector secrets (created if missing; default: <data>/secret.key)")
	debug := flag.Bool("debug", os.Getenv("DOXABOARD_DEBUG") != "", "verbose logging")
	flag.Parse()

	level := slog.LevelInfo
	if *debug {
		level = slog.LevelDebug
	}
	log := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: level}))

	cfg := server.Config{
		AdminToken:      os.Getenv("DOXABOARD_ADMIN_TOKEN"),
		EnrollmentToken: os.Getenv("DOXABOARD_ENROLLMENT_TOKEN"),
		PhotoDir:        *photoDir,
	}
	if len(cfg.AdminToken) < 16 || len(cfg.EnrollmentToken) < 16 {
		log.Error("DOXABOARD_ADMIN_TOKEN and DOXABOARD_ENROLLMENT_TOKEN must be set (16+ characters each)")
		os.Exit(2)
	}

	store, err := server.OpenStore(*dbPath)
	if err != nil {
		log.Error("open database", "err", err)
		os.Exit(1)
	}
	defer store.Close()

	if *dataDir == "" {
		*dataDir = filepath.Dir(*dbPath)
	}
	if *keyFile == "" {
		*keyFile = filepath.Join(*dataDir, "secret.key")
	}
	key, err := content.LoadOrCreateKey(*keyFile)
	if err != nil {
		log.Error("loading secret key", "path", *keyFile, "err", err)
		os.Exit(1)
	}
	cfg.Sealer, err = content.NewSealer(key)
	if err != nil {
		log.Error("secret key", "err", err)
		os.Exit(1)
	}
	cfg.Content = &content.Manager{
		Dir:   filepath.Join(*dataDir, "content"),
		Style: content.DefaultTextStyle,
		// Content logs carry text from remote feeds: clean it.
		Logf: logsafe.Printf(func(m string) { log.Info(m) }),
	}
	if *photoDir != "" {
		cfg.Content.Files = os.DirFS(*photoDir)
	}

	app := server.New(cfg, store, log)
	srv := &http.Server{
		Addr:              *addr,
		Handler:           app.Handler(),
		ReadHeaderTimeout: 10 * time.Second,
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	go app.RunContent(ctx)
	go func() {
		<-ctx.Done()
		shutdown, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_ = srv.Shutdown(shutdown)
	}()

	log.Info("doxaboard server listening", "addr", *addr, "db", *dbPath, "photos", *photoDir)
	if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		log.Error("server", "err", err)
		os.Exit(1)
	}
}
