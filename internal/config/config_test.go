package config

import (
	"fmt"
	"log/slog"
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
	if c.HTTPAddr != ":8080" || c.LogLevel != "info" || c.UploadURLTTL != 15*time.Minute || c.ProviderTimeout != 5*time.Second || c.EvidenceURLTTL != time.Minute ||
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
	if c.UploadSweepInterval != time.Minute {
		t.Errorf("UploadSweepInterval = %v, want 1m", c.UploadSweepInterval)
	}

	for key, value := range map[string]string{
		"UPLOAD_URL_TTL":        "soon",
		"OUTBOX_POLL_INTERVAL":  "-1s",
		"OUTBOX_MAX_ATTEMPTS":   "zero",
		"UPLOAD_SWEEP_INTERVAL": "0s",
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

func TestLoadServeWebhook(t *testing.T) {
	base := map[string]string{
		"DATABASE_URL": "postgres://x", "VAULT_ENDPOINT": "http://localhost:9000",
		"VAULT_ACCESS_KEY": "a", "VAULT_SECRET_KEY": "s",
	}
	with := func(extra map[string]string) map[string]string {
		m := map[string]string{}
		for k, v := range base {
			m[k] = v
		}
		for k, v := range extra {
			m[k] = v
		}
		return m
	}
	const goodSecret = "0123456789abcdef0123456789abcdef"

	c, err := LoadServe(env(base))
	if err != nil || c.Webhook.URL != "" {
		t.Errorf("no WEBHOOK_URL: %+v, %v; want webhook disabled and no error", c.Webhook, err)
	}

	c, err = LoadServe(env(with(map[string]string{"WEBHOOK_URL": "http://localhost:8090/webhooks", "WEBHOOK_SECRET": goodSecret})))
	if err != nil || c.Webhook.URL != "http://localhost:8090/webhooks" || string(c.Webhook.Secret.Reveal()) != goodSecret {
		t.Errorf("valid webhook config: %v", err)
	}

	for name, extra := range map[string]map[string]string{
		"WEBHOOK_SECRET": {"WEBHOOK_URL": "http://localhost:8090/webhooks"},
		"at least 32":    {"WEBHOOK_URL": "http://localhost:8090/webhooks", "WEBHOOK_SECRET": "short"},
		"WEBHOOK_URL (":  {"WEBHOOK_URL": "localhost:8090", "WEBHOOK_SECRET": goodSecret},
	} {
		if _, err := LoadServe(env(with(extra))); err == nil || !strings.Contains(err.Error(), name) {
			t.Errorf("%v: error = %v, want one mentioning %q", extra, err, name)
		}
	}
}

func TestSecretNeverPrints(t *testing.T) {
	s := Secret("super-secret-value")
	var buf strings.Builder
	slog.New(slog.NewTextHandler(&buf, nil)).Info("x", slog.Any("secret", s), slog.Any("webhook", Webhook{URL: "u", Secret: s}))
	for _, out := range []string{fmt.Sprint(s), fmt.Sprintf("%v %+v %#v %s", s, Webhook{Secret: s}, s, s), buf.String()} {
		if strings.Contains(out, "super-secret-value") {
			t.Errorf("secret leaked: %s", out)
		}
	}
}
