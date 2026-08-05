// Copyright (c) CircleCI
// SPDX-License-Identifier: MPL-2.0

package circleci_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"

	"terraform-provider-circleci/internal/circleci"
)

// newGovernanceServer starts a JSON test server running handler. It exists
// because the governance tests need to inspect request bodies, which the
// canned-response newRecordingServer cannot do.
func newGovernanceServer(t *testing.T, handler http.HandlerFunc) *httptest.Server {
	t.Helper()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		handler(w, r)
	}))
	t.Cleanup(srv.Close)

	return srv
}

// governanceJSONEqual compares two decoded JSON values.
func governanceJSONEqual(got, want any) bool {
	return reflect.DeepEqual(got, want)
}

const testPolicyOwnerID = "11111111-1111-1111-1111-111111111111"

// bundleObjectBody is the bundle shape the API's own schema most nearly
// describes: an object keyed by policy name whose values are Policy documents.
const bundleObjectBody = `{
  "allow-docker.rego": {
    "name": "allow-docker.rego",
    "content": "package org\n\npolicy_name[\"allow_docker\"]\n",
    "created_at": "2024-01-02T03:04:05Z",
    "created_by": "alice"
  },
  "require-approval.rego": {
    "name": "require-approval.rego",
    "content": "package org\n",
    "created_at": "2024-01-02T03:04:06Z",
    "created_by": "bob"
  }
}`

func TestGetPolicyBundleRoute(t *testing.T) {
	t.Parallel()

	srv, rec := newRecordingServer(t, http.StatusOK, bundleObjectBody)
	c := circleci.New(circleci.Config{Host: srv.URL, Token: "tok"})

	bundle, err := c.GetPolicyBundle(context.Background(), testPolicyOwnerID, circleci.PolicyContextConfig)
	if err != nil {
		t.Fatalf("GetPolicyBundle returned error: %v", err)
	}

	wantURI := "/api/v2/owner/" + testPolicyOwnerID + "/context/config/policy-bundle"
	if rec.requestURI != wantURI {
		t.Errorf("raw request URI = %q, want %q", rec.requestURI, wantURI)
	}

	if got, want := len(bundle), 2; got != want {
		t.Fatalf("len(bundle) = %d, want %d", got, want)
	}

	policy := bundle["allow-docker.rego"]
	if policy.Content != "package org\n\npolicy_name[\"allow_docker\"]\n" {
		t.Errorf("Content = %q, want the Rego source from the body", policy.Content)
	}
	if policy.CreatedBy != "alice" {
		t.Errorf("CreatedBy = %q, want %q", policy.CreatedBy, "alice")
	}
	if policy.Name != "allow-docker.rego" {
		t.Errorf("Name = %q, want %q", policy.Name, "allow-docker.rego")
	}
}

// TestPolicyBundleDecodesEveryPermittedShape covers the tolerant decoder. The
// endpoint's schema declares bundle entries with an "items" keyword but no
// "type", which pins them down to neither an object nor an array, so all three
// plausible shapes have to decode.
func TestPolicyBundleDecodesEveryPermittedShape(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		body        string
		wantContent string
		wantName    string
	}{
		{
			name:        "policy object",
			body:        `{"a.rego":{"name":"a.rego","content":"package org","created_by":"alice"}}`,
			wantContent: "package org",
			wantName:    "a.rego",
		},
		{
			name:        "single-element array",
			body:        `{"a.rego":[{"name":"a.rego","content":"package org"}]}`,
			wantContent: "package org",
			wantName:    "a.rego",
		},
		{
			name:        "bare rego string",
			body:        `{"a.rego":"package org"}`,
			wantContent: "package org",
			wantName:    "a.rego",
		},
		{
			// An entry whose document omits its own name still gets one from the
			// bundle key, which is authoritative.
			name:        "object without a name",
			body:        `{"a.rego":{"content":"package org"}}`,
			wantContent: "package org",
			wantName:    "a.rego",
		},
		{
			name:        "null entry",
			body:        `{"a.rego":null}`,
			wantContent: "",
			wantName:    "a.rego",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			srv, _ := newRecordingServer(t, http.StatusOK, tc.body)
			c := circleci.New(circleci.Config{Host: srv.URL, Token: "tok"})

			bundle, err := c.GetPolicyBundle(context.Background(), testPolicyOwnerID, circleci.PolicyContextConfig)
			if err != nil {
				t.Fatalf("GetPolicyBundle returned error: %v", err)
			}

			policy, ok := bundle["a.rego"]
			if !ok {
				t.Fatalf("bundle has no entry for a.rego, got %#v", bundle)
			}
			if policy.Content != tc.wantContent {
				t.Errorf("Content = %q, want %q", policy.Content, tc.wantContent)
			}
			if policy.Name != tc.wantName {
				t.Errorf("Name = %q, want %q", policy.Name, tc.wantName)
			}
		})
	}
}

