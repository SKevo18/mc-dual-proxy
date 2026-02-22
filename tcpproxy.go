package main

import (
	"bufio"
	"io"
	"log"
	"net"
	"strconv"
	"sync"
	"time"
)

const (
	// peekBufferSize is large enough to detect and buffer the PROXY protocol
	// header without reading into the actual Minecraft protocol data.
	peekBufferSize = 512

	// dialTimeout is how long we wait to connect to the backend.
	dialTimeout = 10 * time.Second
)

func startTCPProxy(cfg Config) {
	ln, err := net.Listen("tcp", cfg.ListenAddr)
	if err != nil {
		log.Fatalf("[tcp] Failed to listen on %s: %v", cfg.ListenAddr, err)
	}
	log.Printf("[tcp] Listening on %s", cfg.ListenAddr)

	for {
		conn, err := ln.Accept()
		if err != nil {
			log.Printf("[tcp] Accept error: %v", err)
			continue
		}
		go handleConnection(conn, cfg.BackendAddr)
	}
}

func handleConnection(clientConn net.Conn, backendAddr string) {
	defer clientConn.Close()

	clientAddr := clientConn.RemoteAddr().String()

	// Wrap in a buffered reader so we can peek without consuming bytes
	br := bufio.NewReaderSize(clientConn, peekBufferSize)

	// Detect PROXY protocol header
	proxyHeader, err := detectProxyProtocol(br)
	if err != nil {
		log.Printf("[tcp] %s: error detecting proxy protocol: %v", clientAddr, err)
		return
	}

	// Determine the real source address for logging
	realAddr := clientAddr
	source := "direct"
	if proxyHeader != nil {
		if proxyHeader.SrcAddr != nil {
			realAddr = net.JoinHostPort(proxyHeader.SrcAddr.String(), itoa(int(proxyHeader.SrcPort)))
		}
		source = "proxied"
	}

	log.Printf("[tcp] %s: new connection (real=%s, source=%s)", clientAddr, realAddr, source)

	// Connect to backend
	backendConn, err := net.DialTimeout("tcp", backendAddr, dialTimeout)
	if err != nil {
		log.Printf("[tcp] %s: failed to connect to backend %s: %v", clientAddr, backendAddr, err)
		return
	}
	defer backendConn.Close()

	// Send PROXY protocol header to backend
	if proxyHeader != nil {
		// Minehut (or other proxy) connection: forward the original header as-is
		if _, err := backendConn.Write(proxyHeader.RawBytes); err != nil {
			log.Printf("[tcp] %s: failed to write proxy header to backend: %v", clientAddr, err)
			return
		}
	} else {
		// Direct connection: generate a v2 header from the real TCP addresses
		header := buildProxyV2Header(clientConn.RemoteAddr(), clientConn.LocalAddr())
		if _, err := backendConn.Write(header); err != nil {
			log.Printf("[tcp] %s: failed to write generated proxy header to backend: %v", clientAddr, err)
			return
		}
	}

	// Bidirectional pipe: client ↔ backend
	// The buffered reader may still have unread data from the peek,
	// so we use it as the client reader instead of the raw conn.
	var wg sync.WaitGroup
	wg.Add(2)

	// Client → Backend
	go func() {
		defer wg.Done()
		_, err := io.Copy(backendConn, br)
		if err != nil {
			logPipeError("client→backend", clientAddr, err)
		}
		// Signal to backend that client is done writing
		if tc, ok := backendConn.(*net.TCPConn); ok {
			tc.CloseWrite()
		}
	}()

	// Backend → Client
	go func() {
		defer wg.Done()
		_, err := io.Copy(clientConn, backendConn)
		if err != nil {
			logPipeError("backend→client", clientAddr, err)
		}
		// Signal to client that backend is done writing
		if tc, ok := clientConn.(*net.TCPConn); ok {
			tc.CloseWrite()
		}
	}()

	wg.Wait()
	log.Printf("[tcp] %s: connection closed", clientAddr)
}

func logPipeError(direction, clientAddr string, err error) {
	// Don't log normal connection resets / EOF
	if err == io.EOF {
		return
	}
	if netErr, ok := err.(*net.OpError); ok {
		if netErr.Err.Error() == "use of closed network connection" {
			return
		}
	}
	log.Printf("[tcp] %s: pipe %s error: %v", clientAddr, direction, err)
}

func itoa(i int) string {
	return strconv.Itoa(i)
}
