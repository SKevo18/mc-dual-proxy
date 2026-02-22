package main

import (
	"bufio"
	"bytes"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// --- PROXY Protocol Tests ---

func TestDetectProxyV2(t *testing.T) {
	// Build a valid v2 header for 192.168.1.100:12345 → 10.0.0.1:25565
	header := make([]byte, 28) // 16 + 12 (IPv4)
	copy(header[0:12], proxyV2Sig)
	header[12] = 0x21 // version 2, PROXY command
	header[13] = 0x11 // AF_INET, STREAM
	binary.BigEndian.PutUint16(header[14:16], 12) // addr length
	copy(header[16:20], net.ParseIP("192.168.1.100").To4())
	copy(header[20:24], net.ParseIP("10.0.0.1").To4())
	binary.BigEndian.PutUint16(header[24:26], 12345)
	binary.BigEndian.PutUint16(header[26:28], 25565)

	// Append some "Minecraft data" after the header
	mcData := []byte("MINECRAFT_HANDSHAKE_DATA_HERE")
	data := append(header, mcData...)

	br := bufio.NewReaderSize(bytes.NewReader(data), 512)
	ph, err := detectProxyProtocol(br)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if ph == nil {
		t.Fatal("expected v2 header to be detected")
	}
	if ph.Version != 2 {
		t.Fatalf("expected version 2, got %d", ph.Version)
	}
	if ph.SrcAddr.String() != "192.168.1.100" {
		t.Fatalf("expected src 192.168.1.100, got %s", ph.SrcAddr)
	}
	if ph.SrcPort != 12345 {
		t.Fatalf("expected src port 12345, got %d", ph.SrcPort)
	}
	if ph.DstAddr.String() != "10.0.0.1" {
		t.Fatalf("expected dst 10.0.0.1, got %s", ph.DstAddr)
	}
	if ph.DstPort != 25565 {
		t.Fatalf("expected dst port 25565, got %d", ph.DstPort)
	}

	// The remaining data in the reader should be the Minecraft data
	remaining, _ := io.ReadAll(br)
	if !bytes.Equal(remaining, mcData) {
		t.Fatalf("remaining data mismatch: got %q, want %q", remaining, mcData)
	}
}

func TestDetectProxyV1(t *testing.T) {
	v1Header := "PROXY TCP4 192.168.1.50 10.0.0.1 54321 25565\r\n"
	mcData := []byte("MINECRAFT_DATA")
	data := append([]byte(v1Header), mcData...)

	br := bufio.NewReaderSize(bytes.NewReader(data), 512)
	ph, err := detectProxyProtocol(br)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if ph == nil {
		t.Fatal("expected v1 header to be detected")
	}
	if ph.Version != 1 {
		t.Fatalf("expected version 1, got %d", ph.Version)
	}
	if ph.SrcAddr.String() != "192.168.1.50" {
		t.Fatalf("expected src 192.168.1.50, got %s", ph.SrcAddr)
	}
	if ph.SrcPort != 54321 {
		t.Fatalf("expected src port 54321, got %d", ph.SrcPort)
	}

	remaining, _ := io.ReadAll(br)
	if !bytes.Equal(remaining, mcData) {
		t.Fatalf("remaining data mismatch: got %q, want %q", remaining, mcData)
	}
}

func TestDetectNoProxyProtocol(t *testing.T) {
	// A Minecraft handshake starts with a VarInt packet length, then packet ID 0x00.
	// It definitely won't start with "PROXY " or the v2 signature.
	mcData := []byte{0x10, 0x00, 0xFD, 0x05, 0x09, 0x6C, 0x6F, 0x63, 0x61, 0x6C, 0x68, 0x6F, 0x73, 0x74, 0x63, 0xDD, 0x02}

	br := bufio.NewReaderSize(bytes.NewReader(mcData), 512)
	ph, err := detectProxyProtocol(br)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if ph != nil {
		t.Fatal("expected no proxy header, but one was detected")
	}

	// All data should still be readable
	remaining, _ := io.ReadAll(br)
	if !bytes.Equal(remaining, mcData) {
		t.Fatalf("data should be untouched: got %d bytes, want %d bytes", len(remaining), len(mcData))
	}
}

func TestBuildProxyV2Header(t *testing.T) {
	src := &net.TCPAddr{IP: net.ParseIP("203.0.113.50"), Port: 49152}
	dst := &net.TCPAddr{IP: net.ParseIP("10.0.0.1"), Port: 25565}

	header := buildProxyV2Header(src, dst)

	// Should be 16 + 12 = 28 bytes for IPv4
	if len(header) != 28 {
		t.Fatalf("expected 28 bytes, got %d", len(header))
	}

	// Verify signature
	if !bytes.Equal(header[0:12], proxyV2Sig) {
		t.Fatal("invalid v2 signature")
	}

	// Verify version and command
	if header[12] != 0x21 {
		t.Fatalf("expected ver=2 cmd=PROXY (0x21), got 0x%02x", header[12])
	}

	// Parse it back to verify
	br := bufio.NewReaderSize(bytes.NewReader(header), 512)
	ph, err := detectProxyProtocol(br)
	if err != nil {
		t.Fatalf("failed to parse generated header: %v", err)
	}
	if ph.SrcAddr.String() != "203.0.113.50" {
		t.Fatalf("roundtrip src addr mismatch: %s", ph.SrcAddr)
	}
	if ph.SrcPort != 49152 {
		t.Fatalf("roundtrip src port mismatch: %d", ph.SrcPort)
	}
}

func TestBuildProxyV2HeaderIPv6(t *testing.T) {
	src := &net.TCPAddr{IP: net.ParseIP("2001:db8::1"), Port: 49152}
	dst := &net.TCPAddr{IP: net.ParseIP("2001:db8::2"), Port: 25565}

	header := buildProxyV2Header(src, dst)

	// Should be 16 + 36 = 52 bytes for IPv6
	if len(header) != 52 {
		t.Fatalf("expected 52 bytes, got %d", len(header))
	}

	// Parse it back
	br := bufio.NewReaderSize(bytes.NewReader(header), 512)
	ph, err := detectProxyProtocol(br)
	if err != nil {
		t.Fatalf("failed to parse generated header: %v", err)
	}
	if ph.SrcAddr.String() != "2001:db8::1" {
		t.Fatalf("roundtrip src addr mismatch: %s", ph.SrcAddr)
	}
}

// --- Multiauth Tests ---

func TestMultiauthFirstServerSucceeds(t *testing.T) {
	// Simulate Mojang returning 200 (direct player)
	mojang := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("username") != "TestPlayer" {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]interface{}{
			"id":   "abcdef1234567890abcdef1234567890",
			"name": "TestPlayer",
		})
	}))
	defer mojang.Close()

	// Simulate Minehut returning 204 (not their player)
	minehut := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(50 * time.Millisecond) // Slight delay
		w.WriteHeader(http.StatusNoContent)
	}))
	defer minehut.Close()

	servers := []string{mojang.URL, minehut.URL}

	req := httptest.NewRequest("GET", "/session/minecraft/hasJoined?username=TestPlayer&serverId=abc123", nil)
	rec := httptest.NewRecorder()

	handleHasJoined(rec, req, servers)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}

	var body map[string]interface{}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("failed to parse response: %v", err)
	}
	if body["name"] != "TestPlayer" {
		t.Fatalf("expected name TestPlayer, got %v", body["name"])
	}
}

