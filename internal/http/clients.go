package httpapi

import (
	"context"
	"errors"

	openapi_types "github.com/oapi-codegen/runtime/types"

	"github.com/malcolmjefferson/therapy_service_hours_backend/internal/domain"
	"github.com/malcolmjefferson/therapy_service_hours_backend/internal/http/gen"
)

// ClientService is the clients use case the handlers call (S2-07). The service authorizes the
// actor and validates the input; handlers only translate between HTTP and it (GO-10).
type ClientService interface {
	List(ctx context.Context, actor domain.Actor, page, pageSize int32) (domain.ClientPage, error)
	Create(ctx context.Context, actor domain.Actor, name string) (domain.Client, error)
	Rename(ctx context.Context, actor domain.Actor, clientID, name string) (domain.Client, error)
	Delete(ctx context.Context, actor domain.Actor, clientID string, confirmDetach bool) error
}

// ListClients returns the caller's own clients, one page.
func (s *server) ListClients(ctx context.Context, req gen.ListClientsRequestObject) (gen.ListClientsResponseObject, error) {
	var page, pageSize int32
	if req.Params.Page != nil {
		page = *req.Params.Page
	}
	if req.Params.PageSize != nil {
		pageSize = *req.Params.PageSize
	}
	result, err := s.clients.List(ctx, ActorFrom(ctx), page, pageSize)
	if err != nil {
		return nil, err //nolint:wrapcheck // WriteError maps domain errors (GO-33).
	}
	items := make([]gen.Client, 0, len(result.Items))
	for _, c := range result.Items {
		items = append(items, clientDTO(c))
	}
	return gen.ListClients200JSONResponse{
		Items: items,
		Page:  gen.PageInfo{Page: result.Page, PageSize: result.PageSize, TotalItems: result.Total},
	}, nil
}

// CreateClient saves a client. The Idempotency-Key header is required by the contract; replaying
// a key is S2-09's middleware. Until then a retry answers 409 client-name-taken with the existing
// client, which the form selects.
func (s *server) CreateClient(ctx context.Context, req gen.CreateClientRequestObject) (gen.CreateClientResponseObject, error) {
	if req.Body == nil {
		return nil, errors.New("create client: body was not decoded")
	}
	client, err := s.clients.Create(ctx, ActorFrom(ctx), req.Body.Name)
	if err != nil {
		return nil, err //nolint:wrapcheck // WriteError maps domain errors (GO-33).
	}
	return gen.CreateClient201JSONResponse(clientDTO(client)), nil
}

// RenameClient renames one of the caller's clients.
func (s *server) RenameClient(ctx context.Context, req gen.RenameClientRequestObject) (gen.RenameClientResponseObject, error) {
	if req.Body == nil {
		return nil, errors.New("rename client: body was not decoded")
	}
	client, err := s.clients.Rename(ctx, ActorFrom(ctx), req.ClientId.String(), req.Body.Name)
	if err != nil {
		return nil, err //nolint:wrapcheck // WriteError maps domain errors (GO-33).
	}
	return gen.RenameClient200JSONResponse(clientDTO(client)), nil
}

// DeleteClient deletes one of the caller's clients, detaching it from entries only when confirmed.
func (s *server) DeleteClient(ctx context.Context, req gen.DeleteClientRequestObject) (gen.DeleteClientResponseObject, error) {
	confirm := req.Params.ConfirmDetach != nil && *req.Params.ConfirmDetach
	if err := s.clients.Delete(ctx, ActorFrom(ctx), req.ClientId.String(), confirm); err != nil {
		return nil, err //nolint:wrapcheck // WriteError maps domain errors (GO-33).
	}
	return gen.DeleteClient204Response{}, nil
}

// clientDTO is the explicit response shape for a client (GO-54).
func clientDTO(c domain.Client) gen.Client {
	var id openapi_types.UUID
	// The store only returns identifiers PostgreSQL produced, so a failure leaves the zero UUID
	// rather than a half-valid one; the contract test would catch it.
	_ = id.UnmarshalText([]byte(c.ID))
	return gen.Client{Id: id, Name: c.Name}
}
