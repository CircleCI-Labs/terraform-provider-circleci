// Copyright (c) CircleCI
// SPDX-License-Identifier: MPL-2.0

package circleci

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"

	"terraform-provider-circleci/internal/httpcl"
)

// ErrNotFound reports that a resource does not exist.
//
// It exists because an HTTP 404 is not the only way the API says "gone". v3
// collections are scoped by filter and return 200 with an empty data array when
// nothing matches, so a lookup-by-filter has to translate that into this
// sentinel. Always test with IsNotFound rather than checking a status code
// directly.
var ErrNotFound = errors.New("not found")

// APIError is the v3 error envelope:
//
//	{"error": {"id", "type", "title", "detail", "source": {...}}}
type APIError struct {
	ID     string      `json:"id"`
	Type   string      `json:"type"`
	Title  string      `json:"title"`
	Detail string      `json:"detail"`
	Source ErrorSource `json:"source"`
}

// ErrorSource locates the cause of an APIError within the request, typically as
// a JSON pointer to the offending field.
type ErrorSource struct {
	Error   string `json:"error"`
	Offset  int    `json:"offset"`
	Pointer string `json:"pointer"`
}

func (e *APIError) Error() string {
	switch {
	case e.Detail == "":
		return e.Title
	case e.Title == "":
		return e.Detail
	default:
		return e.Title + ": " + e.Detail
	}
}

// Message renders the error for display in a Terraform diagnostic: the summary
// first, then the offending field and the server-side error id when present. The
// error id is what CircleCI support needs to trace a request.
func (e *APIError) Message() string {
	var b strings.Builder

	b.WriteString(e.Error())

	if e.Source.Pointer != "" {
		_, _ = fmt.Fprintf(&b, "\n  at %s", e.Source.Pointer)
		if e.Source.Offset > 0 {
			_, _ = fmt.Fprintf(&b, " (offset %d)", e.Source.Offset)
		}
	}
	if e.Source.Error != "" {
		_, _ = fmt.Fprintf(&b, "\n  %s", e.Source.Error)
	}
	if e.ID != "" {
		_, _ = fmt.Fprintf(&b, "\nerror id: %s", e.ID)
	}

	return b.String()
}

// HasStatus reports whether err carries any of the given HTTP status codes.
func HasStatus(err error, codes ...int) bool {
	return httpcl.HasStatusCode(err, codes...)
}

// StatusCode returns the HTTP status code carried by err, and whether err was an
// HTTP error at all.
func StatusCode(err error) (int, bool) {
	var httpErr *httpcl.HTTPError
	if !errors.As(err, &httpErr) {
		return 0, false
	}

	return httpErr.StatusCode, true
}

// IsNotFound reports whether err means the resource does not exist, covering
// both an HTTP 404 and the ErrNotFound sentinel used for empty v3 collections.
//
// Use this to decide whether to drop a resource from state. It replaces
// substring matching on error text, which also matched unrelated 5xx responses
// whose bodies happened to contain "404" and so deleted live resources from
// state.
func IsNotFound(err error) bool {
	return errors.Is(err, ErrNotFound) || HasStatus(err, http.StatusNotFound)
}

// IsGone reports whether an endpoint has been removed. v3 returns 410 for routes
// retired after their deprecation window, as distinct from a missing resource.
func IsGone(err error) bool {
	return HasStatus(err, http.StatusGone)
}

// IsUnauthorized reports whether err was an authentication or authorization
// failure. Note that v3 deliberately answers 404 rather than 403 for
// cross-tenant access, so a permissions problem can surface as IsNotFound.
func IsUnauthorized(err error) bool {
	return HasStatus(err, http.StatusUnauthorized, http.StatusForbidden)
}

// IsConflict reports whether err was a conflict, such as creating a resource
// class that already exists, or deleting one that still has tokens.
func IsConflict(err error) bool {
	return HasStatus(err, http.StatusConflict)
}

// ParseAPIError extracts the v3 error envelope from err. It reports false for
// non-HTTP errors and for bodies without the envelope, such as v2 responses and
// HTML error pages from an intermediate proxy.
func ParseAPIError(err error) (*APIError, bool) {
	var httpErr *httpcl.HTTPError
	if !errors.As(err, &httpErr) || len(httpErr.Body) == 0 {
		return nil, false
	}

	var envelope struct {
		Error *APIError `json:"error"`
	}

	// A body we cannot parse is simply not a v3 envelope, so report that rather
	// than propagating the decode failure: callers fall back to Detail.
	decoded := json.Unmarshal(httpErr.Body, &envelope) == nil
	if !decoded || envelope.Error == nil {
		return nil, false
	}
	if envelope.Error.Title == "" && envelope.Error.Detail == "" {
		return nil, false
	}

	return envelope.Error, true
}

// serverMessage extracts a human-readable message from a v1/v2 error body, which
// uses a bare "message" or "error" string rather than the v3 envelope.
func serverMessage(body []byte) string {
	if len(body) == 0 {
		return ""
	}

	var payload struct {
		Error   string `json:"error"`
		Message string `json:"message"`
	}
	if json.Unmarshal(body, &payload) == nil {
		if payload.Error != "" {
			return payload.Error
		}
		if payload.Message != "" {
			return payload.Message
		}
	}

	return strings.TrimSpace(string(body))
}

// Detail renders err for the detail field of a Terraform diagnostic, preferring
// the v3 error envelope, then a v1/v2 message body, then the raw error.
func Detail(err error) string {
	if err == nil {
		return ""
	}

	if apiErr, ok := ParseAPIError(err); ok {
		return apiErr.Message()
	}

	var httpErr *httpcl.HTTPError
	if errors.As(err, &httpErr) {
		if msg := serverMessage(httpErr.Body); msg != "" {
			return fmt.Sprintf("%s (HTTP %d)", msg, httpErr.StatusCode)
		}

		return httpErr.Error()
	}

	return err.Error()
}
