package main

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"vocallabs.com/pandora/services/auth"
	"vocallabs.com/pandora/services/config"
	"vocallabs.com/pandora/services/github"
	"vocallabs.com/pandora/services/handler"
	"vocallabs.com/pandora/services/review"
	"vocallabs.com/pandora/services/service"
	"vocallabs.com/pandora/services/store"
)

func main() {
	log := slog.New(slog.NewTextHandler(os.Stdout, nil))
	cfg, err := config.Load()
	if err != nil {
		log.Error("config", "err", err)
		os.Exit(1)
	}
	st, err := store.Open(cfg.DBPath)
	if err != nil {
		log.Error("store", "err", err)
		os.Exit(1)
	}
	defer st.Close()

	var rv review.Reviewer = review.Mock{}
	if cfg.Provider == "anthropic" {
		rv = review.NewAnthropic(cfg.AnthropicKey, cfg.Model)
	} else {
		log.Warn("using MOCK reviewer; every PR will pass")
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	svc := service.New(ctx, cfg, st, github.NewClient(cfg.GitHubToken), rv, log)
	if err := svc.Recover(ctx); err != nil {
		log.Error("recover", "err", err)
	}

	srv := &http.Server{Addr: cfg.Addr, Handler: handler.New(cfg, st, svc, auth.NewManager(cfg.JWTSecret, cfg.JWTTTL), log), ReadHeaderTimeout: 10 * time.Second}
	go func() {
		log.Info("pandora listening", "addr", cfg.Addr, "provider", cfg.Provider)
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Error("server", "err", err)
			stop()
		}
	}()

	<-ctx.Done()
	log.Info("shutting down")
	sctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	_ = srv.Shutdown(sctx)
	svc.Wait()
}
