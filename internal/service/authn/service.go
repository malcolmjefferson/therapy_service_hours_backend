// Package authn resolves validated Gateway identity into a database-backed actor.
package authn

import (
	"context"
	"errors"
	"net/mail"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/malcolmjefferson/therapy_service_hours_backend/internal/domain"
)

// ErrUnauthorized means ID-token identity attributes are absent or invalid.
var ErrUnauthorized = errors.New("unauthorized")

// Store binds identity and accepts an invitation atomically, or reads a returning actor.
type Store interface {
	Resolve(context.Context, domain.Identity, time.Time) (domain.Actor, error)
}

// Service validates trusted claims before storage. It never parses bearer tokens.
type Service struct {
	store Store
	now   func() time.Time
}

// New constructs the authentication use case with an injected clock.
func New(store Store, now func() time.Time) (*Service, error) {
	if store == nil || now == nil {
		return nil, errors.New("authentication requires storage and clock")
	}
	return &Service{store: store, now: now}, nil
}

// Resolve accepts only verified ID-token attributes. Group claims are ignored.
func (s *Service) Resolve(ctx context.Context, claims map[string]string) (domain.Actor, error) {
	i := domain.Identity{Subject: claims["sub"], Email: strings.ToLower(claims["email"]), FirstName: claims["given_name"], LastName: claims["family_name"]}
	if claims["token_use"] != "id" || claims["email_verified"] != "true" || !validText(i.Subject, 128, false) || !validText(i.Email, 254, false) || !validText(i.FirstName, 200, true) || !validText(i.LastName, 200, true) {
		return domain.Actor{}, ErrUnauthorized
	}
	address, err := mail.ParseAddress(i.Email)
	if err != nil || address.Address != i.Email {
		return domain.Actor{}, ErrUnauthorized
	}
	actor, err := s.store.Resolve(ctx, i, s.now().UTC())
	if err != nil {
		return domain.Actor{}, err
	}
	if !actor.Active || actor.ID == "" || actor.OrganizationID == "" {
		return domain.Actor{}, domain.ErrForbidden
	}
	return actor, nil
}

func validText(value string, limit int, emptyOK bool) bool {
	return utf8.ValidString(value) && utf8.RuneCountInString(value) <= limit && (emptyOK || value != "") && value == strings.TrimSpace(value) && !strings.ContainsFunc(value, unicode.IsControl)
}
