package config

import "strings"

// secretMarkers mark a configuration key as sensitive. Any key containing one is
// redacted wherever configuration is surfaced, so a secret cannot leak into output or
// logs. Matching is case-insensitive.
var secretMarkers = []string{"secret", "api_key", "apikey", "password"}

// IsSecretKey reports whether a config key names a secret.
func IsSecretKey(key string) bool {
	lower := strings.ToLower(key)
	for _, marker := range secretMarkers {
		if strings.Contains(lower, marker) {
			return true
		}
	}
	return false
}

// Redact masks a secret value and leaves non-secret values unchanged.
func Redact(key, value string) string {
	if value != "" && IsSecretKey(key) {
		return "[redacted]"
	}
	return value
}

// RedactAll masks every secret value in a config map.
func RedactAll(values map[string]string) map[string]string {
	out := make(map[string]string, len(values))
	for key, value := range values {
		out[key] = Redact(key, value)
	}
	return out
}
