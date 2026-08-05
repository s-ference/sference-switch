// Package dnsbypass resolves hostnames via a raw DNS query that skips
// /etc/hosts entirely. Go's net.Resolver, even with PreferGo and a custom
// Dial, still consults /etc/hosts before DNS — so when the transparent
// interception hosts entry points api.anthropic.com at 127.0.0.1, the
// resolver returns 127.0.0.1 and any passthrough call loops back into the
// door. A raw UDP exchange with 1.1.1.1 is the only way to get the real
// address.
package dnsbypass

import (
	"context"
	"fmt"
	"net"
	"strings"
	"time"
)

// ResolveHost does a raw A-record query for hostname via 1.1.1.1,
// bypassing /etc/hosts. Returns the first IPv4 address found.
func ResolveHost(ctx context.Context, hostname string) (string, error) {
	query := buildQuery(hostname)
	d := net.Dialer{Timeout: 5 * time.Second}
	conn, err := d.DialContext(ctx, "udp", "1.1.1.1:53")
	if err != nil {
		return "", fmt.Errorf("dial 1.1.1.1:53: %w", err)
	}
	defer conn.Close()
	conn.SetDeadline(time.Now().Add(5 * time.Second))
	if _, err := conn.Write(query); err != nil {
		return "", fmt.Errorf("write DNS query: %w", err)
	}
	resp := make([]byte, 512)
	n, err := conn.Read(resp)
	if err != nil {
		return "", fmt.Errorf("read DNS response: %w", err)
	}
	return parseResponse(resp[:n])
}

// buildQuery constructs a minimal recursive DNS query for the A record
// of hostname.
func buildQuery(hostname string) []byte {
	query := []byte{
		0x00, 0x00, // ID
		0x01, 0x00, // flags: standard query, recursion desired
		0x00, 0x01, // 1 question
		0x00, 0x00, // 0 answer
		0x00, 0x00, // 0 authority
		0x00, 0x00, // 0 additional
	}
	for _, label := range strings.Split(hostname, ".") {
		query = append(query, byte(len(label)))
		query = append(query, []byte(label)...)
	}
	query = append(query, 0)          // root label
	query = append(query, 0x00, 0x01)  // QTYPE: A
	query = append(query, 0x00, 0x01) // QCLASS: IN
	return query
}

// parseResponse extracts the first A record from a DNS response.
func parseResponse(resp []byte) (string, error) {
	n := len(resp)
	if n < 12 {
		return "", fmt.Errorf("DNS response too short")
	}
	ancount := int(resp[6])<<8 | int(resp[7])
	if ancount == 0 {
		return "", fmt.Errorf("no answers in DNS response")
	}
	pos := 12 // after header
	// Skip question section (QNAME + null + QTYPE + QCLASS)
	for pos < n && resp[pos] != 0 {
		pos += 1 + int(resp[pos])
	}
	pos += 5 // null label + QTYPE(2) + QCLASS(2)
	for i := 0; i < ancount && pos+12 <= n; i++ {
		// Skip NAME (may be compressed: 0xC0 + offset, or full labels)
		if resp[pos]&0xC0 == 0xC0 {
			pos += 2
		} else {
			for pos < n && resp[pos] != 0 {
				pos += 1 + int(resp[pos])
			}
			pos++ // null
		}
		if pos+10 > n {
			break
		}
		rtype := int(resp[pos])<<8 | int(resp[pos+1])
		rdlen := int(resp[pos+8])<<8 | int(resp[pos+9])
		pos += 10
		if rtype == 1 && rdlen == 4 && pos+4 <= n {
			return fmt.Sprintf("%d.%d.%d.%d", resp[pos], resp[pos+1], resp[pos+2], resp[pos+3]), nil
		}
		pos += rdlen
	}
	return "", fmt.Errorf("no A record in DNS response")
}