// TestGetPolicyBundleEmptyIsNotMissing covers a policy context with nothing in
// it: the API answers 200 with {}, which is a valid empty bundle rather than a
// missing resource.
func TestGetPolicyBundleEmptyIsNotMissing(t *testing.T) {
	t.Parallel()

	srv, _ := newRecordingServer(t, http.StatusOK, `{}`)
	c := circleci.New(circleci.Config{Host: srv.URL, Token: "tok"})

	bundle, err := c.GetPolicyBundle(context.Background(), testPolicyOwnerID, circleci.PolicyContextConfig)
	if err != nil {
		t.Fatalf("GetPolicyBundle returned error: %v", err)
	}
	if bundle == nil {
		t.Fatal("bundle is nil, want a non-nil empty bundle")
	}
	if len(bundle) != 0 {
		t.Errorf("len(bundle) = %d, want 0", len(bundle))
	}
}

// TestSetPolicyBundlePayload pins the upload body. The API takes a flat
// name-to-Rego map under "policies", and the field is never omitted: an empty
// map is how a context is emptied.
func TestSetPolicyBundlePayload(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		policies map[string]string
		dryRun   bool
		wantURI  string
		wantBody map[string]any
	}{
		{
			name:     "two policies",
			policies: map[string]string{"a.rego": "package org", "b.rego": "package org.b"},
			wantURI:  "/api/v2/owner/" + testPolicyOwnerID + "/context/config/policy-bundle",
			wantBody: map[string]any{"policies": map[string]any{
				"a.rego": "package org",
				"b.rego": "package org.b",
			}},
		},
		{
			name:     "empty map empties the context",
			policies: map[string]string{},
			wantURI:  "/api/v2/owner/" + testPolicyOwnerID + "/context/config/policy-bundle",
			wantBody: map[string]any{"policies": map[string]any{}},
		},
		{
			// A nil map must serialize as {} rather than null: null would leave
			// the existing bundle in place instead of clearing it.
			name:     "nil map is sent as an empty object",
			policies: nil,
			wantURI:  "/api/v2/owner/" + testPolicyOwnerID + "/context/config/policy-bundle",
			wantBody: map[string]any{"policies": map[string]any{}},
		},
		{
			name:     "dry run",
			policies: map[string]string{"a.rego": "package org"},
			dryRun:   true,
			wantURI:  "/api/v2/owner/" + testPolicyOwnerID + "/context/config/policy-bundle?dry=true",
			wantBody: map[string]any{"policies": map[string]any{"a.rego": "package org"}},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			var (
				gotMethod string
				gotURI    string
				gotBody   map[string]any
			)

			srv := newGovernanceServer(t, func(w http.ResponseWriter, r *http.Request) {
				gotMethod = r.Method
				gotURI = r.RequestURI
				_ = json.NewDecoder(r.Body).Decode(&gotBody)
				w.WriteHeader(http.StatusCreated)
				_, _ = w.Write([]byte(`{"created":["a.rego"],"modified":[],"deleted":["gone.rego"]}`))
			})
			c := circleci.New(circleci.Config{Host: srv.URL, Token: "tok"})

			diff, err := c.SetPolicyBundle(context.Background(),
				testPolicyOwnerID, circleci.PolicyContextConfig, tc.policies, tc.dryRun)
			if err != nil {
				t.Fatalf("SetPolicyBundle returned error: %v", err)
			}

			if gotMethod != http.MethodPost {
				t.Errorf("method = %q, want POST", gotMethod)
			}
			if gotURI != tc.wantURI {
				t.Errorf("raw request URI = %q, want %q", gotURI, tc.wantURI)
			}
			if !governanceJSONEqual(gotBody, tc.wantBody) {
				t.Errorf("request body = %#v, want %#v", gotBody, tc.wantBody)
			}

			if got, want := diff.Created, []string{"a.rego"}; !reflect.DeepEqual(got, want) {
				t.Errorf("diff.Created = %#v, want %#v", got, want)
			}
			if got, want := diff.Deleted, []string{"gone.rego"}; !reflect.DeepEqual(got, want) {
				t.Errorf("diff.Deleted = %#v, want %#v", got, want)
			}
		})
	}
}

