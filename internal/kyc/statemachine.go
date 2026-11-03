package kyc

import (
	"errors"
	"fmt"
)

// Event is something that can change an Entity's Verification Status.
type Event string

const (
	// EventCompletedUpload: a Document became a Completed Upload.
	EventCompletedUpload Event = "completed_upload"
	// EventApprove: a Compliance Approval.
	EventApprove Event = "approve"
	// EventReject: a Compliance Rejection.
	EventReject Event = "reject"
	// EventHardFail: a Check came back fail, causing a System Rejection.
	EventHardFail Event = "hard_fail"
)

// Statuses and Events lists every value, for exhaustive tests.
var (
	Statuses = []Status{StatusUnverified, StatusPendingReview, StatusVerified, StatusRejected}
	Events   = []Event{EventCompletedUpload, EventApprove, EventReject, EventHardFail}
)

// ErrInvalidTransition means the event is not allowed in the current status.
var ErrInvalidTransition = errors.New("invalid status transition")

// transitions is the whole lifecycle:
//
//	unverified     --completed_upload-->  pending_review   (opens a Review Cycle)
//	pending_review --completed_upload-->  pending_review   (no change: cycle already open)
//	pending_review --approve-->           verified
//	pending_review --reject|hard_fail-->  rejected
//	rejected       --completed_upload-->  pending_review   (Resubmission: new Review Cycle)
//	verified       : terminal
var transitions = map[Status]map[Event]Status{
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
	StatusVerified: {},
}

// Transition returns the status that event leads to from status from, or
// ErrInvalidTransition. A result equal to from means the event is allowed
// but does not change the status (and so produces no audit record).
func Transition(from Status, event Event) (Status, error) {
	to, ok := transitions[from][event]
	if !ok {
		return from, fmt.Errorf("%w: %s from %s", ErrInvalidTransition, event, from)
	}
	return to, nil
}
