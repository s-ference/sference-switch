// tlsdoor.go implements `sference-switch tlsdoor` — the TLS-terminating
// front door process that listens on 127.0.0.1:443 and forwards decrypted
// traffic to the router. This is the transparent-interception entrypoint:
// it runs as root (to bind 443) and is managed by launchd.
package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/sference/sference-switch/gateway/internal/config"
	"github.com/sference/sference-switch/gateway/internal/tlsdoor"
)

func cmdTLSDoor(args []string) int {
	fs := flag.NewFlagSet("sference-switch tlsdoor", flag.ContinueOnError)
	port := fs.Int("port", 443, "port to listen on (443 requires root)")
	if err := fs.Parse(args); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 2
	}
	configDir := config.DefaultDir()
	leafCert, leafKey, err := tlsdoorCertPaths(configDir)
	if err != nil {
		fmt.Fprintf(os.Stderr, "tlsdoor: %v\n", err)
		return 1
	}
	addr := fmt.Sprintf("127.0.0.1:%d", *port)
	d, err := tlsdoor.New(tlsdoor.Config{
		ListenAddr:   addr,
		RouterTarget: "127.0.0.1:45272",
		AdminTarget:  "127.0.0.1:45273",
		CertFile:     leafCert,
		KeyFile:      leafKey,
	})
	if err != nil {
		fmt.Fprintf(os.Stderr, "tlsdoor: %v\n", err)
		return 1
	}
	if err := d.Start(); err != nil {
		fmt.Fprintf(os.Stderr, "tlsdoor: %v\n", err)
		return 1
	}
	fmt.Fprintf(os.Stderr, "[sference-switch tlsdoor] listening on %s -> router 127.0.0.1:45272\n", addr)

	serveErr := make(chan error, 1)
	go func() { serveErr <- d.Serve() }()

	sc := make(chan os.Signal, 2)
	signal.Notify(sc, syscall.SIGTERM, syscall.SIGINT)
	select {
	case sig := <-sc:
		fmt.Fprintf(os.Stderr, "[sference-switch tlsdoor] %s received\n", sig)
	case err := <-serveErr:
		fmt.Fprintf(os.Stderr, "[sference-switch tlsdoor] serve error: %v\n", err)
	}
	fmt.Fprintf(os.Stderr, "[sference-switch tlsdoor] shutting down\n")
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	_ = d.Shutdown(ctx.Done())
	return 0
}

func tlsdoorCertPaths(configDir string) (cert, key string, err error) {
	// The cert paths come from tlsca.Ensure (created by `tls setup`).
	// We just need to check they exist.
	cert = configDir + "/ca/leaf.pem"
	key = configDir + "/ca/leaf-key.pem"
	if _, err := os.Stat(cert); err != nil {
		return "", "", fmt.Errorf("leaf certificate not found at %s; run 'sference-switch tls setup' first", cert)
	}
	if _, err := os.Stat(key); err != nil {
		return "", "", fmt.Errorf("leaf key not found at %s; run 'sference-switch tls setup' first", key)
	}
	return cert, key, nil
}
