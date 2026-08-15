package workentry

import "errors"

// Typed errors for the work-entry kernel. HTTP mapping lives in the handler
// layer; CLI mapping lives in cmd/multica. Reason codes follow API-AND-
// ADAPTER-CONTRACT §8.
var (
	// ErrConflict is the same-key/different-digest conflict (HTTP 409).
	ErrConflict = errors.New("work entry payload conflict")

	// ErrInvalidRequest is a malformed or missing required field (HTTP 400).
	ErrInvalidRequest = errors.New("invalid work entry request")

	// ErrNotFound is a missing receipt/event/object (HTTP 404).
	ErrNotFound = errors.New("work entry object not found")

	// ErrClassificationRequired means ownership could not be confirmed and the
	// caller must obtain a classification decision before any creation. It is
	// not an error condition for resolve-preview; register surfaces it as a
	// receipt with resolution_decision=classification_required.
	ErrClassificationRequired = errors.New("classification_required: ownership is not confirmed")

	// ErrUnavailable means the concrete Store cannot persist this operation in
	// the current slice (e.g. no reusable table without a new migration). It is
	// mapped to 503 by the HTTP layer.
	ErrUnavailable = errors.New("work entry operation unavailable in this slice")
)
