// Package clients is the use case for a user's saved clients and groups (S2-07). Clients are
// strictly private to their owner: every operation requires the active actor to act on themselves,
// and the store scopes every statement to that user.
package clients

import (
	"context"
	"errors"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/malcolmjefferson/therapy_service_hours_backend/internal/authz"
	"github.com/malcolmjefferson/therapy_service_hours_backend/internal/domain"
)

const (
	// MaxNameLength is the longest client name in characters (SEC-26); the column is varchar(64).
	MaxNameLength = 64
	// DefaultPageSize is the list page size when the caller names none (GO-56, ARC-16).
	DefaultPageSize int32 = 50
	// MaxPageSize is the largest list page; bigger requests are clamped to it.
	MaxPageSize int32 = 200
)

// Store persists clients. Every method is scoped to userID and returns domain.ErrNotFound for a
// client that is missing or belongs to someone else (GO-36).
type Store interface {
	// List returns one page, ordered by name ignoring case, then id, and the total count.
	List(ctx context.Context, userID string, limit, offset int32) ([]domain.Client, int64, error)
	// Create saves a client. A name the user already has, ignoring case, returns *domain.ClientNameTakenError.
	Create(ctx context.Context, userID, name string) (domain.Client, error)
	// Rename changes a client's name. Another client's name returns *domain.ClientNameTakenError;
	// changing only the case of the client's own name is allowed.
	Rename(ctx context.Context, userID, clientID, name string) (domain.Client, error)
	// Delete removes a client. If entries use it and detach is false it returns
	// *domain.ClientInUseError; with detach it removes the client from those entries first.
	Delete(ctx context.Context, userID, clientID string, detach bool) error
}

// Service implements the clients use cases.
type Service struct {
	store Store
}

// New returns a Service over store.
func New(store Store) (*Service, error) {
	if store == nil {
		return nil, errors.New("clients service requires a store")
	}
	return &Service{store: store}, nil
}

// List returns the actor's own clients. A zero page or page size selects the default.
func (s *Service) List(ctx context.Context, actor domain.Actor, page, pageSize int32) (domain.ClientPage, error) {
	if err := authorize(actor); err != nil {
		return domain.ClientPage{}, err
	}
	if page < 1 {
		page = 1
	}
	if pageSize < 1 {
		pageSize = DefaultPageSize
	}
	pageSize = min(pageSize, MaxPageSize)
	items, total, err := s.store.List(ctx, actor.ID, pageSize, (page-1)*pageSize)
	if err != nil {
		return domain.ClientPage{}, err //nolint:wrapcheck // The store already wraps with context.
	}
	return domain.ClientPage{Items: items, Page: page, PageSize: pageSize, Total: total}, nil
}

// Create saves a new client for the actor.
func (s *Service) Create(ctx context.Context, actor domain.Actor, name string) (domain.Client, error) {
	if err := authorize(actor); err != nil {
		return domain.Client{}, err
	}
	name, err := validName(name)
	if err != nil {
		return domain.Client{}, err
	}
	return s.store.Create(ctx, actor.ID, name) //nolint:wrapcheck // The store already wraps with context.
}

// Rename changes the name of one of the actor's clients.
func (s *Service) Rename(ctx context.Context, actor domain.Actor, clientID, name string) (domain.Client, error) {
	if err := authorize(actor); err != nil {
		return domain.Client{}, err
	}
	name, err := validName(name)
	if err != nil {
		return domain.Client{}, err
	}
	return s.store.Rename(ctx, actor.ID, clientID, name) //nolint:wrapcheck // The store already wraps with context.
}

// Delete removes one of the actor's clients. Entries that still use it block the delete unless
// confirmDetach is true.
func (s *Service) Delete(ctx context.Context, actor domain.Actor, clientID string, confirmDetach bool) error {
	if err := authorize(actor); err != nil {
		return err
	}
	return s.store.Delete(ctx, actor.ID, clientID, confirmDetach) //nolint:wrapcheck // The store already wraps with context.
}

// authorize allows only the active actor acting on their own data in their organization (GO-59).
func authorize(actor domain.Actor) error {
	if !authz.Self(actor, authz.Resource{UserID: actor.ID, OrganizationID: actor.OrganizationID}) {
		return domain.ErrForbidden
	}
	return nil
}

// validName trims the name and checks it against the same rules as the contract and the column:
// 1 to 64 characters, valid UTF-8, no control characters. The error never echoes the value.
func validName(raw string) (string, error) {
	name := strings.TrimSpace(raw)
	var msg string
	switch {
	case !utf8.ValidString(name):
		msg = "must be valid text"
	case name == "":
		msg = "is required"
	case utf8.RuneCountInString(name) > MaxNameLength:
		msg = "must be at most 64 characters"
	case strings.ContainsFunc(name, unicode.IsControl):
		msg = "must not contain control characters"
	default:
		return name, nil
	}
	return "", domain.NewValidationError(domain.FieldError{Field: "name", Message: msg})
}