func TestMultiauthSecondServerSucceeds(t *testing.T) {
	// Simulate Mojang returning 204 (Minehut player, hash won't match Mojang)
	mojang := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))
	defer mojang.Close()

	// Simulate Minehut returning 200
	minehut := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]interface{}{
			"id":   "1234567890abcdef1234567890abcdef",
			"name": "MinehutPlayer",
		})
	}))
	defer minehut.Close()

	servers := []string{mojang.URL, minehut.URL}

	req := httptest.NewRequest("GET", "/session/minecraft/hasJoined?username=MinehutPlayer&serverId=def456", nil)
	rec := httptest.NewRecorder()

	handleHasJoined(rec, req, servers)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}

	var body map[string]interface{}
	json.Unmarshal(rec.Body.Bytes(), &body)
	if body["name"] != "MinehutPlayer" {
		t.Fatalf("expected name MinehutPlayer, got %v", body["name"])
	}
}

func TestMultiauthBothFail(t *testing.T) {
	// Both servers return 204
	server1 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))
	defer server1.Close()

	server2 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))
	defer server2.Close()

	servers := []string{server1.URL, server2.URL}

	req := httptest.NewRequest("GET", "/session/minecraft/hasJoined?username=FakePlayer&serverId=xyz", nil)
	rec := httptest.NewRecorder()

	handleHasJoined(rec, req, servers)

	if rec.Code != http.StatusNoContent {
		t.Fatalf("expected 204 when both fail, got %d", rec.Code)
	}
}

// --- Integration Test: TCP proxy + backend ---

