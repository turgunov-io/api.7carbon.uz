// Command carbon_go serves the 7 Carbon public site API and its admin backend.
//
// This file is the composition root: it loads configuration, opens the database,
// wires the handlers and middleware together, and manages the server lifecycle.
// Everything else lives under internal/.
package main

import (
	"context"
	"errors"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"carbon_go/internal/database"
	"carbon_go/internal/env"
	"carbon_go/internal/handlers"
	"carbon_go/internal/httpmw"
)

func main() {
	if err := env.LoadDotEnv(); err != nil {
		log.Printf("warning: could not load .env: %v", err)
	}

	dsn := env.FirstNonEmpty(
		os.Getenv("DATABASE_URL"),
		os.Getenv("POSTGRES_DSN"),
	)
	if dsn == "" {
		log.Fatal("DATABASE_URL or POSTGRES_DSN must be set")
	}

	db, err := database.Open(database.NormalizeDSN(dsn))
	if err != nil {
		log.Fatalf("database connection failed: %v", err)
	}
	defer db.Close()

	app := handlers.New(db)

	server := &http.Server{
		Addr:         ":" + env.FirstNonEmpty(os.Getenv("PORT"), "8080"),
		Handler:      httpmw.Logging(httpmw.CORS(httpmw.Response(app.Routes()))),
		ReadTimeout:  15 * time.Second,
		WriteTimeout: 15 * time.Second,
		IdleTimeout:  60 * time.Second,
	}

	go func() {
		log.Printf("listening on %s", server.Addr)
		if err := server.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Fatalf("server error: %v", err)
		}
	}()

	// Graceful shutdown on SIGINT/SIGTERM.
	stop := make(chan os.Signal, 1)
	signal.Notify(stop, os.Interrupt, syscall.SIGTERM)
	<-stop

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	if err := server.Shutdown(ctx); err != nil {
		log.Printf("server shutdown error: %v", err)
	}
	if err := db.Close(); err != nil {
		log.Printf("database close error: %v", err)
	}
	log.Println("shutdown complete")
}
