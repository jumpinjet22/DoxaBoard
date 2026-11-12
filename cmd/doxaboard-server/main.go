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
	_ "time/tzdata" // the container image has no time zone database

	_ "github.com/jumpinjet22/doxaboard/connectors/all" // register content connectors
	"github.com/jumpinjet22/doxaboard/internal/content"
	"github.com/jumpinjet22/doxaboard/internal/live"
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
	relayAPI := flag.String("relay-api", env("DOXABOARD_RELAY_API", "http://127.0.0.1:9997"), "mediamtx control API (localhost only)")
	livePath := flag.String("live-path", env("DOXABOARD_LIVE_PATH", "live"), "relay path OBS publishes to")
	tz := flag.String("timezone", env("DOXABOARD_TIMEZONE", ""), "IANA time zone for the live schedule, e.g. America/Chicago (default: system)")
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
		LiveToken:       os.Getenv("DOXABOARD_LIVE_TOKEN"),
		PhotoDir:        *photoDir,
	}
	if len(cfg.AdminToken) < 16 || len(cfg.EnrollmentToken) < 16 {
		log.Error("DOXABOARD_ADMIN_TOKEN and DOXABOARD_ENROLLMENT_TOKEN must be set (16+ characters each)")
		os.Exit(2)
	}
	if cfg.LiveToken != "" && len(cfg.LiveToken) < 16 {
		log.Error("DOXABOARD_LIVE_TOKEN must be 16+ characters (or unset)")
		os.Exit(2)
	}
	loc := time.Local
	if *tz != "" {
		var err error
		if loc, err = time.LoadLocation(*tz); err != nil {
			log.Error("invalid DOXABOARD_TIMEZONE", "timezone", *tz, "err", err)
			os.Exit(2)
		}
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
	liveCtl := &live.Controller{
		Backend: app.LiveBackend(),
		Relay:   &live.MediaMTX{BaseURL: *relayAPI},
		Path:    *livePath,
		Loc:     loc,
	}
	app.SetLive(liveCtl)
	srv := &http.Server{
		Addr:              *addr,
		Handler:           app.Handler(),
		ReadHeaderTimeout: 10 * time.Second,
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	go app.RunContent(ctx)
	// Keep the relay's outputs in step with what should be live, e.g. after
	// the relay restarts mid-service.
	go liveCtl.Run(ctx, 15*time.Second, logsafe.Printf(func(m string) { log.Warn(m) }))
	go func() {
		<-ctx.Done()
		shutdown, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_ = srv.Shutdown(shutdown)
	}()

	log.Info("doxaboard server listening", "addr", *addr, "db", *dbPath, "photos", *photoDir, "timezone", loc.String())
	if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		log.Error("server", "err", err)
		os.Exit(1)
	}
}
