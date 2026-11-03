// Package webhook signs and verifies Status Change Notification deliveries.
//
// The scheme follows the Standard Webhooks convention
// (https://www.standardwebhooks.com), so receivers can use an off-the-shelf
// verifier: three headers carry a message id, a Unix timestamp and an
// HMAC-SHA256 signature over "id.timestamp.body". Signing the timestamp lets
// a receiver reject replays of old deliveries; signing the id stops one
// delivery's headers being attached to another's body.
//
// One deviation: the shared secret is used as raw bytes rather than the
// "whsec_"-prefixed base64 form, to keep local configuration simple.
package webhook

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"
)

const (
	HeaderID        = "Webhook-Id"
	HeaderTimestamp = "Webhook-Timestamp"
	HeaderSignature = "Webhook-Signature"

	// DefaultTolerance is how far a delivery's timestamp may be from the
	// receiver's clock, in either direction.
	DefaultTolerance = 5 * time.Minute

	signatureVersion = "v1"
)

var (
	ErrMissingHeaders = errors.New("webhook: missing signature headers")
	ErrTimestamp      = errors.New("webhook: timestamp outside tolerance")
	ErrSignature      = errors.New("webhook: no matching signature")
)

// Sign returns the Webhook-Signature header value for a delivery.
func Sign(secret []byte, id string, ts time.Time, body []byte) string {
	return signatureVersion + "," + base64.StdEncoding.EncodeToString(mac(secret, id, ts.Unix(), body))
}

// SetHeaders signs a delivery and sets all three headers on h.
func SetHeaders(h http.Header, secret []byte, id string, ts time.Time, body []byte) {
	h.Set(HeaderID, id)
	h.Set(HeaderTimestamp, strconv.FormatInt(ts.Unix(), 10))
	h.Set(HeaderSignature, Sign(secret, id, ts, body))
}

func mac(secret []byte, id string, unix int64, body []byte) []byte {
	m := hmac.New(sha256.New, secret)
	m.Write([]byte(id + "." + strconv.FormatInt(unix, 10) + "."))
	m.Write(body)
	return m.Sum(nil)
}

// Verifier checks deliveries on the receiving side.
type Verifier struct {
	Secret    []byte
	Tolerance time.Duration    // zero means DefaultTolerance
	Now       func() time.Time // nil means time.Now
}

// Verify reports whether body and its headers were signed with the secret
// within the tolerance window. The signature header may list several
// space-separated signatures (as during secret rotation); any match passes.
func (v Verifier) Verify(h http.Header, body []byte) error {
	id, rawTS, sigs := h.Get(HeaderID), h.Get(HeaderTimestamp), h.Get(HeaderSignature)
	if id == "" || rawTS == "" || sigs == "" {
		return ErrMissingHeaders
	}
	unix, err := strconv.ParseInt(rawTS, 10, 64)
	if err != nil {
		return fmt.Errorf("%w: unparseable", ErrTimestamp)
	}
	now := time.Now
	if v.Now != nil {
		now = v.Now
	}
	tolerance := v.Tolerance
	if tolerance == 0 {
		tolerance = DefaultTolerance
	}
	if d := now().Sub(time.Unix(unix, 0)); d > tolerance || d < -tolerance {
		return ErrTimestamp
	}

	want := mac(v.Secret, id, unix, body)
	for _, s := range strings.Fields(sigs) {
		version, encoded, ok := strings.Cut(s, ",")
		if !ok || version != signatureVersion {
			continue
		}
		got, err := base64.StdEncoding.DecodeString(encoded)
		if err == nil && hmac.Equal(got, want) {
			return nil
		}
	}
	return ErrSignature
}