// TestSetPolicyBundleReplacesWholeBundle is the round-trip that documents why
// the Terraform resource is a bundle rather than a policy: an upload that omits
// a policy deletes it, so two independently managed policies would clobber each
// other.
func TestSetPolicyBundleReplacesWholeBundle(t *testing.T) {
	t.Parallel()

	stored := map[string]string{}

	srv := newGovernanceServer(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost {
			var payload struct {
				Policies map[string]string `json:"policies"`
			}
			if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
				t.Errorf("upload body is not JSON: %v", err)
			}
			// Replacement, not merge: the request body becomes the bundle.
			stored = payload.Policies
			w.WriteHeader(http.StatusCreated)
			_, _ = w.Write([]byte(`{}`))

			return
		}

		documents := make(map[string]circleci.Policy, len(stored))
		for name, content := range stored {
			documents[name] = circleci.Policy{Name: name, Content: content}
		}
		_ = json.NewEncoder(w).Encode(documents)
	})
	c := circleci.New(circleci.Config{Host: srv.URL, Token: "tok"})

	ctx := context.Background()

	if _, err := c.SetPolicyBundle(ctx, testPolicyOwnerID, circleci.PolicyContextConfig, map[string]string{
		"a.rego": "package org.a",
		"b.rego": "package org.b",
	}, false); err != nil {
		t.Fatalf("SetPolicyBundle returned error: %v", err)
	}

	bundle, err := c.GetPolicyBundle(ctx, testPolicyOwnerID, circleci.PolicyContextConfig)
	if err != nil {
		t.Fatalf("GetPolicyBundle returned error: %v", err)
	}
	if got, want := bundle.Contents(), map[string]string{
		"a.rego": "package org.a",
		"b.rego": "package org.b",
	}; !reflect.DeepEqual(got, want) {
		t.Fatalf("bundle contents = %#v, want %#v", got, want)
	}

	// Re-uploading only a.rego drops b.rego, because the POST is a whole-bundle
	// replacement.
	if _, err := c.SetPolicyBundle(ctx, testPolicyOwnerID, circleci.PolicyContextConfig, map[string]string{
		"a.rego": "package org.a.v2",
	}, false); err != nil {
		t.Fatalf("SetPolicyBundle returned error: %v", err)
	}

	bundle, err = c.GetPolicyBundle(ctx, testPolicyOwnerID, circleci.PolicyContextConfig)
	if err != nil {
		t.Fatalf("GetPolicyBundle returned error: %v", err)
	}
	if got, want := bundle.Contents(), map[string]string{"a.rego": "package org.a.v2"}; !reflect.DeepEqual(got, want) {
		t.Errorf("bundle contents after partial upload = %#v, want %#v", got, want)
	}

	// And an empty upload empties the context, which is how the resource deletes.
	if _, err := c.SetPolicyBundle(ctx, testPolicyOwnerID, circleci.PolicyContextConfig, nil, false); err != nil {
		t.Fatalf("SetPolicyBundle returned error: %v", err)
	}

	bundle, err = c.GetPolicyBundle(ctx, testPolicyOwnerID, circleci.PolicyContextConfig)
	if err != nil {
		t.Fatalf("GetPolicyBundle returned error: %v", err)
	}
	if len(bundle) != 0 {
		t.Errorf("bundle after an empty upload = %#v, want empty", bundle)
	}
}

func TestGetPolicyDocument(t *testing.T) {
	t.Parallel()

	// The single-document route returns a FLAT policy object. The bundle route is
	// the one that returns a name-keyed map. This fixture previously used the map
	// shape, which is why the client decoding the wrong one went unnoticed —
	// against production it returned ErrNotFound for every policy that existed.
	srv, rec := newRecordingServer(t, http.StatusOK,
		`{"name":"a.rego","content":"package org","created_by":"alice","created_at":"2026-01-02T03:04:05Z"}`)
	c := circleci.New(circleci.Config{Host: srv.URL, Token: "tok"})

	policy, err := c.GetPolicyDocument(context.Background(),
		testPolicyOwnerID, circleci.PolicyContextConfig, "a.rego")
	if err != nil {
		t.Fatalf("GetPolicyDocument returned error: %v", err)
	}

	wantURI := "/api/v2/owner/" + testPolicyOwnerID + "/context/config/policy-bundle/a.rego"
	if rec.requestURI != wantURI {
		t.Errorf("raw request URI = %q, want %q", rec.requestURI, wantURI)
	}
	if policy.Content != "package org" {
		t.Errorf("Content = %q, want %q", policy.Content, "package org")
	}
	if policy.Name != "a.rego" {
		t.Errorf("Name = %q, want %q", policy.Name, "a.rego")
	}
	if policy.CreatedBy != "alice" {
		t.Errorf("CreatedBy = %q, want %q", policy.CreatedBy, "alice")
	}
}

