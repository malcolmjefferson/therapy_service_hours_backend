package httpapi

import (
	"encoding/json"
	"strings"
	"testing"
)

const (
	linkID    = "00000000-0000-4000-8000-0000000000a1"
	personID  = "00000000-0000-4000-8000-0000000000b1"
	otherUser = "00000000-0000-4000-8000-0000000000b2"
)

// TestSupervisionContract_Operations pins the S6-02 shape the frontend (S6-09 to S6-12) builds
// against. These checks read api/openapi.yaml itself; the handlers arrive in S6-03 to S6-07.
func TestSupervisionContract_Operations(t *testing.T) {
	t.Parallel()
	spec := loadFullSpec(t)

	tests := []struct {
		path, method, operationID string
		idempotent                bool
		statuses                  []string
	}{
		{"/supervisors", "GET", "searchSupervisors", false, []string{"200", "400", "401", "403"}},
		{"/supervision-links", "GET", "listSupervisionLinks", false, []string{"200", "400", "401", "403"}},
		{"/supervision-links", "POST", "createSupervisionLink", true, []string{"201", "400", "401", "403", "404", "409"}},
		{"/supervision-links/{linkId}/cancel", "POST", "cancelSupervisionLink", false, []string{"200", "401", "403", "404", "409"}},
		{"/supervision-links/{linkId}/accept", "POST", "acceptSupervisionLink", false, []string{"200", "401", "403", "404", "409"}},
		{"/supervision-links/{linkId}/decline", "POST", "declineSupervisionLink", false, []string{"200", "401", "403", "404", "409"}},
		{"/supervision-links/{linkId}/end", "POST", "endSupervisionLink", false, []string{"200", "401", "403", "404", "409"}},
		{"/notifications", "GET", "listNotifications", false, []string{"200", "400", "401", "403"}},
		{"/notifications/unread-count", "GET", "getUnreadNotificationCount", false, []string{"200", "401", "403"}},
		{"/notifications/read-all", "POST", "markAllNotificationsRead", false, []string{"200", "401", "403"}},
		{"/notifications/{notificationId}/read", "POST", "markNotificationRead", false, []string{"200", "401", "403", "404"}},
	}
	for _, tt := range tests {
		t.Run(tt.operationID, func(t *testing.T) {
			t.Parallel()
			item := spec.Paths.Find(tt.path)
			if item == nil {
				t.Fatalf("path %s missing", tt.path)
			}
			op := item.GetOperation(tt.method)
			if op == nil {
				t.Fatalf("%s %s missing", tt.method, tt.path)
			}
			if op.OperationID != tt.operationID {
				t.Errorf("operationId = %q, want %q", op.OperationID, tt.operationID)
			}
			for _, status := range tt.statuses {
				if op.Responses.Value(status) == nil {
					t.Errorf("%s %s has no %s response", tt.method, tt.path, status)
				}
			}
			hasKey := false
			for _, p := range op.Parameters {
				if p.Ref == "#/components/parameters/IdempotencyKey" {
					hasKey = true
				}
			}
			if hasKey != tt.idempotent {
				t.Errorf("Idempotency-Key = %v, want %v", hasKey, tt.idempotent)
			}
			if op.Security != nil {
				t.Errorf("%s must use the global cognitoJwt requirement, not override security", tt.operationID)
			}
		})
	}
}

// TestSupervisionContract_SearchIsBounded: the search term has a length cap (SEC-26) and the list is paginated.
func TestSupervisionContract_SearchIsBounded(t *testing.T) {
	t.Parallel()
	spec := loadFullSpec(t)

	op := spec.Paths.Find("/supervisors").Get
	var found bool
	for _, p := range op.Parameters {
		if p.Value == nil || p.Value.Name != "search" {
			continue
		}
		found = true
		if p.Value.In != "query" || p.Value.Required {
			t.Errorf("search must be an optional query parameter")
		}
		if p.Value.Schema.Value.MaxLength == nil || *p.Value.Schema.Value.MaxLength != 64 {
			t.Errorf("search maxLength = %v, want 64", p.Value.Schema.Value.MaxLength)
		}
	}
	if !found {
		t.Fatal("searchSupervisors has no search parameter")
	}
}

