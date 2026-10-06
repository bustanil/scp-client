package main

import (
	"context"
	"log"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"scp-client/internal/connections"
	"scp-client/internal/httpapi"
	"scp-client/internal/knownhosts"
	"scp-client/internal/secrets"
	"scp-client/internal/session"
	"scp-client/web"
)

func main() {
	home, err := os.UserHomeDir()
	if err != nil {
		log.Fatal(err)
	}
	dataDir := os.Getenv("SCP_CLIENT_DATA_DIR")
	if dataDir == "" {
		dataDir = filepath.Join(home, ".scp-client")
	}
	records, err := connections.New(dataDir, secrets.Keychain{})
	if err != nil {
		log.Fatal(err)
	}
	hosts, err := knownhosts.New(dataDir)
	if err != nil {
		log.Fatal(err)
	}
	sessions := session.New(records, hosts)
	defer sessions.Close()
	server := &http.Server{
		Addr: "127.0.0.1:8787", Handler: httpapi.NewWithServices(home, web.Assets(), records, sessions),
		ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 30 * time.Second,
		WriteTimeout: 30 * time.Second, IdleTimeout: 60 * time.Second,
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	go func() {
		<-ctx.Done()
		shutdown, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = server.Shutdown(shutdown)
	}()
	log.Printf("scp-client listening at http://%s", server.Addr)
	if err := server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		log.Fatal(err)
	}
}
