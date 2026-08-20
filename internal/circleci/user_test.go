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

const testUserID = "a1b2c3d4-1111-2222-3333-444455556666"

// testUserBody is the shape production sends: a flat object with exactly
// these four keys. There is no envelope.
const testUserBody = `{
  "id": "` + testUserID + `",
  "login": "octocat",
  "name": "Mona Lisa Octocat",
  "avatar_url": "https://avatars.example.com/u/1"
}`

// newUserServer serves the user routes from handler and records every request.
func newUserServer(t *testing.T, handler http.HandlerFunc) (*circleci.Client, *[]string) {
	t.Helper()

	var seen []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen = append(seen, r.Method+" "+r.RequestURI)
		handler(w, r)
	}))
	t.Cleanup(srv.Close)

	return circleci.New(circleci.Config{Host: srv.URL, Token: "tok"}), &seen
}

func TestUserServiceCurrent(t *testing.T) {
	t.Parallel()

	client, seen := newUserServer(t, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(testUserBody))
	})

	user, err := client.Users().Current(context.Background())
	if err != nil {
		t.Fatalf("Current returned error: %v", err)
	}

	if len(*seen) != 1 {
		t.Fatalf("request count = %d, want 1", len(*seen))
	}
	if got := (*seen)[0]; got != "GET /api/v2/me" {
		t.Errorf("request = %q, want %q", got, "GET /api/v2/me")
	}

	if user.ID != testUserID {
		t.Errorf("id = %q, want %q", user.ID, testUserID)
	}
	if user.Login != "octocat" {
		t.Errorf("login = %q, want %q", user.Login, "octocat")
	}
	if user.Name != "Mona Lisa Octocat" {
		t.Errorf("name = %q, want %q", user.Name, "Mona Lisa Octocat")
	}
	if user.AvatarURL != "https://avatars.example.com/u/1" {
		t.Errorf("avatar url = %q, want the avatar_url value", user.AvatarURL)
	}
}

func TestUserServiceCurrentForbidden(t *testing.T) {
	t.Parallel()

	// The route answers 403, not 401, for a token that is not a user token. That
	// must not be mistaken for a missing user, or the data source would silently
	// report nothing instead of explaining the token is wrong.
	client, _ := newUserServer(t, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusForbidden)
		_, _ = w.Write([]byte(`{"message":"Forbidden."}`))
	})

	_, err := client.Users().Current(context.Background())
	if err == nil {
		t.Fatal("Current returned no error for a 403, want one")
	}
	if !circleci.IsUnauthorized(err) {
		t.Errorf("IsUnauthorized(%v) = false, want true", err)
	}
	if circleci.IsNotFound(err) {
		t.Error("IsNotFound() = true for a 403, want false")
	}
	if detail := circleci.Detail(err); !strings.Contains(detail, "Forbidden.") {
		t.Errorf("Detail() = %q, want it to carry the server message", detail)
	}
}

