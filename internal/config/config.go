package config

import (
	"os"
	"strconv"
)

// Config holds all service configuration loaded from environment variables.
type Config struct {
	KafkaBrokers       string
	KafkaTopic         string
	RedisAddr          string
	PostgresDSN        string
	APIPort            string
	GatewayPort        string
	RateLimitPerMinute int
}

func getEnv(key, defaultVal string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return defaultVal
}

func getEnvInt(key string, defaultVal int) int {
	if v := os.Getenv(key); v != "" {
		if i, err := strconv.Atoi(v); err == nil {
			return i
		}
	}
	return defaultVal
}

// Load returns a Config populated from environment variables with sensible defaults.
func Load() *Config {
	return &Config{
		KafkaBrokers:       getEnv("KAFKA_BROKERS", "kafka:9092"),
		KafkaTopic:         getEnv("KAFKA_TOPIC", "notification_requests"),
		RedisAddr:          getEnv("REDIS_ADDR", "redis:6379"),
		PostgresDSN:        getEnv("POSTGRES_DSN", "postgres://notifications:notifications@postgres:5432/notifications?sslmode=disable"),
		APIPort:            getEnv("API_PORT", "8080"),
		GatewayPort:        getEnv("GATEWAY_PORT", "8081"),
		RateLimitPerMinute: getEnvInt("RATE_LIMIT_PER_MINUTE", 20),
	}
}
