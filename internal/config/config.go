// Package config reads configuration from environment variables and fails
// fast, naming every missing variable, rather than starting half-configured.
package config

import (
	"fmt"
	"os"
	"strings"
)

// Serve is the configuration for `server serve`.
type Serve struct {
	DatabaseURL string // runtime connection as the non-owner app role
	HTTPAddr    string
	LogLevel    string
}

// Migrate is the configuration for `server migrate`.
type Migrate struct {
	DatabaseURL string // connection as the owner role
	LogLevel    string
}

type reader struct {
	getenv  func(string) string
	missing []string
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

func (r *reader) err() error {
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
