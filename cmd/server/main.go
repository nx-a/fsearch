package main

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/nx-a/fsearch"
	"github.com/nx-a/fsearch/httpapi"
	"github.com/nx-a/fsearch/internal/event"
)

func main() {
	conf := NewConfig()

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	var err error
	if conf.IsReadOnly() {
		err = runReader(ctx, conf)
	} else {
		err = runWriter(ctx, conf)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}

// runWriter запускает под-писатель: открывает БД на запись и обслуживает весь
// REST API, включая отдачу снапшота читателям.
func runWriter(ctx context.Context, conf *Config) error {
	e, err := fsearch.Open(fsearch.Options{Path: conf.Db})
	if err != nil {
		return err
	}
	defer e.Close()
	srv := httpapi.NewServer(e, conf)
	log.Printf("fsearch писатель слушает :%s (БД: %s)", conf.ServicePort, conf.Db)
	_ = event.NewIn(e)
	return serve(ctx, conf, srv)
}

// serve поднимает HTTP-сервер и корректно останавливает его по отмене ctx.
func serve(ctx context.Context, conf *Config, handler http.Handler) error {
	httpServer := &http.Server{
		Addr:              ":" + conf.ServicePort,
		Handler:           handler,
		ReadHeaderTimeout: 10 * time.Second,
	}
	errCh := make(chan error, 1)
	go func() {
		err := httpServer.ListenAndServe()
		if err != nil && !errors.Is(err, http.ErrServerClosed) {
			errCh <- err
			return
		}
		errCh <- nil
	}()

	select {
	case <-ctx.Done():
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		return httpServer.Shutdown(shutdownCtx)
	case err := <-errCh:
		return err
	}
}
