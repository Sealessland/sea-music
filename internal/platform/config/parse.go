package config

import (
	"fmt"
	"strconv"
	"strings"
	"time"
)

func splitNonEmpty(value string) []string {
	var result []string
	for _, item := range strings.Split(value, ",") {
		if trimmed := strings.TrimSpace(item); trimmed != "" {
			result = append(result, trimmed)
		}
	}
	return result
}

func parsePositiveInt64(lookup LookupEnv, key string, target *int64) error {
	raw, ok := lookup(key)
	if !ok {
		return nil
	}
	value, err := strconv.ParseInt(raw, 10, 64)
	if err != nil || value <= 0 {
		return fmt.Errorf("%s: must be a positive integer", key)
	}
	*target = value
	return nil
}

func parsePositiveFloat(lookup LookupEnv, key string, target *float64) error {
	raw, ok := lookup(key)
	if !ok {
		return nil
	}
	value, err := strconv.ParseFloat(raw, 64)
	if err != nil || value <= 0 {
		return fmt.Errorf("%s: must be a positive number", key)
	}
	*target = value
	return nil
}

func parsePositiveInt(lookup LookupEnv, key string, target *int) error {
	raw, ok := lookup(key)
	if !ok {
		return nil
	}
	value, err := strconv.Atoi(raw)
	if err != nil || value <= 0 {
		return fmt.Errorf("%s: must be a positive integer", key)
	}
	*target = value
	return nil
}

func parseBool(lookup LookupEnv, key string, target *bool) error {
	raw, ok := lookup(key)
	if !ok {
		return nil
	}
	value, err := strconv.ParseBool(raw)
	if err != nil {
		return fmt.Errorf("%s: must be a boolean", key)
	}
	*target = value
	return nil
}

func valueOrDefault(lookup LookupEnv, key, fallback string) string {
	if value, ok := lookup(key); ok {
		return value
	}
	return fallback
}

func parsePositiveDuration(lookup LookupEnv, key string, target *time.Duration) error {
	raw, ok := lookup(key)
	if !ok {
		return nil
	}
	value, err := time.ParseDuration(raw)
	if err != nil {
		return fmt.Errorf("%s: parse duration: %w", key, err)
	}
	if value <= 0 {
		return fmt.Errorf("%s: duration must be positive", key)
	}
	*target = value
	return nil
}
