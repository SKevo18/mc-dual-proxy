package main

import (
	"flag"
	"fmt"
	"log"
	"os"
	"os/signal"
	"strings"
	"syscall"
)

// Config holds all runtime configuration.
type Config struct {
	// Address the TCP proxy listens on (players connect here)
	ListenAddr string
	// Address of the actual backend (Velocity/Paper)
	BackendAddr string

	// Address the multiauth HTTP server listens on
	AuthListenAddr string

	// Session server endpoints to fan out to
	SessionServers []string
}

func main() {
	cfg := Config{}

	flag.StringVar(&cfg.ListenAddr, "listen", "0.0.0.0:25565", "TCP proxy listen address (players connect here)")
	flag.StringVar(&cfg.BackendAddr, "backend", "127.0.0.1:25566", "Backend server address (Velocity/Paper)")
	flag.StringVar(&cfg.AuthListenAddr, "auth-listen", "127.0.0.1:8652", "Multiauth HTTP server listen address")

	sessionServers := flag.String("session-servers", "https://sessionserver.mojang.com,https://api.minehut.com/mitm/proxy", "Comma-separated session server base URLs")

	flag.Parse()

	for _, s := range strings.Split(*sessionServers, ",") {
		s = strings.TrimSpace(s)
		if s != "" {
			cfg.SessionServers = append(cfg.SessionServers, s)
		}
	}

	if len(cfg.SessionServers) == 0 {
		log.Fatal("At least one session server must be configured")
	}

	log.SetFlags(log.Ldate | log.Ltime | log.Lmicroseconds)

	log.Println("=== mc-dual-proxy ===")
	log.Printf("TCP proxy:   %s → %s", cfg.ListenAddr, cfg.BackendAddr)
	log.Printf("Multiauth:   %s", cfg.AuthListenAddr)
	log.Printf("Session servers: %v", cfg.SessionServers)
	fmt.Println()
	printSetupInstructions(cfg)

	go startMultiauth(cfg)
	go startTCPProxy(cfg)

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	sig := <-sigCh
	log.Printf("Received %s, shutting down", sig)
}

func printSetupInstructions(cfg Config) {
	fmt.Println("--- Setup Instructions ---")
	fmt.Println()
	fmt.Println("For Velocity, use these JVM flags:")
	fmt.Printf("  -Dmojang.sessionserver=http://%s/session/minecraft/hasJoined\n", cfg.AuthListenAddr)
	fmt.Println()
	fmt.Println("For standalone Paper, use these JVM flags:")
	fmt.Printf("  -Dminecraft.api.session.host=http://%s\n", cfg.AuthListenAddr)
	fmt.Println()
	fmt.Println("In the Minehut panel, point your external server to this proxy's")
	fmt.Printf("public IP on port %s (the -listen port).\n", strings.Split(cfg.ListenAddr, ":")[len(strings.Split(cfg.ListenAddr, ":"))-1])
	fmt.Println()
	fmt.Printf("Your backend (Velocity/Paper) should listen on %s with\n", cfg.BackendAddr)
	fmt.Println("proxy-protocol enabled (haproxy-protocol = true for Velocity,")
	fmt.Println("proxy-protocol: true in paper-global.yml for Paper).")
	fmt.Println()
	fmt.Println("For Caddy reverse proxy to the multiauth server:")
	fmt.Printf("  auth.yourdomain.com { reverse_proxy %s }\n", cfg.AuthListenAddr)
	fmt.Println()
	fmt.Println("--------------------------")
	fmt.Println()
}
