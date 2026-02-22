package main

import (
	"context"
	"fmt"
	"io"
	"log"
	"net/http"
	"strings"
	"time"
)

const (
	// hasJoinedPath is the Mojang session server endpoint.
	hasJoinedPath = "/session/minecraft/hasJoined"

	// upstreamTimeout is how long we wait for each upstream session server.
	upstreamTimeout = 10 * time.Second
)

// authResult holds the response from a single upstream session server.
type authResult struct {
	StatusCode int
	Body       []byte
	Server     string
	Err        error
}

func startMultiauth(cfg Config) {
	mux := http.NewServeMux()

	// Handle the hasJoined endpoint
	mux.HandleFunc(hasJoinedPath, func(w http.ResponseWriter, r *http.Request) {
		handleHasJoined(w, r, cfg.SessionServers)
	})

	// Health check
	mux.HandleFunc("/health", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		fmt.Fprint(w, "ok")
	})

	// Catch-all: return 404 with info
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		// Some server software may hit slightly different paths,
		// so if it looks like a hasJoined request, handle it
		if strings.Contains(r.URL.Path, "hasJoined") {
			handleHasJoined(w, r, cfg.SessionServers)
			return
		}
		w.WriteHeader(http.StatusNotFound)
		fmt.Fprint(w, "mc-dual-proxy multiauth server")
	})

	server := &http.Server{
		Addr:         cfg.AuthListenAddr,
		Handler:      mux,
		ReadTimeout:  15 * time.Second,
		WriteTimeout: 30 * time.Second,
	}

	log.Printf("[auth] Listening on %s", cfg.AuthListenAddr)
	if err := server.ListenAndServe(); err != nil {
		log.Fatalf("[auth] Failed to start: %v", err)
	}
}

// handleHasJoined fans out the hasJoined request to all configured session
// servers concurrently and returns the first successful (HTTP 200) response.
//
// The Minecraft login flow guarantees that only the "correct" session server
// will return 200 for any given serverId hash, because the hash is derived
// from the encryption handshake which is unique per connection path.
func handleHasJoined(w http.ResponseWriter, r *http.Request, servers []string) {
	query := r.URL.RawQuery
	username := r.URL.Query().Get("username")

	if query == "" {
		http.Error(w, "missing query parameters", http.StatusBadRequest)
		return
	}

	log.Printf("[auth] hasJoined request: username=%s", username)

	ctx, cancel := context.WithTimeout(r.Context(), upstreamTimeout)
	defer cancel()

	// Fan out requests to all session servers concurrently
	resultCh := make(chan authResult, len(servers))
	for _, server := range servers {
		go querySessionServer(ctx, server, query, resultCh)
	}

	// Wait for a successful response or all failures
	var lastResult authResult
	remaining := len(servers)

	for remaining > 0 {
		select {
		case result := <-resultCh:
			remaining--

			if result.Err != nil {
				log.Printf("[auth]   %s: error: %v", result.Server, result.Err)
				lastResult = result
				continue
			}

			if result.StatusCode == http.StatusOK && len(result.Body) > 0 {
				// Success! This is the correct session server for this connection.
				log.Printf("[auth]   %s: SUCCESS (200, %d bytes)", result.Server, len(result.Body))
				cancel() // Cancel remaining requests

				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusOK)
				w.Write(result.Body)
				return
			}

			log.Printf("[auth]   %s: no match (status=%d, body=%d bytes)", result.Server, result.StatusCode, len(result.Body))
			lastResult = result

		case <-ctx.Done():
			log.Printf("[auth]   timeout waiting for session servers")
			w.WriteHeader(http.StatusNoContent)
			return
		}
	}

	// All servers responded but none returned 200
	log.Printf("[auth]   all servers failed for username=%s (last status=%d)", username, lastResult.StatusCode)

	// Return 204 No Content (standard "auth failed" response for Minecraft)
	w.WriteHeader(http.StatusNoContent)
}

// querySessionServer makes a hasJoined request to a single upstream session server.
func querySessionServer(ctx context.Context, serverBase, rawQuery string, resultCh chan<- authResult) {
	// Build the full URL: base + /session/minecraft/hasJoined?query
	url := strings.TrimRight(serverBase, "/") + hasJoinedPath + "?" + rawQuery

	// Identify the server for logging
	serverName := serverBase
	if strings.Contains(serverBase, "mojang") {
		serverName = "mojang"
	} else if strings.Contains(serverBase, "minehut") {
		serverName = "minehut"
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		resultCh <- authResult{Server: serverName, Err: fmt.Errorf("create request: %w", err)}
		return
	}

	// Use a client without following redirects for safety
	client := &http.Client{
		Timeout: upstreamTimeout,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}

	resp, err := client.Do(req)
	if err != nil {
		resultCh <- authResult{Server: serverName, Err: fmt.Errorf("request failed: %w", err)}
		return
	}
	defer resp.Body.Close()

	// Read the response body (session server responses are small JSON objects)
	body, err := io.ReadAll(io.LimitReader(resp.Body, 64*1024)) // 64KB max
	if err != nil {
		resultCh <- authResult{Server: serverName, Err: fmt.Errorf("read body: %w", err)}
		return
	}

	resultCh <- authResult{
		StatusCode: resp.StatusCode,
		Body:       body,
		Server:     serverName,
	}
}
