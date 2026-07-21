package config

import (
	"fmt"
	"strconv"
	"strings"
	"time"
)

// splitNonEmpty splits a comma-separated string, trims surrounding whitespace from each item, and omits empty items.
func splitNonEmpty(value string) []string {
	var result []string
	for _, item := range strings.Split(value, ",") {
		if trimmed := strings.TrimSpace(item); trimmed != "" {
			result = append(result, trimmed)
		}
	}
	return result
}

// parsePositiveInt64 assigns key's base-10 int64 value to target when present and positive, leaving target unchanged if absent or invalid and returning an error for invalid values.
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

// parsePositiveFloat assigns key's float64 value to target when present and positive, leaving target unchanged if absent or invalid and returning an error for invalid values.
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

// parsePositiveInt assigns key's decimal int value to target when present and positive, leaving target unchanged if absent or invalid and returning an error for invalid values.
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

// parseBool assigns key's boolean value to target when present and valid, leaving target unchanged if absent or invalid and returning an error for invalid values.
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

// valueOrDefault returns key's value when present, including an empty value, and otherwise returns fallback.
func valueOrDefault(lookup LookupEnv, key, fallback string) string {
	if value, ok := lookup(key); ok {
		return value
	}
	return fallback
}

// parsePositiveDuration assigns key's parsed duration to target when present and positive, leaving target unchanged if absent or invalid and returning an error for parse failures or nonpositive values.
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
