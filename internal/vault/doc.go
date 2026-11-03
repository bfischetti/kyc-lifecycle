// Package vault is the Evidence Vault: private, S3-compatible object storage
// for Document bytes (ADR-0001, ADR-0002).
//
// It talks only to the S3 API. Locally and in tests the store is RustFS;
// in production it would be S3 itself.
package vault
