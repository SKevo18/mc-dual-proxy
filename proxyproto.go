package main

import (
	"bufio"
	"bytes"
	"encoding/binary"
	"fmt"
	"net"
	"strings"
)

// PROXY protocol v2 12-byte signature
var proxyV2Sig = []byte{0x0D, 0x0A, 0x0D, 0x0A, 0x00, 0x0D, 0x0A, 0x51, 0x55, 0x49, 0x54, 0x0A}

// proxyV1Prefix is the ASCII prefix for PROXY protocol v1
var proxyV1Prefix = []byte("PROXY ")

// ProxyHeader represents a parsed PROXY protocol header.
type ProxyHeader struct {
	Version  int    // 1 or 2
	SrcAddr  net.IP
	DstAddr  net.IP
	SrcPort  uint16
	DstPort  uint16
	RawBytes []byte // The complete raw header bytes (for passthrough)
}

// detectProxyProtocol peeks at the buffered reader to detect if a PROXY
// protocol header is present. Returns the parsed header and consumes
// the header bytes from the reader. If no header is detected, returns nil
// and no bytes are consumed.
func detectProxyProtocol(br *bufio.Reader) (*ProxyHeader, error) {
	// We need at least 16 bytes to detect v2, or 6 bytes to detect v1.
	// Peek at 16 bytes (the v2 minimum header size).
	peek, err := br.Peek(16)
	if err != nil {
		// If we can't peek 16 bytes, try 6 for v1
		peek, err = br.Peek(6)
		if err != nil {
			return nil, nil // Not enough data, treat as no proxy protocol
		}
	}

	// Check for v2 signature (need at least 16 bytes)
	if len(peek) >= 16 && bytes.Equal(peek[:12], proxyV2Sig) {
		return parseProxyV2(br)
	}

	// Check for v1 prefix
	if len(peek) >= 6 && bytes.Equal(peek[:6], proxyV1Prefix) {
		return parseProxyV1(br)
	}

	return nil, nil
}

// parseProxyV1 parses a PROXY protocol v1 header from the reader.
// Format: "PROXY TCP4 <src> <dst> <srcport> <dstport>\r\n"
func parseProxyV1(br *bufio.Reader) (*ProxyHeader, error) {
	// Read until \r\n (the v1 header is a single line)
	line, err := br.ReadBytes('\n')
	if err != nil {
		return nil, fmt.Errorf("proxy v1: failed to read header line: %w", err)
	}

	// Must end with \r\n
	if len(line) < 2 || line[len(line)-2] != '\r' {
		return nil, fmt.Errorf("proxy v1: header does not end with CRLF")
	}

	header := &ProxyHeader{
		Version:  1,
		RawBytes: line,
	}

	// Parse the header: "PROXY TCP4 src dst srcport dstport"
	str := strings.TrimRight(string(line), "\r\n")
	parts := strings.Split(str, " ")

	if len(parts) == 2 && parts[1] == "UNKNOWN" {
		// PROXY UNKNOWN\r\n - no address info
		return header, nil
	}

	if len(parts) != 6 {
		return nil, fmt.Errorf("proxy v1: expected 6 fields, got %d", len(parts))
	}

	header.SrcAddr = net.ParseIP(parts[2])
	header.DstAddr = net.ParseIP(parts[3])

	srcPort := 0
	dstPort := 0
	fmt.Sscanf(parts[4], "%d", &srcPort)
	fmt.Sscanf(parts[5], "%d", &dstPort)
	header.SrcPort = uint16(srcPort)
	header.DstPort = uint16(dstPort)

	return header, nil
}