func TestSupervisionContract_Schemas(t *testing.T) {
	t.Parallel()
	spec := loadFullSpec(t)

	link := func() map[string]any {
		return map[string]any{
			"id": linkID, "status": "pending", "requested_at": "2026-11-10T04:00:00Z",
			"responded_at": nil, "ended_at": nil,
			"supervisee": map[string]any{"id": personID, "name": "A B"},
			"supervisor": map[string]any{"id": otherUser, "name": "C D"},
		}
	}
	with := func(m map[string]any, k string, v any) map[string]any { m[k] = v; return m }
	notification := func() map[string]any {
		return map[string]any{
			"id": linkID, "type": "supervision.request_received", "read_at": nil,
			"created_at": "2026-11-10T04:00:00Z",
			"payload":    map[string]any{"link_id": linkID, "actor_id": personID, "actor_name": "A B"},
		}
	}

	tests := []struct {
		name    string
		schema  string
		body    any
		wantErr bool
	}{
		{"request valid", "SupervisionRequest", map[string]any{"supervisor_id": otherUser}, false},
		{"request missing supervisor", "SupervisionRequest", map[string]any{}, true},
		{"request id too long", "SupervisionRequest", map[string]any{"supervisor_id": strings.Repeat("a", 37)}, true},
		{"request unknown field", "SupervisionRequest", map[string]any{"supervisor_id": otherUser, "organization_id": personID}, true},
		{"link valid", "SupervisionLink", link(), false},
		{"link accepted", "SupervisionLink", with(with(link(), "status", "accepted"), "responded_at", "2026-11-10T05:00:00Z"), false},
		{"link unknown status", "SupervisionLink", with(link(), "status", "rejected"), true},
		{"link leaks email", "SupervisionLink", with(link(), "email", "a@example.com"), true},
		{"person leaks email", "PersonRef", map[string]any{"id": personID, "name": "A B", "email": "a@example.com"}, true},
		{"supervisor summary leaks roles", "SupervisorSummary", map[string]any{"id": personID, "name": "A B", "roles": []any{"admin"}}, true},
		{"notification valid", "Notification", notification(), false},
		{"notification unknown type", "Notification", with(notification(), "type", "entry.deleted"), true},
		{"notification payload carries notes", "Notification", with(notification(), "payload", map[string]any{"notes": "x"}), true},
		{"unread count negative", "UnreadCount", map[string]any{"unread_count": -1}, true},
		{"unread count valid", "UnreadCount", map[string]any{"unread_count": 3}, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			ref := spec.Components.Schemas[tt.schema]
			if ref == nil {
				t.Fatalf("schema %s not found", tt.schema)
			}
			raw, err := json.Marshal(tt.body)
			if err != nil {
				t.Fatal(err)
			}
			var decoded any
			if err := json.Unmarshal(raw, &decoded); err != nil {
				t.Fatal(err)
			}
			err = ref.Value.VisitJSON(decoded)
			if (err != nil) != tt.wantErr {
				t.Fatalf("VisitJSON error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}

// TestSupervisionContract_PeerDTOsNeverReferenceEntries keeps supervision schemas off the entry
// model (ARC-18, SEC-40).
func TestSupervisionContract_PeerDTOsNeverReferenceEntries(t *testing.T) {
	t.Parallel()
	spec := loadFullSpec(t)
	for _, name := range []string{"PersonRef", "SupervisorSummary", "SupervisionLink", "Notification", "NotificationPayload"} {
		raw, err := json.Marshal(spec.Components.Schemas[name])
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(string(raw), "/Entry") || strings.Contains(string(raw), "/Client") {
			t.Errorf("%s references the entry or client model", name)
		}
	}
}

// TestSupervisionContract_ConflictResponse documents each 409 kind a client can branch on.
func TestSupervisionContract_ConflictResponse(t *testing.T) {
	t.Parallel()
	spec := loadFullSpec(t)
	desc := spec.Components.Responses["SupervisionConflict"].Value.Description
	for _, kind := range []string{"supervision-link-exists", "supervision-request-no-longer-valid", "supervision-link-not-active"} {
		if desc == nil || !strings.Contains(*desc, "urn:therapy-service-hours:problem:"+kind) {
			t.Errorf("SupervisionConflict does not document %s", kind)
		}
	}
}
