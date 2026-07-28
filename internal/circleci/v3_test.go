// Copyright (c) CircleCI
// SPDX-License-Identifier: MPL-2.0

package circleci_test

import (
	"context"
	"errors"
	"testing"

	"terraform-provider-circleci/internal/circleci"
)

func ptr[T any](v T) *T { return &v }

func TestDrainV3(t *testing.T) {
	t.Parallel()

	pages := map[string]circleci.List[string]{
		"": {
			Data: []string{"a", "b"},
			Page: circleci.Page{Next: ptr("cur-2")},
		},
		"cur-2": {
			Data: []string{"c"},
			Page: circleci.Page{Next: nil},
		},
	}

	var requested []string
	got, err := circleci.DrainV3(context.Background(),
		func(_ context.Context, cursor string) (circleci.List[string], error) {
			requested = append(requested, cursor)

			return pages[cursor], nil
		})
	if err != nil {
		t.Fatalf("DrainV3 returned error: %v", err)
	}

	if want := []string{"a", "b", "c"}; !equal(got, want) {
		t.Errorf("items = %v, want %v", got, want)
	}
	if want := []string{"", "cur-2"}; !equal(requested, want) {
		t.Errorf("cursors requested = %v, want %v", requested, want)
	}
}

func TestDrainV3StopsOnEmptyPageDespiteCursor(t *testing.T) {
	t.Parallel()

	// A server that always echoes a next cursor would otherwise loop forever.
	calls := 0
	got, err := circleci.DrainV3(context.Background(),
		func(_ context.Context, _ string) (circleci.List[string], error) {
			calls++

			return circleci.List[string]{
				Data: nil,
				Page: circleci.Page{Next: ptr("always")},
			}, nil
		})
	if err != nil {
		t.Fatalf("DrainV3 returned error: %v", err)
	}

	if calls != 1 {
		t.Errorf("fetch called %d times, want 1", calls)
	}
	if len(got) != 0 {
		t.Errorf("items = %v, want none", got)
	}
}

func TestDrainV3PropagatesError(t *testing.T) {
	t.Parallel()

	sentinel := errors.New("boom")
	_, err := circleci.DrainV3(context.Background(),
		func(_ context.Context, _ string) (circleci.List[string], error) {
			return circleci.List[string]{}, sentinel
		})

	if !errors.Is(err, sentinel) {
		t.Errorf("err = %v, want it to wrap %v", err, sentinel)
	}
}

func TestDrainV2(t *testing.T) {
	t.Parallel()

	pages := map[string]circleci.PaginatedResponse[string]{
		"":      {Items: []string{"a"}, NextPageToken: "tok-2"},
		"tok-2": {Items: []string{"b", "c"}, NextPageToken: ""},
	}

	var requested []string
	got, err := circleci.DrainV2(context.Background(),
		func(_ context.Context, token string) (circleci.PaginatedResponse[string], error) {
			requested = append(requested, token)

			return pages[token], nil
		})
	if err != nil {
		t.Fatalf("DrainV2 returned error: %v", err)
	}

	if want := []string{"a", "b", "c"}; !equal(got, want) {
		t.Errorf("items = %v, want %v", got, want)
	}
	if want := []string{"", "tok-2"}; !equal(requested, want) {
		t.Errorf("tokens requested = %v, want %v", requested, want)
	}
}

func TestDrainV2StopsOnEmptyPageDespiteToken(t *testing.T) {
	t.Parallel()

	calls := 0
	_, err := circleci.DrainV2(context.Background(),
		func(_ context.Context, _ string) (circleci.PaginatedResponse[string], error) {
			calls++

			return circleci.PaginatedResponse[string]{Items: nil, NextPageToken: "always"}, nil
		})
	if err != nil {
		t.Fatalf("DrainV2 returned error: %v", err)
	}

	if calls != 1 {
		t.Errorf("fetch called %d times, want 1", calls)
	}
}

func TestListNextCursor(t *testing.T) {
	t.Parallel()

	var noNext circleci.List[string]
	if got := noNext.NextCursor(); got != "" {
		t.Errorf("NextCursor() with nil next = %q, want empty", got)
	}

	withNext := circleci.List[string]{
		Page: circleci.Page{Next: ptr("cur")},
	}
	if got := withNext.NextCursor(); got != "cur" {
		t.Errorf("NextCursor() = %q, want %q", got, "cur")
	}
}

func equal[T comparable](a, b []T) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}

	return true
}
