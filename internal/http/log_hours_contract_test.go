package httpapi

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"

	"github.com/getkin/kin-openapi/openapi3"
)

// loadFullSpec loads api/openapi.yaml itself. The embedded spec is filtered while the log-hours
// routes are unimplemented (api/oapi-codegen.yaml), so these S2-03 checks read the file.
func loadFullSpec(t *testing.T) *openapi3.T {
	t.Helper()
	spec, err := openapi3.NewLoader().LoadFromFile("../../api/openapi.yaml")
	if err != nil {
		t.Fatalf("load api/openapi.yaml: %v", err)
	}
	if err := spec.Validate(context.Background()); err != nil {
		t.Fatalf("spec is invalid: %v", err)
	}
	return spec
}

func TestLogHoursContract_RequestSchemas(t *testing.T) {
	t.Parallel()
	spec := loadFullSpec(t)

	const (
		category = "00000000-0000-4000-8000-0000000000c1"
		client   = "00000000-0000-4000-8000-0000000000d1"
	)
	validEntry := func() map[string]any {
		return map[string]any{
			"session_date": "2026-10-12", "start_time": "09:00", "end_time": "10:15",
			"category_id": category, "client_ids": []any{client}, "on_site": true, "notes": "Pseudonym A",
		}
	}
	with := func(key string, v any) map[string]any { m := validEntry(); m[key] = v; return m }
	without := func(key string) map[string]any { m := validEntry(); delete(m, key); return m }

	tests := []struct {
		name    string
		schema  string
		body    map[string]any
		wantErr bool
	}{
		{"entry valid", "EntryRequest", validEntry(), false},
		{"entry minimal", "EntryRequest", map[string]any{"session_date": "2026-10-12", "start_time": "00:00", "end_time": "00:15", "category_id": category}, false},
		{"entry hours is not accepted", "EntryRequest", with("hours", 1.25), true},
		{"entry duration_minutes is not accepted", "EntryRequest", with("duration_minutes", 75), true},
		{"entry unknown field", "EntryRequest", with("status", "approved"), true},
		{"entry start not on a 15-minute step", "EntryRequest", with("start_time", "09:07"), false}, // enforced by S2-04, not the schema
		{"entry time with seconds", "EntryRequest", with("start_time", "09:00:00"), true},
		{"entry end of day 24:00", "EntryRequest", with("end_time", "24:00"), true},
		{"entry missing start", "EntryRequest", without("start_time"), true},
		{"entry missing end", "EntryRequest", without("end_time"), true},
		{"entry missing category", "EntryRequest", without("category_id"), true},
		{"entry notes too long", "EntryRequest", with("notes", string(make([]byte, 2001))), true},
		{"entry duplicate clients", "EntryRequest", with("client_ids", []any{client, client}), true},
		{"entry too many clients", "EntryRequest", with("client_ids", manyClients(51)), true},
		{"client valid", "ClientRequest", map[string]any{"name": "A.B."}, false},
		{"client empty name", "ClientRequest", map[string]any{"name": ""}, true},
		{"client name too long", "ClientRequest", map[string]any{"name": string(make([]byte, 65))}, true},
		{"client unknown field", "ClientRequest", map[string]any{"name": "A.B.", "user_id": category}, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			ref := spec.Components.Schemas[tt.schema]
			if ref == nil {
				t.Fatalf("schema %s not found", tt.schema)
			}
			// Round-trip through JSON so the body has the types a decoded request would have.
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

// TestLogHoursContract_HoursIsReadOnly: requests cannot carry hours, responses always do.
func TestLogHoursContract_HoursIsReadOnly(t *testing.T) {
	t.Parallel()
	spec := loadFullSpec(t)

	req := spec.Components.Schemas["EntryRequest"].Value
	for _, field := range []string{"hours", "duration_minutes"} {
		if _, ok := req.Properties[field]; ok {
			t.Errorf("EntryRequest has %q; the server calculates it (FR-LOG-02)", field)
		}
	}
	if req.AdditionalProperties.Has == nil || *req.AdditionalProperties.Has {
		t.Error("EntryRequest must set additionalProperties: false")
	}

	entry := spec.Components.Schemas["Entry"].Value
	for _, field := range []string{"hours", "duration_minutes"} {
		if _, ok := entry.Properties[field]; !ok {
			t.Errorf("Entry has no %q", field)
		}
		if !contains(entry.Required, field) {
			t.Errorf("Entry does not require %q", field)
		}
	}
}

// TestLogHoursContract_Operations pins the shape the frontend (S2-11 onward) builds against.
func TestLogHoursContract_Operations(t *testing.T) {
	t.Parallel()
	spec := loadFullSpec(t)

	tests := []struct {
		path, method, operationID string
		idempotent                bool
		statuses                  []string
	}{
		{"/categories", "GET", "listCategories", false, []string{"200"}},
		{"/clients", "GET", "listClients", false, []string{"200"}},
		{"/clients", "POST", "createClient", true, []string{"201", "409"}},
		{"/clients/{clientId}", "PATCH", "renameClient", false, []string{"200", "404", "409"}},
		{"/clients/{clientId}", "DELETE", "deleteClient", false, []string{"204", "404", "409"}},
		{"/entries", "GET", "listEntries", false, []string{"200", "400"}},
		{"/entries", "POST", "createEntry", true, []string{"201", "400", "409"}},
		{"/entries/{entryId}", "GET", "getEntry", false, []string{"200", "404"}},
		{"/entries/{entryId}", "PUT", "updateEntry", false, []string{"200", "400", "404", "409"}},
		{"/entries/{entryId}", "DELETE", "deleteEntry", false, []string{"204", "404", "409"}},
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
		})
	}

	list := spec.Paths.Find("/entries").Get
	want := map[string]bool{"group": false, "client": false, "from": false, "to": false, "sort": false, "dir": false}
	for _, p := range list.Parameters {
		if p.Value != nil {
			if _, ok := want[p.Value.Name]; ok {
				want[p.Value.Name] = true
			}
		}
	}
	for name, found := range want {
		if !found {
			t.Errorf("listEntries has no %q parameter", name)
		}
	}
}

// manyClients returns n distinct client ids, so only maxItems can reject the list.
func manyClients(n int) []any {
	ids := make([]any, n)
	for i := range ids {
		ids[i] = fmt.Sprintf("00000000-0000-4000-8000-%012d", i+1)
	}
	return ids
}

func contains(list []string, s string) bool {
	for _, v := range list {
		if v == s {
			return true
		}
	}
	return false
}
