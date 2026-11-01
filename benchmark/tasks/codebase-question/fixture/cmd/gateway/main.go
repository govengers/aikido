package main

import (
	"context"
	"log"
	"os"
	"os/signal"

	"example.com/gateway/internal/config"
	"example.com/gateway/internal/server"
)

func main() {
	cfg := config.Load()
	srv := server.New(cfg)
	go func() {
		if err := srv.ListenAndServe(); err != nil {
			log.Println(err)
		}
	}()
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	<-ctx.Done()
	shutdown, cancel := context.WithTimeout(context.Background(), cfg.ShutdownTimeout)
	defer cancel()
	_ = srv.Shutdown(shutdown)
}