func TestTCPProxyDirectConnection(t *testing.T) {
	// Start a mock "backend" that expects a PROXY protocol v2 header
	backendLn, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer backendLn.Close()

	backendGotHeader := make(chan *ProxyHeader, 1)
	backendGotData := make(chan []byte, 1)

	go func() {
		conn, err := backendLn.Accept()
		if err != nil {
			return
		}
		defer conn.Close()

		br := bufio.NewReaderSize(conn, 512)
		ph, _ := detectProxyProtocol(br)
		backendGotHeader <- ph

		data, _ := io.ReadAll(br)
		backendGotData <- data

		conn.Write([]byte("RESPONSE"))
	}()

	// Start the TCP proxy
	proxyLn, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer proxyLn.Close()

	go func() {
		conn, err := proxyLn.Accept()
		if err != nil {
			return
		}
		handleConnection(conn, backendLn.Addr().String())
	}()

	// Connect as a "direct player" (no PROXY protocol)
	clientConn, err := net.DialTimeout("tcp", proxyLn.Addr().String(), 2*time.Second)
	if err != nil {
		t.Fatalf("failed to connect to proxy: %v", err)
	}
	defer clientConn.Close()

	// Send some Minecraft-like data
	clientConn.Write([]byte("HELLO_MC"))
	clientConn.(*net.TCPConn).CloseWrite()

	// Verify backend received a PROXY protocol header
	select {
	case ph := <-backendGotHeader:
		if ph == nil {
			t.Fatal("backend did not receive PROXY protocol header")
		}
		if ph.Version != 2 {
			t.Fatalf("expected v2 header, got v%d", ph.Version)
		}
		t.Logf("Backend received v2 header: src=%s:%d", ph.SrcAddr, ph.SrcPort)
	case <-time.After(3 * time.Second):
		t.Fatal("timeout waiting for backend to receive header")
	}

	// Verify backend received the MC data
	select {
	case data := <-backendGotData:
		if !bytes.Equal(data, []byte("HELLO_MC")) {
			t.Fatalf("backend got %q, expected HELLO_MC", data)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("timeout waiting for backend data")
	}

	// Verify client can read the response
	clientConn.SetReadDeadline(time.Now().Add(3 * time.Second))
	resp, _ := io.ReadAll(clientConn)
	if !bytes.Equal(resp, []byte("RESPONSE")) {
		t.Fatalf("client got %q, expected RESPONSE", resp)
	}
}

func TestTCPProxyPassthroughProxyProtocol(t *testing.T) {
	// Start a mock backend
	backendLn, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer backendLn.Close()

	backendGotHeader := make(chan *ProxyHeader, 1)

	go func() {
		conn, err := backendLn.Accept()
		if err != nil {
			return
		}
		defer conn.Close()

		br := bufio.NewReaderSize(conn, 512)
		ph, _ := detectProxyProtocol(br)
		backendGotHeader <- ph
	}()

	// Start the TCP proxy
	proxyLn, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer proxyLn.Close()

	go func() {
		conn, err := proxyLn.Accept()
		if err != nil {
			return
		}
		handleConnection(conn, backendLn.Addr().String())
	}()

	// Connect and send a v1 PROXY protocol header (as Minehut would)
	clientConn, err := net.DialTimeout("tcp", proxyLn.Addr().String(), 2*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer clientConn.Close()

	// Write a v1 header pretending to be from 1.2.3.4
	fmt.Fprintf(clientConn, "PROXY TCP4 1.2.3.4 10.0.0.1 11111 25565\r\n")
	clientConn.Write([]byte("MC_DATA"))
	clientConn.(*net.TCPConn).CloseWrite()

	// The backend should receive the same v1 header with the original IP
	select {
	case ph := <-backendGotHeader:
		if ph == nil {
			t.Fatal("backend did not receive PROXY protocol header")
		}
		if ph.Version != 1 {
			t.Fatalf("expected v1 header passthrough, got v%d", ph.Version)
		}
		if ph.SrcAddr.String() != "1.2.3.4" {
			t.Fatalf("expected src addr 1.2.3.4, got %s", ph.SrcAddr)
		}
		if ph.SrcPort != 11111 {
			t.Fatalf("expected src port 11111, got %d", ph.SrcPort)
		}
		t.Logf("Backend correctly received passthrough v1 header: src=%s:%d", ph.SrcAddr, ph.SrcPort)
	case <-time.After(3 * time.Second):
		t.Fatal("timeout")
	}
}

// Suppress test log noise
func init() {
	// Comment this out if you want to see log output during tests
	// log.SetOutput(io.Discard)
}

var _ = strings.Contains // suppress unused import warning