func TestUserServiceCurrentEmptyBodyIsNotFound(t *testing.T) {
	t.Parallel()

	client, _ := newUserServer(t, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{}`))
	})

	if _, err := client.Users().Current(context.Background()); !circleci.IsNotFound(err) {
		t.Errorf("Current error = %v, want a not found error", err)
	}
}

func TestUserServiceGet(t *testing.T) {
	t.Parallel()

	client, seen := newUserServer(t, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(testUserBody))
	})

	user, err := client.Users().Get(context.Background(), testUserID)
	if err != nil {
		t.Fatalf("Get returned error: %v", err)
	}
	if user.ID != testUserID {
		t.Errorf("id = %q, want %q", user.ID, testUserID)
	}

	// Note /user/{id}, singular, not /users/{id}.
	want := "GET /api/v2/user/" + testUserID
	if got := (*seen)[0]; got != want {
		t.Errorf("request = %q, want %q", got, want)
	}
}

func TestUserServiceGetNotFound(t *testing.T) {
	t.Parallel()

	client, _ := newUserServer(t, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte(`{"message":"Not found."}`))
	})

	if _, err := client.Users().Get(context.Background(), testUserID); !circleci.IsNotFound(err) {
		t.Errorf("Get error = %v, want a not found error", err)
	}
}

func TestUserServiceGetEscapesID(t *testing.T) {
	t.Parallel()

	client, seen := newUserServer(t, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(testUserBody))
	})

	if _, err := client.Users().Get(context.Background(), "id/../me"); err != nil {
		t.Fatalf("Get returned error: %v", err)
	}

	want := "GET /api/v2/user/id%2F..%2Fme"
	if got := (*seen)[0]; got != want {
		t.Errorf("request = %q, want %q", got, want)
	}
}

func TestUserServiceListCollaborations(t *testing.T) {
	t.Parallel()

	// A bare JSON array, not the items/next_page_token envelope: the API
	// builds the whole set per request, so the route does not paginate. The second
	// entry has a null id, which is the documented case for an organization that
	// exists on the VCS but has never been used on CircleCI.
	//
	// The VCS key is written "vcs_type", because that is what production sends —
	// checked against Cloud over 38 collaborations, github and circleci alike, with
	// no hyphenated key anywhere in the payload. The published OpenAPI document
	// declares "vcs-type" instead; TestCollaborationAcceptsBothVCSTypeSpellings
	// covers that spelling, which the client still accepts as a fallback. See
	// Collaboration.UnmarshalJSON.
	const body = `[
      {
        "id": "11111111-1111-1111-1111-111111111111",
        "vcs_type": "circleci",
        "name": "acme",
        "slug": "circleci/11111111-1111-1111-1111-111111111111",
        "avatar_url": "https://avatars.example.com/u/2"
      },
      {
        "id": null,
        "vcs_type": "github",
        "name": "unknown-to-circleci",
        "slug": "gh/unknown-to-circleci",
        "avatar_url": "https://avatars.example.com/u/3"
      }
    ]`

	client, seen := newUserServer(t, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(body))
	})

	collaborations, err := client.Users().ListCollaborations(context.Background())
	if err != nil {
		t.Fatalf("ListCollaborations returned error: %v", err)
	}

	if len(*seen) != 1 {
		t.Fatalf("request count = %d, want 1 (the route does not paginate)", len(*seen))
	}
	if got := (*seen)[0]; got != "GET /api/v2/me/collaborations" {
		t.Errorf("request = %q, want %q", got, "GET /api/v2/me/collaborations")
	}

	if len(collaborations) != 2 {
		t.Fatalf("collaboration count = %d, want 2", len(collaborations))
	}

	first := collaborations[0]
	if first.ID == nil || *first.ID != "11111111-1111-1111-1111-111111111111" {
		t.Errorf("first id = %v, want the UUID", first.ID)
	}
	// This assertion is the whole point of the fixture above: with the key decoded
	// under the wrong spelling the field is "" and circleci_user_collaborations
	// reports no VCS type for any organization.
	if first.VCSType != "circleci" {
		t.Errorf("first vcs type = %q, want %q (decoded from vcs_type)", first.VCSType, "circleci")
	}
	if first.Slug != "circleci/11111111-1111-1111-1111-111111111111" {
		t.Errorf("first slug = %q, want the standalone slug", first.Slug)
	}
	if first.AvatarURL != "https://avatars.example.com/u/2" {
		t.Errorf("first avatar url = %q, want the avatar_url value", first.AvatarURL)
	}

	// A null id must stay distinguishable from an id the server sent as "".
	if collaborations[1].ID != nil {
		t.Errorf("second id = %v, want nil for a null id", collaborations[1].ID)
	}
}

// TestCollaborationAcceptsBothVCSTypeSpellings covers the one key in the v2
// surface whose published spelling and real spelling differ.
//
// The OpenAPI document for GET /api/v2/me/collaborations declares a required
// "vcs-type"; Cloud sends "vcs_type", verified across 38 collaborations. This test
// pins production as the primary and the document as a tolerated fallback, so
// neither belief can leave the field empty. It also pins the surrounding fields,
// because the fallback is implemented in an UnmarshalJSON and a field added to
// Collaboration without a thought for it would otherwise stop decoding silently.
//
// Organization.VCSType is asserted alongside because the two are one letter apart
// and describe the same concept, so the tags invite being "tidied" into agreement.
func TestCollaborationAcceptsBothVCSTypeSpellings(t *testing.T) {
	t.Parallel()

	for name, body := range map[string]string{
		"vcs_type, what production sends": `{"id":"11111111-1111-1111-1111-111111111111",
			"vcs_type":"github","name":"acme","slug":"gh/acme",
			"avatar_url":"https://avatars.example.com/u/2"}`,
		"vcs-type, what the published document declares": `{"id":"11111111-1111-1111-1111-111111111111",
			"vcs-type":"github","name":"acme","slug":"gh/acme",
			"avatar_url":"https://avatars.example.com/u/2"}`,
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			var collaboration circleci.Collaboration
			if err := json.Unmarshal([]byte(body), &collaboration); err != nil {
				t.Fatalf("decoding a collaboration: %v", err)
			}

			if collaboration.VCSType != "github" {
				t.Errorf("vcs type = %q, want %q — the field is empty and "+
					"circleci_user_collaborations reports no VCS type for any organization",
					collaboration.VCSType, "github")
			}
			// Every other field must survive the custom decoder.
			if collaboration.ID == nil || *collaboration.ID != "11111111-1111-1111-1111-111111111111" {
				t.Errorf("id = %v, want the UUID", collaboration.ID)
			}
			if collaboration.Name != "acme" || collaboration.Slug != "gh/acme" ||
				collaboration.AvatarURL != "https://avatars.example.com/u/2" {
				t.Errorf("name/slug/avatar = %q/%q/%q, want them decoded from their own tags",
					collaboration.Name, collaboration.Slug, collaboration.AvatarURL)
			}
		})
	}

	organizationField, ok := reflect.TypeOf(circleci.Organization{}).FieldByName("VCSType")
	if !ok {
		t.Fatal("Organization has no VCSType field")
	}
	if got := organizationField.Tag.Get("json"); got != "vcs_type" {
		t.Errorf("Organization.VCSType json tag = %q, want %q: the organization routes use "+
			"snake_case, as /me/collaborations turns out to as well", got, "vcs_type")
	}
}

func TestUserServiceListCollaborationsEmpty(t *testing.T) {
	t.Parallel()

	client, _ := newUserServer(t, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`[]`))
	})

	collaborations, err := client.Users().ListCollaborations(context.Background())
	if err != nil {
		t.Fatalf("ListCollaborations returned error: %v", err)
	}
	if len(collaborations) != 0 {
		t.Errorf("collaboration count = %d, want 0", len(collaborations))
	}
}
