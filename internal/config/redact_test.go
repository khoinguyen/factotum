package config

import "testing"

func TestIsSecretKey(t *testing.T) {
	secret := []string{"secret_api_key", "api_key", "apikey", "API_KEY", "password", "db_password", "client_secret"}
	for _, key := range secret {
		if !IsSecretKey(key) {
			t.Errorf("IsSecretKey(%q) = false, want true", key)
		}
	}
	open := []string{"model", "base_url", "backend", "provider", "retries"}
	for _, key := range open {
		if IsSecretKey(key) {
			t.Errorf("IsSecretKey(%q) = true, want false", key)
		}
	}
}

func TestRedactAllMasksSecrets(t *testing.T) {
	got := RedactAll(map[string]string{"secret_api_key": "sk-1", "model": "jev-latest", "password": "hunter2"})
	if got["secret_api_key"] != "[redacted]" {
		t.Errorf("secret_api_key = %q, want [redacted]", got["secret_api_key"])
	}
	if got["password"] != "[redacted]" {
		t.Errorf("password = %q, want [redacted]", got["password"])
	}
	if got["model"] != "jev-latest" {
		t.Errorf("model = %q, want jev-latest", got["model"])
	}
}
