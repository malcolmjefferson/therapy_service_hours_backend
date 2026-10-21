package entries

import (
	"context"
	"fmt"

	"github.com/malcolmjefferson/therapy_service_hours_backend/internal/authz"
	"github.com/malcolmjefferson/therapy_service_hours_backend/internal/domain"
)

// Writer reads and changes single entries for the service. Every method is scoped to userID in
// SQL and answers domain.ErrNotFound for an entry that is missing or belongs to someone else, so
// the two cases cannot be told apart (GO-36, SEC-12).
type Writer interface {
	// GetEntry returns one of userID's entries with its category and clients.
	GetEntry(ctx context.Context, userID, entryID string) (domain.Entry, error)
	// CreateEntry stores a new pending entry and attaches its clients in one transaction. A session
	// that overlaps another non-rejected one returns *domain.EntryOverlapError; a category or client
	// that does not exist or is not the caller's returns a *domain.ValidationError.
	CreateEntry(ctx context.Context, userID string, v Validated) (domain.Entry, error)
	// UpdateEntry replaces every editable field and the client set of a pending or rejected entry,
	// and sets a rejected entry back to pending, in one transaction. An approved entry returns
	// *domain.EntryLockedError; the errors of CreateEntry apply as well.
	UpdateEntry(ctx context.Context, userID, entryID string, v Validated) (domain.Entry, error)
	// DeleteEntry removes a pending or rejected entry. An approved entry returns *domain.EntryLockedError.
	DeleteEntry(ctx context.Context, userID, entryID string) error
}

// authorizeSelf allows an active actor to work on their own entries only. Entries have no sharing:
// supervisors and admins reach other users' entries through their own routes, never through these
// (CLAUDE.md §5).
func authorizeSelf(actor domain.Actor) error {
	if !authz.Self(actor, authz.Resource{UserID: actor.ID, OrganizationID: actor.OrganizationID}) {
		return domain.ErrForbidden
	}
	return nil
}

// Get returns one of the actor's own entries.
func (s *Service) Get(ctx context.Context, actor domain.Actor, entryID string) (domain.Entry, error) {
	if err := authorizeSelf(actor); err != nil {
		return domain.Entry{}, err
	}
	entry, err := s.store.GetEntry(ctx, actor.ID, entryID)
	if err != nil {
		return domain.Entry{}, fmt.Errorf("get entry: %w", err)
	}
	return entry, nil
}

// Create validates the input and stores it as a new pending entry owned by the actor. The error is a
// *domain.ValidationError for bad fields and a *domain.EntryOverlapError for a clashing session.
func (s *Service) Create(ctx context.Context, actor domain.Actor, in Input) (domain.Entry, error) {
	if err := authorizeSelf(actor); err != nil {
		return domain.Entry{}, err
	}
	v, err := Validate(in)
	if err != nil {
		return domain.Entry{}, err
	}
	entry, err := s.store.CreateEntry(ctx, actor.ID, v)
	if err != nil {
		return domain.Entry{}, fmt.Errorf("create entry: %w", err)
	}
	return entry, nil
}

// Update replaces the editable fields of one of the actor's pending or rejected entries. Saving a
// rejected entry makes it pending again (CLAUDE.md §7.1). The input is validated before the entry is
// looked up, so a malformed request never reveals whether the id exists.
func (s *Service) Update(ctx context.Context, actor domain.Actor, entryID string, in Input) (domain.Entry, error) {
	if err := authorizeSelf(actor); err != nil {
		return domain.Entry{}, err
	}
	v, err := Validate(in)
	if err != nil {
		return domain.Entry{}, err
	}
	entry, err := s.store.UpdateEntry(ctx, actor.ID, entryID, v)
	if err != nil {
		return domain.Entry{}, fmt.Errorf("update entry: %w", err)
	}
	return entry, nil
}

// Delete removes one of the actor's pending or rejected entries.
func (s *Service) Delete(ctx context.Context, actor domain.Actor, entryID string) error {
	if err := authorizeSelf(actor); err != nil {
		return err
	}
	if err := s.store.DeleteEntry(ctx, actor.ID, entryID); err != nil {
		return fmt.Errorf("delete entry: %w", err)
	}
	return nil
}