// parseProxyV2 parses a PROXY protocol v2 header from the reader.
func parseProxyV2(br *bufio.Reader) (*ProxyHeader, error) {
	// Read the fixed 16-byte header
	fixedHeader := make([]byte, 16)
	if _, err := readFull(br, fixedHeader); err != nil {
		return nil, fmt.Errorf("proxy v2: failed to read fixed header: %w", err)
	}

	// Byte 12: version (upper nibble) | command (lower nibble)
	verCmd := fixedHeader[12]
	ver := verCmd >> 4
	if ver != 2 {
		return nil, fmt.Errorf("proxy v2: unexpected version %d", ver)
	}

	// Byte 13: address family (upper nibble) | transport protocol (lower nibble)
	famProto := fixedHeader[13]
	addrFamily := famProto >> 4
	// transport := famProto & 0x0F

	// Bytes 14-15: length of the address section (big-endian)
	addrLen := binary.BigEndian.Uint16(fixedHeader[14:16])

	// Read the address block
	addrBlock := make([]byte, addrLen)
	if addrLen > 0 {
		if _, err := readFull(br, addrBlock); err != nil {
			return nil, fmt.Errorf("proxy v2: failed to read address block: %w", err)
		}
	}

	// Combine into raw bytes
	rawBytes := make([]byte, 0, 16+int(addrLen))
	rawBytes = append(rawBytes, fixedHeader...)
	rawBytes = append(rawBytes, addrBlock...)

	header := &ProxyHeader{
		Version:  2,
		RawBytes: rawBytes,
	}

	// Parse addresses based on family
	switch addrFamily {
	case 0x1: // AF_INET (IPv4): 4+4+2+2 = 12 bytes
		if addrLen >= 12 {
			header.SrcAddr = net.IP(addrBlock[0:4])
			header.DstAddr = net.IP(addrBlock[4:8])
			header.SrcPort = binary.BigEndian.Uint16(addrBlock[8:10])
			header.DstPort = binary.BigEndian.Uint16(addrBlock[10:12])
		}
	case 0x2: // AF_INET6: 16+16+2+2 = 36 bytes
		if addrLen >= 36 {
			header.SrcAddr = net.IP(addrBlock[0:16])
			header.DstAddr = net.IP(addrBlock[16:32])
			header.SrcPort = binary.BigEndian.Uint16(addrBlock[32:34])
			header.DstPort = binary.BigEndian.Uint16(addrBlock[34:36])
		}
	}

	return header, nil
}

// buildProxyV2Header generates a PROXY protocol v2 header for a TCP connection.
// This is used for direct connections that don't come with a PROXY protocol header.
func buildProxyV2Header(srcAddr, dstAddr net.Addr) []byte {
	srcTCP, srcOk := srcAddr.(*net.TCPAddr)
	dstTCP, dstOk := dstAddr.(*net.TCPAddr)

	if !srcOk || !dstOk {
		// Can't determine addresses, send a LOCAL command (no address info)
		header := make([]byte, 16)
		copy(header[0:12], proxyV2Sig)
		header[12] = 0x20 // version 2, LOCAL command
		header[13] = 0x00 // AF_UNSPEC, UNSPEC
		binary.BigEndian.PutUint16(header[14:16], 0)
		return header
	}

	srcIP := srcTCP.IP
	dstIP := dstTCP.IP

	// Determine if we're dealing with IPv4 or IPv6
	srcIPv4 := srcIP.To4()
	dstIPv4 := dstIP.To4()

	var header []byte

	if srcIPv4 != nil && dstIPv4 != nil {
		// IPv4: AF_INET (0x1), STREAM/TCP (0x1)
		// Address block: 4 + 4 + 2 + 2 = 12 bytes
		header = make([]byte, 16+12)
		copy(header[0:12], proxyV2Sig)
		header[12] = 0x21 // version 2, PROXY command
		header[13] = 0x11 // AF_INET, STREAM
		binary.BigEndian.PutUint16(header[14:16], 12) // address length

		copy(header[16:20], srcIPv4)
		copy(header[20:24], dstIPv4)
		binary.BigEndian.PutUint16(header[24:26], uint16(srcTCP.Port))
		binary.BigEndian.PutUint16(header[26:28], uint16(dstTCP.Port))
	} else {
		// IPv6: AF_INET6 (0x2), STREAM/TCP (0x1)
		// Address block: 16 + 16 + 2 + 2 = 36 bytes
		srcIPv6 := srcIP.To16()
		dstIPv6 := dstIP.To16()

		header = make([]byte, 16+36)
		copy(header[0:12], proxyV2Sig)
		header[12] = 0x21 // version 2, PROXY command
		header[13] = 0x21 // AF_INET6, STREAM
		binary.BigEndian.PutUint16(header[14:16], 36) // address length

		copy(header[16:32], srcIPv6)
		copy(header[32:48], dstIPv6)
		binary.BigEndian.PutUint16(header[48:50], uint16(srcTCP.Port))
		binary.BigEndian.PutUint16(header[50:52], uint16(dstTCP.Port))
	}

	return header
}

// readFull reads exactly len(buf) bytes from the reader.
func readFull(br *bufio.Reader, buf []byte) (int, error) {
	n := 0
	for n < len(buf) {
		nn, err := br.Read(buf[n:])
		n += nn
		if err != nil {
			return n, err
		}
	}
	return n, nil
}
