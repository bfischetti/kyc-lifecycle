package vault

import (
	"context"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/aws-sdk-go-v2/service/s3/types"
	"github.com/google/uuid"
)

// Config locates the Evidence Vault.
type Config struct {
	// Endpoint is the S3 API base URL. Presigned URLs are signed for this
	// host, so it must be the address clients use to upload.
	Endpoint  string
	Region    string
	AccessKey string
	SecretKey string
	Bucket    string
}

// Client is the Evidence Vault.
type Client struct {
	s3      *s3.Client
	presign *s3.PresignClient
	bucket  string
}

// New returns a Client for cfg.
func New(cfg Config) *Client {
	cli := s3.New(s3.Options{
		Region:       cfg.Region,
		BaseEndpoint: aws.String(cfg.Endpoint),
		UsePathStyle: true,
		Credentials:  credentials.NewStaticCredentialsProvider(cfg.AccessKey, cfg.SecretKey, ""),
	})
	return &Client{s3: cli, presign: s3.NewPresignClient(cli), bucket: cfg.Bucket}
}

// NewKey returns a fresh object key. It is random and carries no PII and no
// Entity identifier, so knowing it reveals nothing (ADR-0001).
func NewKey() string { return "documents/" + uuid.NewString() }

// UploadPermission lets a caller deliver exactly the declared bytes to the
// vault, once, before ExpiresAt.
type UploadPermission struct {
	Method string
	URL    string
	// Headers the caller must send verbatim; they are part of the signature.
	Headers   map[string]string
	ExpiresAt time.Time
}

// Declared is what the caller said they will upload.
type Declared struct {
	ContentType string
	SizeBytes   int64
	SHA256Hex   string
}

// PresignUpload returns a permission to PUT the declared bytes at key. The
// content type, length and SHA-256 are signed into it, so the store rejects
// any other upload (verified by the store contract test; ADR-0002).
func (c *Client) PresignUpload(ctx context.Context, key string, d Declared, ttl time.Duration) (UploadPermission, error) {
	sum, err := hex.DecodeString(d.SHA256Hex)
	if err != nil || len(sum) != 32 {
		return UploadPermission{}, fmt.Errorf("sha256 must be 64 hex characters")
	}
	now := time.Now()
	req, err := c.presign.PresignPutObject(ctx, &s3.PutObjectInput{
		Bucket:            aws.String(c.bucket),
		Key:               aws.String(key),
		ContentType:       aws.String(d.ContentType),
		ContentLength:     aws.Int64(d.SizeBytes),
		ChecksumAlgorithm: types.ChecksumAlgorithmSha256,
		ChecksumSHA256:    aws.String(base64.StdEncoding.EncodeToString(sum)),
	}, s3.WithPresignExpires(ttl))
	if err != nil {
		return UploadPermission{}, fmt.Errorf("presign upload: %w", err)
	}

	headers := make(map[string]string, len(req.SignedHeader))
	for k, v := range req.SignedHeader {
		// Host comes from the URL; HTTP clients set Content-Length from the body.
		if strings.EqualFold(k, "Host") {
			continue
		}
		headers[http.CanonicalHeaderKey(k)] = strings.Join(v, ",")
	}
	return UploadPermission{Method: req.Method, URL: req.URL, Headers: headers, ExpiresAt: now.Add(ttl)}, nil
}

// DownloadPermission is a short-lived URL that returns one object's bytes.
type DownloadPermission struct {
	URL       string
	ExpiresAt time.Time
}

// PresignDownload returns a permission to GET the object at key for ttl.
// The response is served as an attachment named filename, so a browser
// saves the evidence rather than rendering it inline.
func (c *Client) PresignDownload(ctx context.Context, key, filename string, ttl time.Duration) (DownloadPermission, error) {
	now := time.Now()
	req, err := c.presign.PresignGetObject(ctx, &s3.GetObjectInput{
		Bucket:                     aws.String(c.bucket),
		Key:                        aws.String(key),
		ResponseContentDisposition: aws.String(fmt.Sprintf("attachment; filename=%q", filename)),
		ResponseCacheControl:       aws.String("no-store"),
	}, s3.WithPresignExpires(ttl))
	if err != nil {
		return DownloadPermission{}, fmt.Errorf("presign download: %w", err)
	}
	return DownloadPermission{URL: req.URL, ExpiresAt: now.Add(ttl)}, nil
}

// ErrObjectNotFound means nothing is stored under the key.
var ErrObjectNotFound = errors.New("object not found")

// ObjectInfo is what the vault reports about a stored object.
type ObjectInfo struct {
	SizeBytes   int64
	SHA256Hex   string // empty if the store holds no SHA-256 for it
	ContentType string
}

// Stat describes the object at key, or returns ErrObjectNotFound.
func (c *Client) Stat(ctx context.Context, key string) (ObjectInfo, error) {
	out, err := c.s3.HeadObject(ctx, &s3.HeadObjectInput{
		Bucket:       aws.String(c.bucket),
		Key:          aws.String(key),
		ChecksumMode: types.ChecksumModeEnabled,
	})
	var nf *types.NotFound
	if errors.As(err, &nf) {
		return ObjectInfo{}, ErrObjectNotFound
	}
	if err != nil {
		return ObjectInfo{}, fmt.Errorf("head object: %w", err)
	}
	info := ObjectInfo{SizeBytes: aws.ToInt64(out.ContentLength), ContentType: aws.ToString(out.ContentType)}
	if b64 := aws.ToString(out.ChecksumSHA256); b64 != "" {
		if sum, err := base64.StdEncoding.DecodeString(b64); err == nil {
			info.SHA256Hex = hex.EncodeToString(sum)
		}
	}
	return info, nil
}

// ReadPrefix returns up to n leading bytes of the object at key.
func (c *Client) ReadPrefix(ctx context.Context, key string, n int) ([]byte, error) {
	out, err := c.s3.GetObject(ctx, &s3.GetObjectInput{
		Bucket: aws.String(c.bucket),
		Key:    aws.String(key),
		Range:  aws.String(fmt.Sprintf("bytes=0-%d", n-1)),
	})
	var nk *types.NoSuchKey
	if errors.As(err, &nk) {
		return nil, ErrObjectNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("get object: %w", err)
	}
	defer func() { _ = out.Body.Close() }()
	return io.ReadAll(io.LimitReader(out.Body, int64(n)))
}
