package api

import (
	"os"
	"strconv"
	"strings"
)

func getEnvInt(key string, fallback int) int {
	if value, ok := os.LookupEnv(key); ok {
		if i, err := strconv.Atoi(value); err == nil && i > 0 {
			return i
		}
	}
	return fallback
}

func getEnvInt64(key string, fallback int64) int64 {
	if value, ok := os.LookupEnv(key); ok {
		if i, err := strconv.ParseInt(value, 10, 64); err == nil && i > 0 {
			return i
		}
	}
	return fallback
}

func getEnvString(key string, fallback string) string {
	if value, ok := os.LookupEnv(key); ok {
		if v := strings.TrimSpace(value); v != "" {
			return v
		}
	}
	return fallback
}
