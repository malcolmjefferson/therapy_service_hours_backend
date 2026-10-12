// Package authz is the deny-by-default authorization policy for API handlers.
package authz

import (
	"context"

	"github.com/malcolmjefferson/therapy_service_hours_backend/internal/domain"
)

// Resource identifies a user-owned resource without carrying its private data.
type Resource struct{ UserID, OrganizationID string }

// Relationships reads accepted, same-organization supervision links from storage.
type Relationships interface {
	SupervisorOf(context.Context, string, string, string) (bool, error)
	Peers(context.Context, string, string, string) (bool, error)
}

// Self permits only the active actor's own profile or resource in their organization.
func Self(actor domain.Actor, target Resource) bool {
	return usable(actor) && target.UserID == actor.ID && target.OrganizationID == actor.OrganizationID
}

// Admin reflects the current database role. It does not grant access to entries or notes.
func Admin(actor domain.Actor) bool { return usable(actor) && actor.Admin }

// Supervisor reflects the current database role, not a token claim.
func Supervisor(actor domain.Actor) bool { return usable(actor) && actor.Supervisor }

// Supervise permits access only through an accepted supervision link in the same org.
func Supervise(ctx context.Context, actor domain.Actor, target Resource, links Relationships) (bool, error) {
	if !Supervisor(actor) || !sameOrg(actor, target) || links == nil || actor.ID == target.UserID {
		return false, nil
	}
	allowed, err := links.SupervisorOf(ctx, actor.OrganizationID, actor.ID, target.UserID)
	return allowed && err == nil, err
}

// PeerSchedule permits only a peer's restricted schedule DTO, never entries or notes.
func PeerSchedule(ctx context.Context, actor domain.Actor, target Resource, links Relationships) (bool, error) {
	if !usable(actor) || !sameOrg(actor, target) || links == nil || actor.ID == target.UserID {
		return false, nil
	}
	allowed, err := links.Peers(ctx, actor.OrganizationID, actor.ID, target.UserID)
	return allowed && err == nil, err
}

func usable(actor domain.Actor) bool {
	return actor.Active && actor.ID != "" && actor.OrganizationID != ""
}
func sameOrg(actor domain.Actor, target Resource) bool {
	return target.UserID != "" && target.OrganizationID == actor.OrganizationID
}
