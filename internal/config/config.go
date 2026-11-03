// Package config reads configuration from environment variables and fails
// fast, naming every missing variable, rather than starting half-configured.
package config

import (
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"
)

// Serve is the configuration for `server serve`.
type Serve struct {
	DatabaseURL     string // runtime connection as the non-owner app role
	HTTPAddr        string
	LogLevel        string
	Vault           Vault
	UploadURLTTL    time.Duration
	ProviderTimeout time.Duration
	EvidenceURLTTL  time.Duration
	Outbox          Outbox
	// UploadSweepInterval is how often lapsed Upload Intents are expired.
	UploadSweepInterval time.Duration
}

// Outbox tunes notification delivery.
type Outbox struct {
	PollInterval time.Duration
	MaxAttempts  int
}

// Vault locates the Evidence Vault.
type Vault struct {
	// Endpoint must be the address clients upload to: presigned URLs are
	// signed for this host.
	Endpoint  string
	Region    string
	AccessKey string
	SecretKey string
	Bucket    string
}

// Migrate is the configuration for `server migrate`.
type Migrate struct {
	DatabaseURL string // connection as the owner role
	LogLevel    string
}

type reader struct {
	getenv  func(string) string
	missing []string
	invalid []string
}

func (r *reader) required(key string) string {
	v := strings.TrimSpace(r.getenv(key))
	if v == "" {
		r.missing = append(r.missing, key)
	}
	return v
}

func (r *reader) optional(key, def string) string {
	if v := strings.TrimSpace(r.getenv(key)); v != "" {
		return v
	}
	return def
}

func (r *reader) duration(key string, def time.Duration) time.Duration {
	v := strings.TrimSpace(r.getenv(key))
	if v == "" {
		return def
	}
	d, err := time.ParseDuration(v)
	if err != nil || d <= 0 {
		r.invalid = append(r.invalid, key+" (want a positive duration such as 15m)")
		return def
	}
	return d
}

func (r *reader) positiveInt(key string, def int) int {
	v := strings.TrimSpace(r.getenv(key))
	if v == "" {
		return def
	}
	n, err := strconv.Atoi(v)
	if err != nil || n <= 0 {
		r.invalid = append(r.invalid, key+" (want a positive integer)")
		return def
	}
	return n
}

func (r *reader) err() error {
	if len(r.invalid) > 0 {
		return fmt.Errorf("invalid environment variables: %s", strings.Join(r.invalid, ", "))
	}
	if len(r.missing) == 0 {
		return nil
	}
	return fmt.Errorf("missing required environment variables: %s (see .env.example)", strings.Join(r.missing, ", "))
}

// LoadServe reads Serve config. getenv is usually os.Getenv.
func LoadServe(getenv func(string) string) (Serve, error) {
	r := &reader{getenv: getenv}
	c := Serve{
		DatabaseURL: r.required("DATABASE_URL"),
		HTTPAddr:    r.optional("HTTP_ADDR", ":8080"),
		LogLevel:    r.optional("LOG_LEVEL", "info"),
		Vault: Vault{
			Endpoint:  r.required("VAULT_ENDPOINT"),
			Region:    r.optional("VAULT_REGION", "us-east-1"),
			AccessKey: r.required("VAULT_ACCESS_KEY"),
			SecretKey: r.required("VAULT_SECRET_KEY"),
			Bucket:    r.optional("VAULT_BUCKET", "evidence"),
		},
		UploadURLTTL:    r.duration("UPLOAD_URL_TTL", 15*time.Minute),
		ProviderTimeout: r.duration("PROVIDER_TIMEOUT", 5*time.Second),
		EvidenceURLTTL:  r.duration("EVIDENCE_URL_TTL", time.Minute),
		Outbox: Outbox{
			PollInterval: r.duration("OUTBOX_POLL_INTERVAL", time.Second),
			MaxAttempts:  r.positiveInt("OUTBOX_MAX_ATTEMPTS", 10),
		},
		UploadSweepInterval: r.duration("UPLOAD_SWEEP_INTERVAL", time.Minute),
	}
	return c, r.err()
}

// LoadMigrate reads Migrate config.
func LoadMigrate(getenv func(string) string) (Migrate, error) {
	r := &reader{getenv: getenv}
	c := Migrate{
		DatabaseURL: r.required("MIGRATE_DATABASE_URL"),
		LogLevel:    r.optional("LOG_LEVEL", "info"),
	}
	return c, r.err()
}

// Getenv is the default environment lookup.
var Getenv = os.Getenv
