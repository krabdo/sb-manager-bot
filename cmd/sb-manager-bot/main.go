package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/krabdo/sb-manager-bot/internal/manager"
)

var version = "dev"

func main() {
	if len(os.Args) > 1 && os.Args[1] == "--version" {
		fmt.Println("sb-manager-bot " + version)
		return
	}
	if len(os.Args) > 1 && os.Args[1] == "healthcheck" {
		addr := os.Getenv("HTTP_ADDR")
		if addr == "" {
			if p := os.Getenv("PORT"); p != "" {
				addr = ":" + p
			} else {
				addr = ":8080"
			}
		}
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		_, port, err := net.SplitHostPort(addr)
		if err != nil || port == "" {
			os.Exit(1)
		}
		req, _ := http.NewRequestWithContext(ctx, http.MethodGet, "http://127.0.0.1:"+port+"/readyz", nil)
		resp, e := http.DefaultClient.Do(req)
		if e != nil || resp.StatusCode != http.StatusOK {
			os.Exit(1)
		}
		resp.Body.Close()
		return
	}
	if e := run(); e != nil {
		slog.Error("service stopped", "error", e)
		os.Exit(1)
	}
}
func run() error {
	log := slog.New(slog.NewJSONHandler(os.Stdout, nil))
	cfg, e := manager.LoadConfig()
	if e != nil {
		return e
	}
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	store, e := manager.OpenStore(ctx, cfg.DatabaseFile)
	if e != nil {
		return e
	}
	defer store.Close()
	cipher, e := manager.NewCookieCipher(cfg.EncryptionKey)
	if e != nil {
		return e
	}
	if e = store.ValidateCookies(ctx, cipher); e != nil {
		return e
	}
	gate := manager.NewRequestGate(cfg.ForumRate)
	forum, e := manager.NewForumClient(cfg.ForumBaseURL, cfg.ForumTimeout, version, gate)
	if e != nil {
		return e
	}
	offset, e := store.UpdateOffsetValue(ctx)
	if e != nil {
		return e
	}
	telegram, e := manager.NewTelegram(cfg.TelegramToken, cfg.TelegramAPIURL, offset, store, log)
	if e != nil {
		return e
	}
	controller := manager.NewController(store, cipher, forum, telegram, cfg.AdminIDs, cfg.MaxUsers, log)
	telegram.SetController(controller)
	poller := manager.NewPoller(store, cipher, forum, telegram, cfg.AdminIDs, cfg.PollInterval, cfg.Workers, log)
	var botReady, pollerReady atomic.Bool
	botReady.Store(true)
	pollerReady.Store(true)
	health := manager.NewHealth(store, &botReady, &pollerReady)
	server := &http.Server{Addr: cfg.HTTPAddr, Handler: health.Handler(), ReadHeaderTimeout: 5 * time.Second}
	errCh := make(chan error, 1)
	go func() {
		e := server.ListenAndServe()
		if e != nil && e != http.ErrServerClosed {
			errCh <- e
		}
	}()
	var services sync.WaitGroup
	services.Add(2)
	go func() { defer services.Done(); poller.Run(ctx); pollerReady.Store(false) }()
	go func() { defer services.Done(); telegram.Start(ctx); botReady.Store(false) }()
	log.Info("sb-manager-bot started", "version", version, "workers", cfg.Workers, "max_users", cfg.MaxUsers)
	var runErr error
	select {
	case <-ctx.Done():
	case e := <-errCh:
		runErr = e
		cancel()
	}
	shutdownCtx, stop := context.WithTimeout(context.Background(), 10*time.Second)
	defer stop()
	if e := server.Shutdown(shutdownCtx); e != nil && runErr == nil {
		runErr = e
	}
	done := make(chan struct{})
	go func() { services.Wait(); close(done) }()
	select {
	case <-done:
	case <-shutdownCtx.Done():
		if runErr == nil {
			runErr = errors.New("service shutdown timed out")
		}
	}
	return runErr
}
