package config

import (
	"os"
	"testing"
)

func TestLoad_Defaults(t *testing.T) {
	// Clear any env overrides that may be set in CI
	for _, key := range []string{
		"KAFKA_BROKERS", "KAFKA_TOPIC", "REDIS_ADDR", "POSTGRES_DSN",
		"API_PORT", "GATEWAY_PORT", "RATE_LIMIT_PER_MINUTE",
	} {
		os.Unsetenv(key)
	}

	cfg := Load()

	if cfg.KafkaBrokers != "kafka:9092" {
		t.Errorf("KafkaBrokers: got %q, want %q", cfg.KafkaBrokers, "kafka:9092")
	}
	if cfg.KafkaTopic != "notification_requests" {
		t.Errorf("KafkaTopic: got %q, want %q", cfg.KafkaTopic, "notification_requests")
	}
	if cfg.RedisAddr != "redis:6379" {
		t.Errorf("RedisAddr: got %q, want %q", cfg.RedisAddr, "redis:6379")
	}
	if cfg.APIPort != "8080" {
		t.Errorf("APIPort: got %q, want %q", cfg.APIPort, "8080")
	}
	if cfg.GatewayPort != "8081" {
		t.Errorf("GatewayPort: got %q, want %q", cfg.GatewayPort, "8081")
	}
	if cfg.RateLimitPerMinute != 20 {
		t.Errorf("RateLimitPerMinute: got %d, want %d", cfg.RateLimitPerMinute, 20)
	}
}

func TestLoad_EnvOverrides(t *testing.T) {
	t.Setenv("KAFKA_BROKERS", "broker1:9092,broker2:9092")
	t.Setenv("KAFKA_TOPIC", "my_topic")
	t.Setenv("REDIS_ADDR", "localhost:6380")
	t.Setenv("API_PORT", "9090")
	t.Setenv("GATEWAY_PORT", "9091")
	t.Setenv("RATE_LIMIT_PER_MINUTE", "50")

	cfg := Load()

	if cfg.KafkaBrokers != "broker1:9092,broker2:9092" {
		t.Errorf("KafkaBrokers: got %q", cfg.KafkaBrokers)
	}
	if cfg.KafkaTopic != "my_topic" {
		t.Errorf("KafkaTopic: got %q", cfg.KafkaTopic)
	}
	if cfg.RedisAddr != "localhost:6380" {
		t.Errorf("RedisAddr: got %q", cfg.RedisAddr)
	}
	if cfg.APIPort != "9090" {
		t.Errorf("APIPort: got %q", cfg.APIPort)
	}
	if cfg.GatewayPort != "9091" {
		t.Errorf("GatewayPort: got %q", cfg.GatewayPort)
	}
	if cfg.RateLimitPerMinute != 50 {
		t.Errorf("RateLimitPerMinute: got %d", cfg.RateLimitPerMinute)
	}
}

func TestLoad_InvalidRateLimit(t *testing.T) {
	t.Setenv("RATE_LIMIT_PER_MINUTE", "not-a-number")

	cfg := Load()

	// Should fall back to default when value is invalid
	if cfg.RateLimitPerMinute != 20 {
		t.Errorf("RateLimitPerMinute should use default on invalid value, got %d", cfg.RateLimitPerMinute)
	}
}
