package app

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5/pgtype"

	"github.com/bfischetti/kyc-lifecycle/internal/db"
	"github.com/bfischetti/kyc-lifecycle/internal/db/dbgen"
	"github.com/bfischetti/kyc-lifecycle/internal/kyc"
)

var (
	// ErrIdempotencyKeyReused: the key was first used with a different request.
	ErrIdempotencyKeyReused = errors.New("idempotency key reused with a different request")
	// ErrIdempotencyInProgress: the first request with this key has not
	// finished within the wait; the client should retry shortly.
	ErrIdempotencyInProgress = errors.New("a request with this idempotency key is still in progress")
)

// StoredResponse is the response recorded for an idempotency key.
type StoredResponse struct {
	Status int
	Body   []byte
}

// IdempotencyOptions tunes key handling.
type IdempotencyOptions struct {
	// Wait is how long a duplicate waits for the first request to finish
	// before giving up with ErrIdempotencyInProgress. It should exceed the
	// slowest guarded request (a provider call).
	Wait time.Duration
	// Lease is how long a claim stays valid without completing. After it,
	// the claim is assumed dead (e.g. the process crashed) and a retry may
	// take it over and redo the work.
	Lease time.Duration
	// Poll is how often a waiting duplicate looks for the result.
	Poll time.Duration
}

// DefaultIdempotencyOptions suit the default ProviderTimeout (5s).
var DefaultIdempotencyOptions = IdempotencyOptions{Wait: 10 * time.Second, Lease: time.Minute, Poll: 50 * time.Millisecond}

// BeginIdempotent claims key for this request. It returns nil when the
// caller should do the work (and then call FinishIdempotent), or the stored
// response when the same request already ran.
//
// A key is scoped to the actor: another user's identical key is unrelated.
func (s *Service) BeginIdempotent(ctx context.Context, actor kyc.Actor, key string, fingerprint []byte) (*StoredResponse, error) {
	o := s.opts.Idempotency
	deadline := time.Now().Add(o.Wait)
	for {
		stored, claimed, err := s.tryClaim(ctx, actor, key, fingerprint)
		if err != nil || claimed || stored != nil {
			return stored, err
		}
		// Someone else holds the claim: wait for their result.
		if time.Now().After(deadline) {
			return nil, ErrIdempotencyInProgress
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(o.Poll):
		}
	}
}

// tryClaim commits a claim on its own, before any work starts, so that a
// concurrent duplicate sees it.
func (s *Service) tryClaim(ctx context.Context, actor kyc.Actor, key string, fingerprint []byte) (stored *StoredResponse, claimed bool, err error) {
	err = s.store.InTx(ctx, actor, func(q *dbgen.Queries) error {
		n, err := q.ClaimIdempotencyKey(ctx, dbgen.ClaimIdempotencyKeyParams{UserID: actor.UserID, Key: key, Fingerprint: fingerprint})
		if err != nil {
			return fmt.Errorf("claim idempotency key: %w", err)
		}
		if n == 1 {
			claimed = true
			return nil
		}
		row, err := q.GetIdempotencyKey(ctx, dbgen.GetIdempotencyKeyParams{UserID: actor.UserID, Key: key})
		if db.IsNoRows(err) {
			return nil // released between our insert and select; loop again
		}
		if err != nil {
			return fmt.Errorf("get idempotency key: %w", err)
		}
		if !bytes.Equal(row.Fingerprint, fingerprint) {
			return ErrIdempotencyKeyReused
		}
		if row.CompletedAt != nil {
			stored = &StoredResponse{Status: int(row.ResponseStatus.Int32), Body: row.ResponseBody}
			return nil
		}
		n, err = q.TakeOverIdempotencyKey(ctx, dbgen.TakeOverIdempotencyKeyParams{
			UserID: actor.UserID, Key: key, LeaseSeconds: s.opts.Idempotency.Lease.Seconds(),
		})
		if err != nil {
			return fmt.Errorf("take over idempotency key: %w", err)
		}
		claimed = n == 1
		return nil
	})
	return stored, claimed, err
}

// FinishIdempotent records the response for a claimed key. Responses below
// 500 are final and replayed to every retry. A 5xx (including a Provider
// Error's 502) means nothing durable happened, so the claim is released
// and a retry with the same key does the work again.
func (s *Service) FinishIdempotent(ctx context.Context, actor kyc.Actor, key string, status int, body []byte) error {
	return s.store.InTx(ctx, actor, func(q *dbgen.Queries) error {
		if status >= 500 {
			return q.ReleaseIdempotencyKey(ctx, dbgen.ReleaseIdempotencyKeyParams{UserID: actor.UserID, Key: key})
		}
		return q.CompleteIdempotencyKey(ctx, dbgen.CompleteIdempotencyKeyParams{
			UserID: actor.UserID, Key: key, ResponseStatus: pgtype.Int4{Int32: int32(status), Valid: true}, ResponseBody: body,
		})
	})
}
