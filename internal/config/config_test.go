package config

import (
	"strings"
	"testing"
	"time"
)

func env(m map[string]string) func(string) string {
	return func(k string) string { return m[k] }
}

func TestLoadServe(t *testing.T) {
	required := map[string]string{
		"DATABASE_URL":     "postgres://x",
		"VAULT_ENDPOINT":   "http://localhost:9000",
		"VAULT_ACCESS_KEY": "a",
		"VAULT_SECRET_KEY": "s",
	}
	c, err := LoadServe(env(required))
	if err != nil {
		t.Fatalf("LoadServe() error = %v", err)
	}
	if c.HTTPAddr != ":8080" || c.LogLevel != "info" || c.UploadURLTTL != 15*time.Minute || c.ProviderTimeout != 5*time.Second ||
		c.Vault.Bucket != "evidence" || c.Vault.Region != "us-east-1" {
		t.Errorf("defaults not applied: %+v", c)
	}

	_, err = LoadServe(env(nil))
	for k := range required {
		if err == nil || !strings.Contains(err.Error(), k) {
			t.Errorf("LoadServe(empty) error = %v, want one naming %s", err, k)
		}
	}

	if c.Outbox.PollInterval != time.Second || c.Outbox.MaxAttempts != 10 {
		t.Errorf("outbox defaults not applied: %+v", c.Outbox)
	}

	for key, value := range map[string]string{
		"UPLOAD_URL_TTL":       "soon",
		"OUTBOX_POLL_INTERVAL": "-1s",
		"OUTBOX_MAX_ATTEMPTS":  "zero",
	} {
		bad := map[string]string{key: value}
		for k, v := range required {
			bad[k] = v
		}
		if _, err := LoadServe(env(bad)); err == nil || !strings.Contains(err.Error(), key) {
			t.Errorf("LoadServe(%s=%q) error = %v, want one naming %s", key, value, err, key)
		}
	}
}

func TestLoadMigrateNamesMissingVariable(t *testing.T) {
	_, err := LoadMigrate(env(map[string]string{"MIGRATE_DATABASE_URL": "  "}))
	if err == nil || !strings.Contains(err.Error(), "MIGRATE_DATABASE_URL") {
		t.Errorf("LoadMigrate(blank) error = %v, want one naming MIGRATE_DATABASE_URL", err)
	}
}
