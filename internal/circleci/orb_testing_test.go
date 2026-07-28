// Copyright (c) CircleCI
// SPDX-License-Identifier: MPL-2.0

package circleci_test

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"terraform-provider-circleci/internal/circleci"
)

// orbRequest is one request captured by orbRecorder.
type orbRequest struct {
	method string
	path   string
	query  string
	body   string
}

// orbRecorder records every request an orb or namespace client call made, so a
// test can assert on the exact route, query and body that went over the wire.
type orbRecorder struct {
	mu       sync.Mutex
	requests []orbRequest
}

func (r *orbRecorder) record(req *http.Request) {
	body, _ := io.ReadAll(req.Body)

	r.mu.Lock()
	defer r.mu.Unlock()

	r.requests = append(r.requests, orbRequest{
		method: req.Method,
		path:   req.URL.Path,
		query:  req.URL.RawQuery,
		body:   string(body),
	})
}

func (r *orbRecorder) all() []orbRequest {
	r.mu.Lock()
	defer r.mu.Unlock()

	return append([]orbRequest(nil), r.requests...)
}

// last returns the most recent request, failing the test when none was made.
func (r *orbRecorder) last(t *testing.T) orbRequest {
	t.Helper()

	all := r.all()
	if len(all) == 0 {
		t.Fatal("the client made no requests")
	}

	return all[len(all)-1]
}

// newOrbClient serves handler over a test server and returns a client pointed at
// it, along with the recorder of everything it received.
func newOrbClient(t *testing.T, handler http.HandlerFunc) (*circleci.Client, *orbRecorder) {
	t.Helper()

	rec := &orbRecorder{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rec.record(r)
		handler(w, r)
	}))
	t.Cleanup(srv.Close)

	return circleci.New(circleci.Config{Host: srv.URL, Token: "tok"}), rec
}

// orbJSON replies with a JSON body and status 200.
func orbJSON(body string) http.HandlerFunc {
	return func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(body))
	}
}

// orbStatus replies with a status code and body, for error-path tests.
func orbStatus(status int, body string) http.HandlerFunc {
	return func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_, _ = w.Write([]byte(body))
	}
}

// orbRoutes replies with a different body per request path prefix, for the calls
// that need more than one round trip.
//
// GetOrbVersionByRef fetches the orb package first, because filter[ref] takes a
// fully-qualified "namespace/orb@version" reference that cannot be built from an
// orb id alone.
func orbRoutes(bodies map[string]string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")

		for prefix, body := range bodies {
			if strings.HasPrefix(r.URL.Path, prefix) {
				_, _ = w.Write([]byte(body))

				return
			}
		}

		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte(`{"error":{"id":"trace","title":"Not Found."}}`))
	}
}

// orbPackageBody is a minimal detail-shape orb package, enough for
// GetOrbVersionByRef to build a qualified reference. name is the qualified
// "namespace/orb" form the API reports in attributes.name.
func orbPackageBody(id, namespace, name string) string {
	return `{"data":{"id":"` + id + `","attributes":{"name":"` + name + `"},` +
		`"references":{"namespace":{"id":"` + orbNsID + `","attributes":{"name":"` + namespace + `"}}}}}`
}
