package domain

import (
	"errors"
	"testing"
)

// The transition table is the decision this file records, so it is tested as a
// table: every legal edge, and every illegal one that a UI button could produce.
func TestNextStatus(t *testing.T) {
	legal := []struct {
		from   ProductStatus
		action LifecycleAction
		want   ProductStatus
	}{
		{StatusDraft, ActionPublish, StatusActive},
		{StatusDraft, ActionArchive, StatusArchived},
		{StatusActive, ActionArchive, StatusArchived},
		{StatusArchived, ActionRestore, StatusActive},
	}
	for _, c := range legal {
		got, err := NextStatus(c.from, c.action)
		if err != nil || got != c.want {
			t.Errorf("NextStatus(%s, %s) = (%s, %v), want (%s, nil)", c.from, c.action, got, err, c.want)
		}
	}

	illegal := []struct {
		from   ProductStatus
		action LifecycleAction
	}{
		{StatusActive, ActionPublish},   // already public
		{StatusArchived, ActionPublish}, // must be restored, not published
		{StatusArchived, ActionArchive}, // already withdrawn
		{StatusDraft, ActionRestore},    // never was archived
		{StatusActive, ActionRestore},   // already active
	}
	for _, c := range illegal {
		_, err := NextStatus(c.from, c.action)
		if !errors.Is(err, ErrInvalidTransition) {
			t.Errorf("NextStatus(%s, %s) = %v, want ErrInvalidTransition", c.from, c.action, err)
		}
		// The operator-facing envelope maps ErrConflict to 409, so the wrap chain
		// has to reach it — that is the whole point of wrapping.
		if !errors.Is(err, ErrConflict) {
			t.Errorf("NextStatus(%s, %s) must wrap ErrConflict, got %v", c.from, c.action, err)
		}
	}

	if _, err := NextStatus(StatusDraft, LifecycleAction("DELETE")); !errors.Is(err, ErrInvalidInput) {
		t.Errorf("unknown action = %v, want ErrInvalidInput", err)
	}
}

func TestProductStatusValid(t *testing.T) {
	for _, s := range []ProductStatus{StatusDraft, StatusActive, StatusArchived} {
		if !s.Valid() {
			t.Errorf("%s must be valid", s)
		}
	}
	for _, s := range []ProductStatus{"", "draft", "PUBLISHED", "DELETED"} {
		if s.Valid() {
			t.Errorf("%q must not be valid", s)
		}
	}
}

func TestVersionConflictWrapsConflict(t *testing.T) {
	if !errors.Is(ErrVersionConflict, ErrConflict) {
		t.Error("ErrVersionConflict must wrap ErrConflict so handlers answer 409")
	}
}