func TestGetPolicyDocumentNotFound(t *testing.T) {
	t.Parallel()

	srv, _ := newRecordingServer(t, http.StatusNotFound, `{"error":"policy not found"}`)
	c := circleci.New(circleci.Config{Host: srv.URL, Token: "tok"})

	_, err := c.GetPolicyDocument(context.Background(),
		testPolicyOwnerID, circleci.PolicyContextConfig, "missing.rego")
	if err == nil {
		t.Fatal("GetPolicyDocument returned no error for a 404")
	}
	if !circleci.IsNotFound(err) {
		t.Errorf("IsNotFound(%v) = false, want true", err)
	}
}

func TestPolicyDecisionSettingsRoundTrip(t *testing.T) {
	t.Parallel()

	srv, rec := newRecordingServer(t, http.StatusOK, `{"enabled":true}`)
	c := circleci.New(circleci.Config{Host: srv.URL, Token: "tok"})

	settings, err := c.GetPolicyDecisionSettings(context.Background(),
		testPolicyOwnerID, circleci.PolicyContextConfig)
	if err != nil {
		t.Fatalf("GetPolicyDecisionSettings returned error: %v", err)
	}

	wantURI := "/api/v2/owner/" + testPolicyOwnerID + "/context/config/decision/settings"
	if rec.requestURI != wantURI {
		t.Errorf("raw request URI = %q, want %q", rec.requestURI, wantURI)
	}
	if settings.Enabled == nil || !*settings.Enabled {
		t.Errorf("Enabled = %v, want true", settings.Enabled)
	}
}

func TestSetPolicyDecisionSettingsPayload(t *testing.T) {
	t.Parallel()

	var (
		gotMethod string
		gotURI    string
		gotBody   map[string]any
	)

	srv := newGovernanceServer(t, func(w http.ResponseWriter, r *http.Request) {
		gotMethod = r.Method
		gotURI = r.RequestURI
		_ = json.NewDecoder(r.Body).Decode(&gotBody)
		_, _ = w.Write([]byte(`{"enabled":false}`))
	})
	c := circleci.New(circleci.Config{Host: srv.URL, Token: "tok"})

	// PolicyContextConfig, not PolicyContextCustom, even though the client would
	// happily put either in the path: the settings handlers validate this segment
	// against the single value "config" and then check it a second time, so a test
	// built on "custom" would be asserting the shape of a request the API always
	// answers 400 to.
	updated, err := c.SetPolicyDecisionSettings(context.Background(),
		testPolicyOwnerID, circleci.PolicyContextConfig,
		circleci.PolicyDecisionSettings{Enabled: ptr(false)})
	if err != nil {
		t.Fatalf("SetPolicyDecisionSettings returned error: %v", err)
	}

	if gotMethod != http.MethodPatch {
		t.Errorf("method = %q, want PATCH", gotMethod)
	}
	if want := "/api/v2/owner/" + testPolicyOwnerID + "/context/config/decision/settings"; gotURI != want {
		t.Errorf("raw request URI = %q, want %q", gotURI, want)
	}
	if !governanceJSONEqual(gotBody, map[string]any{"enabled": false}) {
		t.Errorf("request body = %#v, want {\"enabled\": false}", gotBody)
	}
	if updated.Enabled == nil || *updated.Enabled {
		t.Errorf("Enabled = %v, want false", updated.Enabled)
	}
}

