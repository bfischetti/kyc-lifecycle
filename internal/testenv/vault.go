//go:build integration

package testenv

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/aws-sdk-go-v2/service/s3/types"
	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/wait"
)

// VaultImage is the S3-compatible store used for the Evidence Vault in
// tests. Keep in sync with docker-compose.yml.
const VaultImage = "rustfs/rustfs:1.0.4"

// Vault holds what a test needs to reach a throwaway Evidence Vault.
type Vault struct {
	Endpoint  string
	Region    string
	AccessKey string
	SecretKey string
	Bucket    string
}

// Client returns an S3 client for the vault (path-style addressing).
func (v Vault) Client() *s3.Client {
	return s3.New(s3.Options{
		Region:       v.Region,
		BaseEndpoint: aws.String(v.Endpoint),
		UsePathStyle: true,
		Credentials:  credentials.NewStaticCredentialsProvider(v.AccessKey, v.SecretKey, ""),
	})
}

// StartVault starts the S3-compatible store with an empty private bucket and
// terminates it when the test ends.
func StartVault(t testing.TB) Vault {
	t.Helper()
	ctx := context.Background()
	v := Vault{Region: "us-east-1", AccessKey: "vault-test", SecretKey: "vault-test-secret", Bucket: "evidence"}

	ctr, err := testcontainers.Run(ctx, VaultImage,
		testcontainers.WithEnv(map[string]string{
			"RUSTFS_ACCESS_KEY": v.AccessKey,
			"RUSTFS_SECRET_KEY": v.SecretKey,
		}),
		testcontainers.WithExposedPorts("9000/tcp"),
		testcontainers.WithWaitStrategy(wait.ForListeningPort("9000/tcp").WithStartupTimeout(60*time.Second)),
	)
	testcontainers.CleanupContainer(t, ctr)
	if err != nil {
		t.Fatalf("start vault: %v", err)
	}
	endpoint, err := ctr.PortEndpoint(ctx, "9000/tcp", "http")
	if err != nil {
		t.Fatalf("vault endpoint: %v", err)
	}
	v.Endpoint = endpoint

	// The port can accept connections slightly before the S3 API is ready.
	cli := v.Client()
	deadline := time.Now().Add(30 * time.Second)
	for {
		_, err = cli.CreateBucket(ctx, &s3.CreateBucketInput{Bucket: aws.String(v.Bucket)})
		var owned *types.BucketAlreadyOwnedByYou
		if err == nil || errors.As(err, &owned) {
			return v
		}
		if time.Now().After(deadline) {
			t.Fatal(fmt.Errorf("create bucket: %w", err))
		}
		time.Sleep(500 * time.Millisecond)
	}
}
