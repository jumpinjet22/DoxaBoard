// Command doxaboard-agent runs on each screen (Raspberry Pi or any Linux box).
// It enrolls with the server once, then keeps the screen showing the best
// available source, even when the server is unreachable.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io/fs"
	"log"
	"math/rand/v2"
	"net/http"
	"os"
	"os/signal"
	"runtime"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/jumpinjet22/doxaboard/core"
	"github.com/jumpinjet22/doxaboard/internal/agent"
	"github.com/jumpinjet22/doxaboard/internal/logsafe"
)

// version is set at build time with -ldflags "-X main.version=...".
var version = "dev"

func main() {
	cfgPath := flag.String("config", "/etc/doxaboard/agent.json", "config file")
	showVersion := flag.Bool("version", false, "print version and exit")
	flag.Parse()
	if *showVersion {
		fmt.Println(version)
		return
	}

	logger := log.New(os.Stderr, "", log.LstdFlags)
	cfg, err := agent.LoadConfig(*cfgPath)
	if err != nil {
		logger.Fatalf("config: %v", err)
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	creds, err := enroll(ctx, cfg, logger)
	if err != nil {
		logger.Fatalf("enroll: %v", err)
	}
	logger.Printf("doxaboard agent %s, device %s, server %s", version, creds.DeviceID, cfg.ServerURL)

	// Engine, player and slideshow logs carry text from the network (errors,
	// URLs, IDs): clean it before it reaches the journal.
	logf := logsafe.Printf(func(m string) { logger.Print(m) })

	httpClient := &http.Client{Timeout: 60 * time.Second}
	var display atomic.Value
	display.Store(core.DisplaySpec{})

	engine := &core.Engine{
		Client: core.NewClient(cfg.ServerURL, creds.DeviceToken),
		Store:  agent.FileStore{Dir: cfg.DataDir},
		Player: &agent.Player{MpvPath: cfg.MpvPath, MpvArgs: cfg.MpvArgs, Logf: logf},
		Slideshow: &agent.Slideshow{
			ServerURL: cfg.ServerURL,
			Token:     creds.DeviceToken,
			DataDir:   cfg.DataDir,
			MpvPath:   cfg.MpvPath,
			MpvArgs:   cfg.MpvArgs,
			HTTP:      httpClient,
			Logf:      logf,
			Display:   func() core.DisplaySpec { return display.Load().(core.DisplaySpec) },
		},
		Prober: &agent.Prober{
			Interface:   cfg.Interface,
			FFprobePath: cfg.FFprobePath,
			HTTP:        &http.Client{Timeout: 5 * time.Second},
		},
		Platform:     runtime.GOOS + "/" + runtime.GOARCH,
		Version:      version,
		Metrics:      func() map[string]float64 { return agent.Metrics(cfg.Interface) },
		Logf:         logf,
		OnState:      func(st core.DesiredState) { display.Store(st.Display) },
		TickInterval: 5 * time.Second,
		ProbeTimeout: 4 * time.Second,
	}
	if err := engine.Run(ctx); err != nil && !errors.Is(err, context.Canceled) {
		logger.Fatal(err)
	}
}

// enroll returns saved credentials, or registers with the server, retrying
// with backoff until it succeeds (the server may not be up yet after a
// power cut).
func enroll(ctx context.Context, cfg agent.Config, logger *log.Logger) (agent.Credentials, error) {
	creds, err := agent.LoadCredentials(cfg.DataDir)
	if err == nil {
		return creds, nil
	}
	if !errors.Is(err, fs.ErrNotExist) {
		return creds, err
	}
	if cfg.EnrollmentToken == "" {
		return creds, errors.New("not enrolled and no enrollment_token in config")
	}
	host, _ := os.Hostname()
	client := core.NewClient(cfg.ServerURL, "")
	backoff := 2 * time.Second
	for {
		resp, err := client.Enroll(ctx, core.EnrollRequest{
			EnrollmentToken: cfg.EnrollmentToken,
			Hostname:        host,
			Platform:        runtime.GOOS + "/" + runtime.GOARCH,
		})
		if err == nil {
			creds = agent.Credentials{DeviceID: resp.DeviceID, DeviceToken: resp.DeviceToken}
			if err := agent.SaveCredentials(cfg.DataDir, creds); err != nil {
				return creds, fmt.Errorf("save credentials: %w", err)
			}
			logger.Printf("enrolled as %s", creds.DeviceID)
			return creds, nil
		}
		logger.Printf("enrollment failed, retrying in %s: %s", backoff, logsafe.Clean(err.Error()))
		select {
		case <-ctx.Done():
			return creds, ctx.Err()
		case <-time.After(backoff + time.Duration(rand.Int64N(int64(time.Second)))): //nolint:gosec // retry jitter, not security
		}
		backoff = min(backoff*2, 2*time.Minute)
	}
}
