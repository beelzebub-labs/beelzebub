package plugins

import (
	"testing"
	"time"
)

func TestRateLimiting(t *testing.T) {
	config := LLMHoneypot{
		RateLimitEnabled:       true,
		RateLimitRequests:      2,
		RateLimitWindowSeconds: 1,
	}

	honeypot := InitLLMHoneypot(config)
	clientIP := "192.168.1.1"

	if err := honeypot.checkRateLimit(clientIP); err != nil {
		t.Errorf("First request should not be rate limited: %v", err)
	}

	if err := honeypot.checkRateLimit(clientIP); err != nil {
		t.Errorf("Second request should not be rate limited: %v", err)
	}

	if err := honeypot.checkRateLimit(clientIP); err == nil {
		t.Error("Third request should be rate limited")
	}

	time.Sleep(1 * time.Second)

	if err := honeypot.checkRateLimit(clientIP); err != nil {
		t.Errorf("Request after window should not be rate limited: %v", err)
	}
}

func TestRateLimitingDisabled(t *testing.T) {
	config := LLMHoneypot{
		RateLimitEnabled: false,
	}

	honeypot := InitLLMHoneypot(config)

	for i := 0; i < 100; i++ {
		if err := honeypot.checkRateLimit("192.168.1.1"); err != nil {
			t.Errorf("Request %d should not be rate limited when disabled: %v", i, err)
		}
	}
}

func TestRateLimitingPerIP(t *testing.T) {
	config := LLMHoneypot{
		RateLimitEnabled:       true,
		RateLimitRequests:      1,
		RateLimitWindowSeconds: 1,
	}

	honeypot := InitLLMHoneypot(config)
	clientIP1 := "192.168.1.1"
	clientIP2 := "192.168.1.2"

	if err := honeypot.checkRateLimit(clientIP1); err != nil {
		t.Errorf("First request from IP1 should not be rate limited: %v", err)
	}

	if err := honeypot.checkRateLimit(clientIP1); err == nil {
		t.Error("Second request from IP1 should be rate limited")
	}

	if err := honeypot.checkRateLimit(clientIP2); err != nil {
		t.Errorf("First request from IP2 should not be rate limited: %v", err)
	}

	if err := honeypot.checkRateLimit(clientIP2); err == nil {
		t.Error("Second request from IP2 should be rate limited")
	}
}

func TestRateLimitingInvalidConfigAndEmptyIP(t *testing.T) {
	globalRateLimiterMutex.Lock()
	delete(globalRateLimiters, "unknown")
	globalRateLimiterMutex.Unlock()

	invalid := InitLLMHoneypot(LLMHoneypot{
		RateLimitEnabled:       true,
		RateLimitRequests:      0,
		RateLimitWindowSeconds: 1,
	})
	if err := invalid.checkRateLimit("invalid-config"); err != nil {
		t.Fatalf("invalid rate-limit config returned error: %v", err)
	}

	honeypot := InitLLMHoneypot(LLMHoneypot{
		RateLimitEnabled:       true,
		RateLimitRequests:      1,
		RateLimitWindowSeconds: 60,
	})
	if err := honeypot.checkRateLimit(""); err != nil {
		t.Fatalf("first unknown-IP request returned error: %v", err)
	}
	if err := honeypot.checkRateLimit(""); err == nil {
		t.Fatal("second unknown-IP request should be rate limited")
	}
}
