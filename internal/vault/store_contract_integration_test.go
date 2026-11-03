//go:build integration

package vault_test

// Store contract for ADR-0002. These tests pin down behaviour of the
// S3-compatible store that the upload design depends on: a presigned PUT
// with content type, length and sha256 signed into it must store exactly
// the declared bytes and nothing else. If a store upgrade or swap breaks
// this, these fail before anything built on top does.

import (
	"bytes"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/aws-sdk-go-v2/service/s3/types"

	"github.com/bfischetti/kyc-lifecycle/internal/testenv"
)

func sha256B64(b []byte) string {
	h := sha256.Sum256(b)
	return base64.StdEncoding.EncodeToString(h[:])
}

func TestPresignedPutEnforcesSignedConstraints(t *testing.T) {
	v := testenv.StartVault(t)
	cli := v.Client()
	presigner := s3.NewPresignClient(cli)

	declared := append([]byte("%PDF-1.7\n"), bytes.Repeat([]byte("a"), 1000)...)
	sameSizeOther := append([]byte("%PDF-1.7\n"), bytes.Repeat([]byte("b"), 1000)...)
	longer := append(append([]byte(nil), declared...), 'x')

	tests := []struct {
		name        string
		body        []byte
		contentType string
		checksum    string // x-amz-checksum-sha256 header sent; "" omits it
		wantStored  bool
	}{
		{"matching upload is stored", declared, "application/pdf", sha256B64(declared), true},
		{"different bytes under the declared checksum", sameSizeOther, "application/pdf", sha256B64(declared), false},
		{"different bytes with their own checksum", sameSizeOther, "application/pdf", sha256B64(sameSizeOther), false},
		{"body longer than declared", longer, "application/pdf", sha256B64(declared), false},
		{"different content type", declared, "image/png", sha256B64(declared), false},
		{"checksum header omitted", declared, "application/pdf", "", false},
	}
	for i, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			key := "documents/contract-" + string(rune('a'+i))
			signed, err := presigner.PresignPutObject(t.Context(), &s3.PutObjectInput{
				Bucket:            aws.String(v.Bucket),
				Key:               aws.String(key),
				ContentType:       aws.String("application/pdf"),
				ContentLength:     aws.Int64(int64(len(declared))),
				ChecksumAlgorithm: types.ChecksumAlgorithmSha256,
				ChecksumSHA256:    aws.String(sha256B64(declared)),
			}, s3.WithPresignExpires(15*time.Minute))
			if err != nil {
				t.Fatalf("presign: %v", err)
			}

			req, err := http.NewRequestWithContext(t.Context(), http.MethodPut, signed.URL, bytes.NewReader(tt.body))
			if err != nil {
				t.Fatal(err)
			}
			for k, vals := range signed.SignedHeader {
				// net/http sets Host and Content-Length from the request itself.
				if strings.EqualFold(k, "Host") || strings.EqualFold(k, "Content-Length") {
					continue
				}
				req.Header[http.CanonicalHeaderKey(k)] = vals
			}
			req.Header.Set("Content-Type", tt.contentType)
			if tt.checksum == "" {
				req.Header.Del("X-Amz-Checksum-Sha256")
			} else {
				req.Header.Set("X-Amz-Checksum-Sha256", tt.checksum)
			}

			resp, err := http.DefaultClient.Do(req)
			if err != nil {
				t.Fatalf("PUT: %v", err)
			}
			respBody, _ := io.ReadAll(resp.Body)
			_ = resp.Body.Close()

			head, headErr := cli.HeadObject(t.Context(), &s3.HeadObjectInput{
				Bucket:       aws.String(v.Bucket),
				Key:          aws.String(key),
				ChecksumMode: types.ChecksumModeEnabled,
			})

			if tt.wantStored {
				if resp.StatusCode != http.StatusOK {
					t.Fatalf("PUT status %d, want 200: %s", resp.StatusCode, respBody)
				}
				if headErr != nil {
					t.Fatalf("object not stored: %v", headErr)
				}
				if got := aws.ToInt64(head.ContentLength); got != int64(len(declared)) {
					t.Errorf("stored size %d, want %d", got, len(declared))
				}
				if got := aws.ToString(head.ChecksumSHA256); got != sha256B64(declared) {
					t.Errorf("stored checksum %q, want %q", got, sha256B64(declared))
				}
				return
			}

			if resp.StatusCode < 400 || resp.StatusCode >= 500 {
				t.Errorf("PUT status %d, want a 4xx rejection: %s", resp.StatusCode, respBody)
			}
			var notFound *types.NotFound
			if !errors.As(headErr, &notFound) {
				t.Errorf("object exists after a rejected upload (head err: %v)", headErr)
			}
			t.Logf("rejected with HTTP %d", resp.StatusCode)
		})
	}
}
