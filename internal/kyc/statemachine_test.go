package kyc

import (
	"errors"
	"testing"
)

func TestTransitionCoversEveryPair(t *testing.T) {
	// want lists every allowed (status, event) pair. Every pair not listed
	// must be refused, so adding a status or event without deciding its
	// transitions fails this test.
	want := map[Status]map[Event]Status{
		StatusUnverified: {
			EventCompletedUpload: StatusPendingReview,
		},
		StatusPendingReview: {
			EventCompletedUpload: StatusPendingReview,
			EventApprove:         StatusVerified,
			EventReject:          StatusRejected,
			EventHardFail:        StatusRejected,
		},
		StatusRejected: {
			EventCompletedUpload: StatusPendingReview,
		},
	}

	for _, from := range Statuses {
		for _, ev := range Events {
			t.Run(string(from)+"/"+string(ev), func(t *testing.T) {
				got, err := Transition(from, ev)
				wantTo, allowed := want[from][ev]
				if allowed {
					if err != nil {
						t.Fatalf("Transition(%s, %s) error = %v, want %s", from, ev, err, wantTo)
					}
					if got != wantTo {
						t.Errorf("Transition(%s, %s) = %s, want %s", from, ev, got, wantTo)
					}
					return
				}
				if !errors.Is(err, ErrInvalidTransition) {
					t.Errorf("Transition(%s, %s) = (%s, %v), want ErrInvalidTransition", from, ev, got, err)
				}
				if got != from {
					t.Errorf("refused transition returned %s, want unchanged %s", got, from)
				}
			})
		}
	}
}

func TestUnknownStatusOrEventIsRefused(t *testing.T) {
	if _, err := Transition("archived", EventApprove); !errors.Is(err, ErrInvalidTransition) {
		t.Errorf("unknown status: err = %v", err)
	}
	if _, err := Transition(StatusPendingReview, "escalate"); !errors.Is(err, ErrInvalidTransition) {
		t.Errorf("unknown event: err = %v", err)
	}
}
