package dnsbypass

import (
	"context"
	"testing"
	"time"
)

func TestResolveHost(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	ip, err := ResolveHost(ctx, "api.anthropic.com")
	if err != nil {
		t.Skipf("DNS unavailable: %v", err)
	}
	if ip == "" {
		t.Fatal("empty IP")
	}
	t.Logf("api.anthropic.com -> %s", ip)
}

func TestResolveHostSkipsHostsFile(t *testing.T) {
	// api.anthropic.com is in /etc/hosts pointing at 127.0.0.1 when
	// intercept is on. ResolveHost must return the real IP, not 127.0.0.1.
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	ip, err := ResolveHost(ctx, "api.anthropic.com")
	if err != nil {
		t.Skipf("DNS unavailable: %v", err)
	}
	if ip == "127.0.0.1" {
		t.Fatal("returned 127.0.0.1 — /etc/hosts was not bypassed")
	}
}

func TestParseResponse(t *testing.T) {
	// Minimal valid DNS response for api.anthropic.com -> 160.79.104.10
	resp := []byte{
		0x00, 0x00, 0x81, 0x80, // header: ID=0, standard response, no error
		0x00, 0x01, 0x00, 0x01, // 1 question, 1 answer
		0x00, 0x00, 0x00, 0x00, // 0 authority, 0 additional
		// QNAME: api.anthropic.com
		3, 'a', 'p', 'i', 9, 'a', 'n', 't', 'h', 'r', 'o', 'p', 'i', 'c', 3, 'c', 'o', 'm', 0,
		0x00, 0x01, 0x00, 0x01, // QTYPE=A, QCLASS=IN
		// Answer: compressed name pointer to offset 12
		0xC0, 0x0C,
		0x00, 0x01, // TYPE=A
		0x00, 0x01, // CLASS=IN
		0x00, 0x00, 0x00, 0x3C, // TTL=60
		0x00, 0x04, // RDLENGTH=4
		160, 79, 104, 10, // 160.79.104.10
	}
	ip, err := parseResponse(resp)
	if err != nil {
		t.Fatalf("parseResponse: %v", err)
	}
	if ip != "160.79.104.10" {
		t.Fatalf("ip = %q, want 160.79.104.10", ip)
	}
}

func TestParseResponseNoAnswers(t *testing.T) {
	resp := []byte{
		0x00, 0x00, 0x81, 0x80,
		0x00, 0x01, 0x00, 0x00, // 1 question, 0 answers
		0x00, 0x00, 0x00, 0x00,
		3, 'a', 'p', 'i', 9, 'a', 'n', 't', 'h', 'r', 'o', 'p', 'i', 'c', 3, 'c', 'o', 'm', 0,
		0x00, 0x01, 0x00, 0x01,
	}
	_, err := parseResponse(resp)
	if err == nil {
		t.Fatal("expected error for no answers")
	}
}

func TestParseResponseTooShort(t *testing.T) {
	_, err := parseResponse([]byte{0x00, 0x01})
	if err == nil {
		t.Fatal("expected error for short response")
	}
}
