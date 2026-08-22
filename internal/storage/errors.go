package storage

import (
	"errors"
	"net/http"
)

// Ingest failures, each mapping onto exactly one HTTP status.
//
// PHP returned an IngestResult carrying a status and a message. Go returns an
// error, and HTTPStatus keeps the status table in one place the way that
// struct did.
var (
	// ErrEmptyBody is a POST with no bytes at all.
	ErrEmptyBody = errors.New("empty body")

	// ErrLengthMismatch is a body that ended short of its Content-Length.
	// Storing it would hand out a URL to a truncated chunk that every
	// downloader would then fail on.
	ErrLengthMismatch = errors.New("body length does not match Content-Length")

	// ErrTooLarge is a body over Storage.MaxChunkSize, declared or actual.
	ErrTooLarge = errors.New("chunk exceeds maxChunkSize")

	// ErrNoStorage is any failure to put the bytes on disk.
	ErrNoStorage = errors.New("insufficient storage")
)

// statusMessage carries the exact wording the PHP server used, so a client
// comparing error strings across the two implementations sees no difference.
type statusError struct {
	err     error
	status  int
	message string
}

func (e *statusError) Error() string { return e.message }
func (e *statusError) Unwrap() error { return e.err }

func failure(err error, status int, message string) error {
	return &statusError{err: err, status: status, message: message}
}

// HTTPStatus maps an ingest error onto the status the contract requires,
// defaulting to 500 for anything unrecognised.
func HTTPStatus(err error) int {
	var se *statusError
	if errors.As(err, &se) {
		return se.status
	}

	switch {
	case errors.Is(err, ErrEmptyBody), errors.Is(err, ErrLengthMismatch):
		return http.StatusBadRequest
	case errors.Is(err, ErrTooLarge):
		return http.StatusRequestEntityTooLarge
	case errors.Is(err, ErrNoStorage):
		return http.StatusInsufficientStorage
	default:
		return http.StatusInternalServerError
	}
}
