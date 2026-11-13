package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"arcam/internal/app"
	"arcam/internal/httpapi"
	"arcam/internal/store"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func main() {
	if e := run(); e != nil {
		log.Fatal(e)
	}
}
func run() error {
	addr := flag.String("addr", "127.0.0.1:8080", "HTTP listen address")
	data := flag.String("data", "./data", "persistent data directory (single process)")
	stdio := flag.Bool("mcp-stdio", false, "serve research MCP over stdio instead of HTTP")
	seed := flag.Bool("seed-demo", true, "seed clearly labeled synthetic demo data")
	flag.Parse()
	host, _, e := net.SplitHostPort(*addr)
	if e != nil {
		return e
	}
	token := os.Getenv("ARCAM_API_TOKEN")
	if host != "127.0.0.1" && host != "localhost" && host != "::1" && token == "" {
		return fmt.Errorf("non-loopback binding requires ARCAM_API_TOKEN")
	}
	st, e := store.Open(*data)
	if e != nil {
		return e
	}
	defer st.Close()
	svc, e := app.New(st)
	if e != nil {
		return e
	}
	defer svc.Close()
	if _, e = app.NewSessions(svc); e != nil {
		return e
	}
	if *seed {
		if e = svc.Seed(); e != nil {
			return e
		}
	}
	api := &httpapi.Server{App: svc, Token: token, AllowedHosts: strings.FieldsFunc(os.Getenv("ARCAM_ALLOWED_HOSTS"), func(r rune) bool { return r == ',' })}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if *stdio {
		return api.MCPServer().Run(ctx, &mcp.StdioTransport{})
	}
	server := &http.Server{Addr: *addr, Handler: api.Handler(), ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 30 * time.Second, WriteTimeout: 10 * time.Minute, IdleTimeout: 60 * time.Second, MaxHeaderBytes: 1 << 20}
	errCh := make(chan error, 1)
	go func() {
		log.Printf("Arcam listening on http://%s (live enabled: %t)", *addr, svc.Sessions.LiveEnabled)
		errCh <- server.ListenAndServe()
	}()
	select {
	case e = <-errCh:
		if e != http.ErrServerClosed {
			return e
		}
	case <-ctx.Done():
		shutdown, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer cancel()
		return server.Shutdown(shutdown)
	}
	return nil
}
