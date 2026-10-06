package main

import (
	"context"
	"encoding/json"
	"flag"
	"log"
	"net"
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
	desktop := flag.Bool("desktop", false, "use a private port for the desktop app")
	flag.Parse()
	address, token := "127.0.0.1:8787", ""
	if *desktop {
		address, token = "127.0.0.1:0", os.Getenv("SCP_CLIENT_DESKTOP_TOKEN")
		if len(token) < 32 {
			log.Fatal("desktop mode requires a startup token")
		}
	}
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
	listener, err := net.Listen("tcp", address)
	if err != nil {
		log.Fatal(err)
	}
	defer listener.Close()
	address = listener.Addr().String()
	server := &http.Server{
		Addr: address, Handler: httpapi.NewWithOptions(home, web.Assets(), records, sessions, httpapi.Options{Address: address, Token: token}),
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
	if *desktop {
		if err := json.NewEncoder(os.Stdout).Encode(struct {
			Event string `json:"event"`
			URL   string `json:"url"`
		}{"ready", "http://" + address}); err != nil {
			log.Fatal(err)
		}
	}
	if err := server.Serve(listener); err != nil && err != http.ErrServerClosed {
		log.Fatal(err)
	}
}
