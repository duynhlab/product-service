package domain

import (
	"fmt"
	"time"
)

// The catalog lifecycle (RFC-0023 slice B). Three states rather than a published
// boolean, because "not published yet" and "no longer sold" are different answers:
// the first is an operator's unfinished work, the second is a decision that must
// survive in the record. Public catalog reads show ACTIVE only; the operator sees
// all three.
//
// Transitions are COMMANDS, never a status setter — the same discipline
// order-service uses for its own FSM. A generic "set status to X" endpoint would
// make every illegal edge reachable by construction and leave the audit unable to
// say what the operator meant.

// ProductStatus is the stored lifecycle state of a catalog row.
type ProductStatus string

const (
	// StatusDraft is a product an operator created but has not published. It is
	// invisible to public reads.
	StatusDraft ProductStatus = "DRAFT"
	// StatusActive is a product the public catalog serves. Rows that predate the
	// lifecycle migration are ACTIVE by default, so nothing became invisible when
	// the column arrived.
	StatusActive ProductStatus = "ACTIVE"
	// StatusArchived is a product withdrawn from sale. Its page 404s, but its
	// PRICE still resolves over gRPC — see the note on BatchGetCurrentPrices in
	// docs/api/product.md: a cart holding a just-archived product must still price
	// correctly, and checkout's price re-validation is the guard there.
	StatusArchived ProductStatus = "ARCHIVED"
)

// Valid reports whether s is a state this service stores.
func (s ProductStatus) Valid() bool {
	switch s {
	case StatusDraft, StatusActive, StatusArchived:
		return true
	default:
		return false
	}
}

// LifecycleAction is one of the three transition commands.
type LifecycleAction string

const (
	// ActionPublish makes a draft public: DRAFT → ACTIVE.
	ActionPublish LifecycleAction = "PUBLISH"
	// ActionArchive withdraws a product from sale: DRAFT | ACTIVE → ARCHIVED.
	ActionArchive LifecycleAction = "ARCHIVE"
	// ActionRestore returns an archived product to sale: ARCHIVED → ACTIVE.
	ActionRestore LifecycleAction = "RESTORE"
)

// ErrInvalidTransition is the illegal-edge error. Callers map it to 409 with code
// INVALID_TRANSITION so an operator sees which edge was refused rather than a bare
// conflict.
var ErrInvalidTransition = fmt.Errorf("invalid lifecycle transition: %w", ErrConflict)

// ErrVersionConflict means the edit carried a version other than the row's current
// one: someone else changed the product since it was read. Callers map it to 409.
var ErrVersionConflict = fmt.Errorf("product changed since it was read: %w", ErrConflict)

// NextStatus resolves the target state for an action, or ErrInvalidTransition when
// the edge does not exist. The whole FSM is this table:
//
//	DRAFT    --publish--> ACTIVE
//	DRAFT    --archive--> ARCHIVED
//	ACTIVE   --archive--> ARCHIVED
//	ARCHIVED --restore--> ACTIVE
//
// Re-running a command that already happened is an invalid edge, not a no-op: the
// operator asked for a change the row cannot make, and saying so is more useful
// than a silent success. Command retries are handled by the idempotency key, which
// replays the original answer instead of re-evaluating the edge.
func NextStatus(from ProductStatus, action LifecycleAction) (ProductStatus, error) {
	switch action {
	case ActionPublish:
		if from == StatusDraft {
			return StatusActive, nil
		}
	case ActionArchive:
		if from == StatusDraft || from == StatusActive {
			return StatusArchived, nil
		}
	case ActionRestore:
		if from == StatusArchived {
			return StatusActive, nil
		}
	default:
		return "", fmt.Errorf("unknown action %q: %w", action, ErrInvalidInput)
	}
	return "", fmt.Errorf("cannot %s a %s product: %w", action, from, ErrInvalidTransition)
}

// Category is a flat catalog grouping. No hierarchy: the table has no parent
// column and adding one is its own decision (RFC-0023 keeps categories flat).
type Category struct {
	ID          int       `json:"id"`
	Name        string    `json:"name"`
	Description string    `json:"description"`
	CreatedAt   time.Time `json:"created_at"`
	UpdatedAt   time.Time `json:"updated_at"`
}

// AuditEntry is the durable record of one privileged catalog write. It commits in
// the same transaction as the write it describes (ADR-047), so a successful
// command without its audit row cannot exist.
type AuditEntry struct {
	TargetType    string
	TargetID      int
	Action        string
	ActorSub      string
	Reason        string
	ChangedFields map[string]any
	VersionBefore *int64
	VersionAfter  *int64
	RequestID     string
}