// TestSetPolicyDecisionSettingsOmitsUnsetEnabled covers the omitempty on Enabled:
// a nil Enabled must not reach the wire as false, which would switch enforcement
// off.
//
// It does NOT mean the route supports a partial update, which is what the pointer
// was originally documented as being for. The handler validates the decoded body
// with a NotNil rule on enabled, so the empty object this produces is rejected
// with a 400 rather than leaving the current value alone — and the fake here
// answers exactly that, so nothing in this package can come to rely on a partial
// PATCH working. Both halves are asserted: the body must be empty (never false),
// and the call must fail.
func TestSetPolicyDecisionSettingsOmitsUnsetEnabled(t *testing.T) {
	t.Parallel()

	var gotBody map[string]any

	srv := newGovernanceServer(t, func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewDecoder(r.Body).Decode(&gotBody)

		if _, ok := gotBody["enabled"]; !ok {
			w.WriteHeader(http.StatusBadRequest)
			_, _ = w.Write([]byte(`{"error":"enabled: cannot be blank."}`))

			return
		}

		_, _ = w.Write([]byte(`{"enabled":true}`))
	})
	c := circleci.New(circleci.Config{Host: srv.URL, Token: "tok"})

	_, err := c.SetPolicyDecisionSettings(context.Background(),
		testPolicyOwnerID, circleci.PolicyContextConfig, circleci.PolicyDecisionSettings{})

	if !governanceJSONEqual(gotBody, map[string]any{}) {
		t.Errorf("request body = %#v, want an empty object rather than {\"enabled\": false}", gotBody)
	}

	if err == nil {
		t.Fatal("SetPolicyDecisionSettings succeeded with no enabled value; the real route " +
			"answers 400, so every caller must set Enabled")
	}
	if !circleci.HasStatus(err, http.StatusBadRequest) {
		t.Errorf("HasStatus(err, 400) = false, err = %v", err)
	}
	if got := circleci.Detail(err); !strings.Contains(got, "cannot be blank") {
		t.Errorf("Detail(err) = %q, want the service's own message", got)
	}
}

// TestPolicyContextIsNotACircleCIContext guards the naming: the {context} path
// segment is a policy context, and the only values CircleCI documents for it are
// "config" and "custom". A CircleCI context UUID would be a mistake.
func TestPolicyContextIsNotACircleCIContext(t *testing.T) {
	t.Parallel()

	if circleci.PolicyContextConfig != "config" {
		t.Errorf("PolicyContextConfig = %q, want %q", circleci.PolicyContextConfig, "config")
	}
	if circleci.PolicyContextCustom != "custom" {
		t.Errorf("PolicyContextCustom = %q, want %q", circleci.PolicyContextCustom, "custom")
	}
}

// TestPolicyContextCustomIsRejectedByEveryRoute states, in a test, the thing
// PolicyContextCustom exists to document: the constant is spelled correctly and
// the API refuses it everywhere.
//
// Every handler taking the {context} segment validates it against a single
// permitted value, "config" — the bundle routes with an In(internal.Config) rule
// and the decision-settings routes with In("config") plus a second explicit
// comparison. So "custom" is a 400 on all of them, and the fake answers that way
// rather than accepting it. The client deliberately does not validate the value
// itself: the API's message is clearer, and the schema refuses it at plan time.
func TestPolicyContextCustomIsRejectedByEveryRoute(t *testing.T) {
	t.Parallel()

	srv := newGovernanceServer(t, func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.Path, "/context/"+circleci.PolicyContextCustom+"/") {
			w.WriteHeader(http.StatusBadRequest)
			_, _ = w.Write([]byte(`{"error":"context: must be a valid value."}`))

			return
		}

		_, _ = w.Write([]byte(`{}`))
	})
	c := circleci.New(circleci.Config{Host: srv.URL, Token: "tok"})

	calls := map[string]func() error{
		"GetPolicyBundle": func() error {
			_, err := c.GetPolicyBundle(context.Background(),
				testPolicyOwnerID, circleci.PolicyContextCustom)

			return err
		},
		"GetPolicyDocument": func() error {
			_, err := c.GetPolicyDocument(context.Background(),
				testPolicyOwnerID, circleci.PolicyContextCustom, "policy.rego")

			return err
		},
		"SetPolicyBundle": func() error {
			_, err := c.SetPolicyBundle(context.Background(),
				testPolicyOwnerID, circleci.PolicyContextCustom, map[string]string{}, false)

			return err
		},
		"GetPolicyDecisionSettings": func() error {
			_, err := c.GetPolicyDecisionSettings(context.Background(),
				testPolicyOwnerID, circleci.PolicyContextCustom)

			return err
		},
		"SetPolicyDecisionSettings": func() error {
			_, err := c.SetPolicyDecisionSettings(context.Background(),
				testPolicyOwnerID, circleci.PolicyContextCustom,
				circleci.PolicyDecisionSettings{Enabled: ptr(true)})

			return err
		},
	}

	for name, call := range calls {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			err := call()
			if err == nil {
				t.Fatalf("%s succeeded with the %q policy context; the API answers 400",
					name, circleci.PolicyContextCustom)
			}
			if !circleci.HasStatus(err, http.StatusBadRequest) {
				t.Errorf("HasStatus(err, 400) = false, err = %v", err)
			}
		})
	}
}
