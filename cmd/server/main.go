// Command server runs the KYC lifecycle service.
//
//	server migrate                apply database migrations (as the owner role)
//	server serve [--worker=false] run the HTTP API and, unless disabled, the
//	                              outbox worker that delivers notifications
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/bfischetti/kyc-lifecycle/internal/app"
	"github.com/bfischetti/kyc-lifecycle/internal/config"
	"github.com/bfischetti/kyc-lifecycle/internal/db"
	"github.com/bfischetti/kyc-lifecycle/internal/httpapi"
	"github.com/bfischetti/kyc-lifecycle/internal/observability"
	"github.com/bfischetti/kyc-lifecycle/internal/outbox"
	"github.com/bfischetti/kyc-lifecycle/internal/screening"
	"github.com/bfischetti/kyc-lifecycle/internal/vault"
)

const usage = "usage: server migrate | server serve [--worker=false]"

func main() {
	if len(os.Args) < 2 {
		fmt.Fprintln(os.Stderr, usage)
		os.Exit(2)
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	var err error
	switch os.Args[1] {
	case "migrate":
		err = migrate(ctx)
	case "serve":
		err = serve(ctx, os.Args[2:])
	default:
		fmt.Fprintln(os.Stderr, usage)
		os.Exit(2)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}

func migrate(ctx context.Context) error {
	cfg, err := config.LoadMigrate(config.Getenv)
	if err != nil {
		return err
	}
	log := observability.NewLogger(os.Stdout, cfg.LogLevel)
	if err := db.Migrate(ctx, cfg.DatabaseURL); err != nil {
		return err
	}
	log.InfoContext(ctx, "migrations applied")
	return nil
}

func serve(ctx context.Context, args []string) error {
	flags := flag.NewFlagSet("serve", flag.ContinueOnError)
	runWorker := flags.Bool("worker", true, "run the outbox worker in this process")
	if err := flags.Parse(args); err != nil {
		return err
	}
	cfg, err := config.LoadServe(config.Getenv)
	if err != nil {
		return err
	}
	log := observability.NewLogger(os.Stdout, cfg.LogLevel)

	store, err := db.Open(ctx, cfg.DatabaseURL)
	if err != nil {
		return fmt.Errorf("database: %w", err)
	}
	defer store.Close()

	evidence := vault.New(vault.Config(cfg.Vault))
	svc := app.New(store, evidence, screening.Mock{}, app.Options{
		UploadURLTTL:    cfg.UploadURLTTL,
		ProviderTimeout: cfg.ProviderTimeout,
	})

	srv := &http.Server{
		Addr:              cfg.HTTPAddr,
		Handler:           httpapi.Handler(svc, log, func(r *http.Request) error { return store.Ping(r.Context()) }),
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       30 * time.Second,
		WriteTimeout:      30 * time.Second,
		IdleTimeout:       120 * time.Second,
	}

	var workerDone chan struct{}
	if *runWorker {
		opts := outbox.DefaultOptions
		opts.PollInterval = cfg.Outbox.PollInterval
		opts.MaxAttempts = cfg.Outbox.MaxAttempts
		worker := outbox.NewWorker(store, outbox.LogPublisher{Log: log}, log, opts)
		workerDone = make(chan struct{})
		go func() { worker.Run(ctx); close(workerDone) }()
	} else {
		log.InfoContext(ctx, "outbox worker disabled")
	}

	errc := make(chan error, 1)
	go func() {
		log.InfoContext(ctx, "listening", slog.String("addr", cfg.HTTPAddr))
		if err := srv.ListenAndServe(); !errors.Is(err, http.ErrServerClosed) {
			errc <- err
		}
		close(errc)
	}()

	select {
	case err := <-errc:
		return err
	case <-ctx.Done():
	}
	log.Info("shutting down")
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	err = srv.Shutdown(shutdownCtx)
	if workerDone != nil {
		<-workerDone // the worker stops on ctx cancellation; let it finish its transaction
	}
	return err
}
