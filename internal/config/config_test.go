package config

import (
	"strings"
	"testing"
)

func env(m map[string]string) func(string) string {
	return func(k string) string { return m[k] }
}

func TestLoadServe(t *testing.T) {
	c, err := LoadServe(env(map[string]string{"DATABASE_URL": "postgres://x"}))
	if err != nil {
		t.Fatalf("LoadServe() error = %v", err)
	}
	if c.HTTPAddr != ":8080" || c.LogLevel != "info" {
		t.Errorf("defaults not applied: %+v", c)
	}

	_, err = LoadServe(env(nil))
	if err == nil || !strings.Contains(err.Error(), "DATABASE_URL") {
		t.Errorf("LoadServe(empty) error = %v, want one naming DATABASE_URL", err)
	}
}

func TestLoadMigrateNamesMissingVariable(t *testing.T) {
	_, err := LoadMigrate(env(map[string]string{"MIGRATE_DATABASE_URL": "  "}))
	if err == nil || !strings.Contains(err.Error(), "MIGRATE_DATABASE_URL") {
		t.Errorf("LoadMigrate(blank) error = %v, want one naming MIGRATE_DATABASE_URL", err)
	}
}
