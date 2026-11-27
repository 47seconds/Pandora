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
	switch cfg.Provider {
	case "anthropic":
		rv = review.NewAnthropic(cfg.AnthropicKey, cfg.Model)
	case "openrouter":
		rv = review.NewOpenRouter(cfg.OpenRouterKey, cfg.Model)
	default:
		log.Warn("using MOCK reviewer; every PR will pass")
	}

	var src github.TokenSource = github.StaticToken(cfg.GitHubToken)
	var prSource service.PRSource // only a GitHub App can list org PRs for the backfill
	if cfg.GitHubAppID != "" {
		app, err := github.NewAppAuth(cfg.GitHubAppID, cfg.GitHubAppKey, "")
		if err != nil {
			log.Error("github app", "err", err)
			os.Exit(1)
		}
		src, prSource = app, app
		log.Info("github auth: app", "app_id", cfg.GitHubAppID)
	} else {
		log.Warn("github auth: personal access token (dev fallback); use a GitHub App in production")
	}
	ghc := github.NewClient(src)

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	svc := service.New(ctx, cfg, st, ghc, rv, prSource, log)
	if err := svc.Recover(ctx); err != nil {
		log.Error("recover", "err", err)
	}
	if prSource != nil {
		svc.StartSync() // backfill recent PRs in the background (BackfillDays)
	}
	if n, err := st.CountAdmins(ctx); err == nil && n == 0 && cfg.BootstrapAdmin == "" {
		log.Warn("no admin exists: set PANDORA_BOOTSTRAP_ADMIN_GITHUB to your GitHub username, then sign in")
	}

	srv := &http.Server{Addr: cfg.Addr, Handler: handler.New(handler.Deps{
		Config: cfg, Store: st, Svc: svc, Auth: auth.NewManager(cfg.JWTSecret, cfg.JWTTTL),
		OAuth: github.NewOAuth(cfg.OAuthClientID, cfg.OAuthClientSecret, cfg.RedirectURL()), Log: log,
	}), ReadHeaderTimeout: 10 * time.Second}
	go func() {
		log.Info("pandora listening", "addr", cfg.Addr, "provider", cfg.Provider, "model", cfg.Model)
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
