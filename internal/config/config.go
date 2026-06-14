package config

import (
	"os"
	"strconv"
	"time"
)

type Config struct {
	Port           string
	AdminUsername   string
	AdminPassword   string
	JWTSecret       string
	RefreshInterval time.Duration
	CacheMaxAge     time.Duration
	DataSourcePath  string
	CacheDir        string
	DefaultUA       string
}

func Load() *Config {
	return &Config{
		Port:           getEnv("PORT", "8080"),
		AdminUsername:   getEnv("ADMIN_USERNAME", "admin"),
		AdminPassword:   getEnv("ADMIN_PASSWORD", "admin123"),
		JWTSecret:       getEnv("JWT_SECRET", "tvbox-merger-secret-key-change-me"),
		RefreshInterval: getDurationEnv("REFRESH_INTERVAL", 6*time.Hour),
		CacheMaxAge:     getDurationEnv("CACHE_MAX_AGE", 24*time.Hour),
		DataSourcePath:  getEnv("DATA_SOURCE", "data/tvbox.db"),
		CacheDir:        getEnv("CACHE_DIR", "data/cache"),
		DefaultUA:       getEnv("DEFAULT_UA", "okhttp/4.1.0"),
	}
}

func getEnv(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

func getDurationEnv(key string, fallback time.Duration) time.Duration {
	if v := os.Getenv(key); v != "" {
		if d, err := time.ParseDuration(v); err == nil {
			return d
		}
	}
	return fallback
}

func getIntEnv(key string, fallback int) int {
	if v := os.Getenv(key); v != "" {
		if i, err := strconv.Atoi(v); err == nil {
			return i
		}
	}
	return fallback
}
