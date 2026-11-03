package webhook

import (
	"errors"
	"net/http"
	"strconv"
	"testing"
	"time"
)

var (
	secret = []byte("test-secret-at-least-32-characters!!")
	now    = time.Unix(1_800_000_000, 0)
	body   = []byte(`{"type":"entity.status_changed","data":{"to":"verified"}}`)
)

func signed(id string, ts time.Time, b []byte) http.Header {
	h := http.Header{}
	SetHeaders(h, secret, id, ts, b)
	return h
}

func TestVerify(t *testing.T) {
	v := Verifier{Secret: secret, Now: func() time.Time { return now }}

	tests := []struct {
		name   string
		header http.Header
		body   []byte
		want   error
	}{
		{"valid", signed("msg_1", now, body), body, nil},
		{"valid at the edge of tolerance", signed("msg_1", now.Add(-DefaultTolerance), body), body, nil},
		{"tampered body", signed("msg_1", now, body), []byte(`{"type":"entity.status_changed","data":{"to":"rejected"}}`), ErrSignature},
		{"too old (replay)", signed("msg_1", now.Add(-DefaultTolerance-time.Second), body), body, ErrTimestamp},
		{"too far in the future", signed("msg_1", now.Add(DefaultTolerance+time.Second), body), body, ErrTimestamp},
		{"id swapped onto another delivery", func() http.Header {
			h := signed("msg_1", now, body)
			h.Set(HeaderID, "msg_2")
			return h
		}(), body, ErrSignature},
		{"timestamp changed after signing", func() http.Header {
			h := signed("msg_1", now, body)
			h.Set(HeaderTimestamp, strconv.FormatInt(now.Unix()+1, 10))
			return h
		}(), body, ErrSignature},
		{"wrong secret", func() http.Header {
			h := http.Header{}
			SetHeaders(h, []byte("some-other-secret-of-similar-length!"), "msg_1", now, body)
			return h
		}(), body, ErrSignature},
		{"unknown signature version", func() http.Header {
			h := signed("msg_1", now, body)
			h.Set(HeaderSignature, "v2,"+h.Get(HeaderSignature)[3:])
			return h
		}(), body, ErrSignature},
		{"one of several signatures matches (rotation)", func() http.Header {
			h := signed("msg_1", now, body)
			h.Set(HeaderSignature, "v1,bm90LWl0 "+h.Get(HeaderSignature))
			return h
		}(), body, nil},
		{"missing signature", func() http.Header {
			h := signed("msg_1", now, body)
			h.Del(HeaderSignature)
			return h
		}(), body, ErrMissingHeaders},
		{"unparseable timestamp", func() http.Header {
			h := signed("msg_1", now, body)
			h.Set(HeaderTimestamp, "yesterday")
			return h
		}(), body, ErrTimestamp},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if err := v.Verify(tt.header, tt.body); !errors.Is(err, tt.want) {
				t.Errorf("Verify() = %v, want %v", err, tt.want)
			}
		})
	}
}

func TestToleranceIsConfigurable(t *testing.T) {
	v := Verifier{Secret: secret, Tolerance: 10 * time.Second, Now: func() time.Time { return now }}
	if err := v.Verify(signed("msg_1", now.Add(-11*time.Second), body), body); !errors.Is(err, ErrTimestamp) {
		t.Errorf("Verify() = %v, want ErrTimestamp", err)
	}
}
