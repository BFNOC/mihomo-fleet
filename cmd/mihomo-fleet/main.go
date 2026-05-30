package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"mihomo-fleet/internal/app"
)

func main() {
	var (
		bind        = flag.String("bind", "127.0.0.1", "HTTP bind address")
		port        = flag.Int("port", 47890, "HTTP bind port")
		dataDir     = flag.String("data", ".mihomo-fleet", "runtime data directory")
		mihomoPath  = flag.String("mihomo", "", "path to mihomo binary")
		openBrowser = flag.Bool("open", false, "print browser URL with emphasis")
	)
	flag.Parse()

	controller, err := app.NewController(app.Options{
		Bind:       *bind,
		Port:       *port,
		DataDir:    *dataDir,
		MihomoPath: *mihomoPath,
	})
	if err != nil {
		log.Fatalf("init controller: %v", err)
	}
	defer controller.Shutdown(context.Background())

	mux := http.NewServeMux()
	controller.RegisterRoutes(mux)
	handler := controller.SecureHandler(mux)

	addr := fmt.Sprintf("%s:%d", *bind, *port)
	server := &http.Server{
		Addr:              addr,
		Handler:           handler,
		ReadHeaderTimeout: 5 * time.Second,
	}

	errs := make(chan error, 1)
	go func() {
		errs <- server.ListenAndServe()
	}()

	url := fmt.Sprintf("http://%s:%d", *bind, *port)
	if *openBrowser {
		log.Printf("Mihomo Fleet ready: %s", url)
	} else {
		log.Printf("listening on %s", url)
	}

	signals := make(chan os.Signal, 1)
	signal.Notify(signals, os.Interrupt, syscall.SIGTERM)

	select {
	case sig := <-signals:
		log.Printf("received %s, shutting down", sig)
	case err := <-errs:
		if err != nil && err != http.ErrServerClosed {
			log.Fatalf("http server: %v", err)
		}
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := server.Shutdown(ctx); err != nil {
		log.Printf("http shutdown: %v", err)
	}
	controller.Shutdown(ctx)
}
