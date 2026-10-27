package httpapi

import (
	"encoding/json"
	"testing"
)

// TestImportExportContract_Operations pins the S4-01 shape the frontend (S4-10 and the exporters)
// builds against. The operations are excluded from route generation until S4-04 and S4-06 implement
// them, so these checks read api/openapi.yaml itself.
func TestImportExportContract_Operations(t *testing.T) {
	t.Parallel()
	spec := loadFullSpec(t)

	tests := []struct {
		path, method, operationID string
		idempotent                bool
		statuses                  []string
	}{
		{"/import/markdown", "POST", "importMarkdown", true, []string{"200", "400", "401", "403", "409", "413"}},
		{"/export/entries", "GET", "exportEntries", false, []string{"200", "400", "401", "403"}},
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

// TestImportExportContract_UploadLimits: the upload is one multipart file part with a size cap, and
// no other part is accepted.
func TestImportExportContract_UploadLimits(t *testing.T) {
	t.Parallel()
	spec := loadFullSpec(t)

	op := spec.Paths.Find("/import/markdown").Post
	if op.RequestBody == nil || op.RequestBody.Value == nil || !op.RequestBody.Value.Required {
		t.Fatal("importMarkdown must require a request body")
	}
	content := op.RequestBody.Value.Content
	if len(content) != 1 || content.Get("multipart/form-data") == nil {
		t.Fatalf("importMarkdown must accept only multipart/form-data, got %d content types", len(content))
	}

	upload := spec.Components.Schemas["ImportMarkdownRequest"].Value
	if upload.AdditionalProperties.Has == nil || *upload.AdditionalProperties.Has {
		t.Error("ImportMarkdownRequest must set additionalProperties: false")
	}
	file := upload.Properties["file"]
	if file == nil || !contains(upload.Required, "file") {
		t.Fatal("ImportMarkdownRequest must require a file part")
	}
	if file.Value.Format != "binary" {
		t.Errorf("file format = %q, want binary", file.Value.Format)
	}
	if file.Value.MaxLength == nil || *file.Value.MaxLength != 1<<20 {
		t.Errorf("file maxLength = %v, want 1 MiB", file.Value.MaxLength)
	}
}

// TestImportExportContract_ReportSchema: the report lists what was imported, which clients were
// merged, and every skipped row with a reason (S4-01 acceptance criteria).
func TestImportExportContract_ReportSchema(t *testing.T) {
	t.Parallel()
	spec := loadFullSpec(t)

	report := spec.Components.Schemas["ImportReport"].Value
	for _, field := range []string{"total_rows", "imported_count", "needs_time_count", "merged_clients", "skipped_count", "skipped_rows"} {
		if report.Properties[field] == nil {
			t.Errorf("ImportReport has no %q", field)
		}
		if !contains(report.Required, field) {
			t.Errorf("ImportReport does not require %q", field)
		}
	}
	if report.AdditionalProperties.Has == nil || *report.AdditionalProperties.Has {
		t.Error("ImportReport must set additionalProperties: false")
	}

	skipped := spec.Components.Schemas["SkippedRow"].Value
	for _, field := range []string{"row", "reason", "message"} {
		if !contains(skipped.Required, field) {
			t.Errorf("SkippedRow does not require %q", field)
		}
	}
	reasons := spec.Components.Schemas["SkipReason"].Value
	for _, want := range []string{"duplicate", "overlap", "malformed-row", "unknown-category"} {
		found := false
		for _, v := range reasons.Enum {
			found = found || v == want
		}
		if !found {
			t.Errorf("SkipReason has no %q", want)
		}
	}

	valid := map[string]any{
		"total_rows": 2, "imported_count": 1, "needs_time_count": 1, "skipped_count": 1,
		"merged_clients": []any{map[string]any{"client": map[string]any{"id": "00000000-0000-4000-8000-0000000000d1", "name": "A.B."}, "created": true}},
		"skipped_rows":   []any{map[string]any{"row": 2, "reason": "duplicate", "message": "Same as an existing entry."}},
	}
	tests := []struct {
		name    string
		mutate  func(map[string]any)
		wantErr bool
	}{
		{"valid report", func(map[string]any) {}, false},
		{"unknown field", func(m map[string]any) { m["rows"] = 1 }, true},
		{"missing skipped rows", func(m map[string]any) { delete(m, "skipped_rows") }, true},
		{"unknown reason", func(m map[string]any) {
			m["skipped_rows"] = []any{map[string]any{"row": 1, "reason": "because", "message": "x"}}
		}, true},
		{"row numbers start at 1", func(m map[string]any) {
			m["skipped_rows"] = []any{map[string]any{"row": 0, "reason": "duplicate", "message": "x"}}
		}, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			body := cloneJSON(t, valid)
			tt.mutate(body)
			err := spec.Components.Schemas["ImportReport"].Value.VisitJSON(body)
			if (err != nil) != tt.wantErr {
				t.Fatalf("VisitJSON error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}

// TestImportExportContract_ExportIsPagedAndUnfiltered: the export reads the caller's entries page by
// page with the shared page size cap, and offers no way to widen the scope.
func TestImportExportContract_ExportIsPagedAndUnfiltered(t *testing.T) {
	t.Parallel()
	spec := loadFullSpec(t)

	op := spec.Paths.Find("/export/entries").Get
	got := map[string]bool{}
	for _, p := range op.Parameters {
		name := p.Ref
		if p.Value != nil && p.Ref == "" {
			name = p.Value.Name
		}
		got[name] = true
	}
	want := map[string]bool{"#/components/parameters/Page": true, "#/components/parameters/PageSize": true}
	if len(got) != len(want) {
		t.Errorf("exportEntries parameters = %v, want only page and page_size", got)
	}
	for name := range want {
		if !got[name] {
			t.Errorf("exportEntries has no %s", name)
		}
	}

	pageSize := spec.Components.Parameters["PageSize"].Value.Schema.Value
	if pageSize.Max == nil || *pageSize.Max != 200 {
		t.Errorf("page size cap = %v, want 200", pageSize.Max)
	}

	// The export rows carry the times, status and clients the exporters need.
	entry := spec.Components.Schemas["Entry"].Value
	for _, field := range []string{"session_date", "start_time", "end_time", "hours", "category", "clients", "on_site", "notes", "status", "needs_time"} {
		if entry.Properties[field] == nil {
			t.Errorf("Entry has no %q for the export", field)
		}
	}
	for _, field := range []string{"start_time", "end_time"} {
		if contains(entry.Required, field) {
			t.Errorf("Entry requires %q, but a needs_time entry has none", field)
		}
	}
}

func cloneJSON(t *testing.T, v map[string]any) map[string]any {
	t.Helper()
	raw, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	var out map[string]any
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatal(err)
	}
	return out
}
