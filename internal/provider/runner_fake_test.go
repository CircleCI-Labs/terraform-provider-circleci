// Copyright (c) CircleCI
// SPDX-License-Identifier: MPL-2.0

package provider

import (
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"sync"
	"testing"
)

// The runner admin API lives on its own origin, which the provider reaches
// through the runner_host attribute (see provider.go). That makes it easy to
// point the runner service at a fake: these helpers stand up an httptest server
// that speaks the runner API and record every request it receives, so tests can
// assert on what the provider actually sent rather than only on resulting state.

// runnerFakeRequest is one request the fake runner API received.
type runnerFakeRequest struct {
	Method string
	Path   string
	Query  url.Values
	Body   string
}

// runnerFakeAPI is a fake of the CircleCI runner admin API.
type runnerFakeAPI struct {
	server *httptest.Server

	mu        sync.Mutex
	requests  []runnerFakeRequest
	responses map[string]string
}

// newRunnerFakeAPI starts a fake runner API and stops it when the test ends.
func newRunnerFakeAPI(t *testing.T) *runnerFakeAPI {
	t.Helper()

	api := &runnerFakeAPI{responses: map[string]string{}}
	api.server = httptest.NewServer(http.HandlerFunc(api.handle))
	t.Cleanup(api.server.Close)

	return api
}

// respond registers the response body for a "METHOD /path" key. Requests with no
// registered response get an empty 200, which is what the API returns for a
// delete.
func (a *runnerFakeAPI) respond(method, path, body string) {
	a.mu.Lock()
	defer a.mu.Unlock()

	a.responses[method+" "+path] = body
}

// URL is the fake's origin, for the provider's runner_host attribute.
func (a *runnerFakeAPI) URL() string {
	return a.server.URL
}

func (a *runnerFakeAPI) handle(w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(r.Body)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)

		return
	}

	a.mu.Lock()
	a.requests = append(a.requests, runnerFakeRequest{
		Method: r.Method,
		Path:   r.URL.Path,
		Query:  r.URL.Query(),
		Body:   string(body),
	})
	response, ok := a.responses[r.Method+" "+r.URL.Path]
	a.mu.Unlock()

	w.Header().Set("Content-Type", "application/json")
	if !ok {
		return
	}

	_, _ = io.WriteString(w, response)
}

// requests returns every recorded request for a method and path.
func (a *runnerFakeAPI) requestsFor(method, path string) []runnerFakeRequest {
	a.mu.Lock()
	defer a.mu.Unlock()

	var matching []runnerFakeRequest
	for _, request := range a.requests {
		if request.Method == method && request.Path == path {
			matching = append(matching, request)
		}
	}

	return matching
}

// firstRequest returns the first recorded request for a method and path, failing
// the test when the provider never made it.
func (a *runnerFakeAPI) firstRequest(t *testing.T, method, path string) runnerFakeRequest {
	t.Helper()

	matching := a.requestsFor(method, path)
	if len(matching) == 0 {
		t.Fatalf("expected the provider to send %s %s, but it never did (requests: %v)", method, path, a.allRequests())
	}

	return matching[0]
}

// allRequests returns a copy of every recorded request, for failure messages.
func (a *runnerFakeAPI) allRequests() []runnerFakeRequest {
	a.mu.Lock()
	defer a.mu.Unlock()

	return append([]runnerFakeRequest(nil), a.requests...)
}

// runnerProviderConfig renders a provider block pointing the runner service at
// the fake. host is deliberately unroutable: these tests must not reach the
// main API, and a connection error is a clearer failure than a silent success.
func runnerProviderConfig(runnerHost string) string {
	return fmt.Sprintf(`
provider "circleci" {
  host        = "http://127.0.0.1:1"
  runner_host = %q
  key         = "fake"
}
`, runnerHost)
}

// runnerProtoV6ProviderFactories instantiates the provider for the runner tests.
// It is the standard factory map; the alias keeps these tests from silently
// depending on the shared one growing extra providers.
var runnerProtoV6ProviderFactories = testAccProtoV6ProviderFactories
