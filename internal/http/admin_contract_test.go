package httpapi

import (
	"encoding/json"
	"sort"
	"strings"
	"testing"

	"github.com/getkin/kin-openapi/openapi3"
)

const (
	adminOrg  = "00000000-0000-4000-8000-0000000000a1"
	adminUser = "00000000-0000-4000-8000-0000000000a2"
)

// adminOperations is the S5-02 surface the admin console (S5-13 to S5-15) and the S5-03 to S5-12
// handlers build against. The operations are excluded from route generation until implemented
// (api/oapi-codegen.yaml), so these checks read api/openapi.yaml itself.
var adminOperations = []struct {
	path, method, operationID, tag string
	idempotent                     bool
	statuses                       []string
}{
	{"/admin/organizations", "GET", "listAdminOrganizations", "admin-organizations", false, []string{"200", "400", "401", "403"}},
	{"/admin/organizations", "POST", "createAdminOrganization", "admin-organizations", true, []string{"201", "400", "401", "403", "409"}},
	{"/admin/organizations/{organizationId}", "PATCH", "renameAdminOrganization", "admin-organizations", false, []string{"200", "400", "401", "403", "404", "409"}},
	{"/admin/organizations/{organizationId}", "DELETE", "deleteAdminOrganization", "admin-organizations", false, []string{"204", "401", "403", "404", "409"}},
	{"/admin/users", "GET", "listAdminUsers", "admin-users", false, []string{"200", "400", "401", "403"}},
	{"/admin/users", "POST", "createAdminUser", "admin-users", true, []string{"201", "400", "401", "403", "409", "502", "504"}},
	{"/admin/users/{userId}", "GET", "getAdminUser", "admin-users", false, []string{"200", "401", "403", "404"}},
	{"/admin/users/{userId}", "PATCH", "updateAdminUser", "admin-users", false, []string{"200", "400", "401", "403", "404"}},
	{"/admin/users/{userId}", "DELETE", "deleteAdminUser", "admin-users", false, []string{"204", "401", "403", "404", "409", "502", "504"}},
	{"/admin/users/{userId}/roles", "PUT", "setAdminUserRoles", "admin-users", false, []string{"200", "400", "401", "403", "404", "409", "502", "504"}},
	{"/admin/users/{userId}/email", "PUT", "changeAdminUserEmail", "admin-users", false, []string{"200", "400", "401", "403", "404", "409", "502", "504"}},
	{"/admin/users/{userId}/organization", "PUT", "setAdminUserOrganization", "admin-users", false, []string{"200", "400", "401", "403", "404"}},
	{"/admin/users/{userId}/organization-change-preview", "GET", "previewAdminUserOrganizationChange", "admin-users", false, []string{"200", "400", "401", "403", "404"}},
	{"/admin/users/{userId}/deactivate", "POST", "deactivateAdminUser", "admin-users", true, []string{"200", "401", "403", "404", "409", "502", "504"}},
	{"/admin/users/{userId}/reactivate", "POST", "reactivateAdminUser", "admin-users", true, []string{"200", "401", "403", "404", "409", "502", "504"}},
	{"/admin/audit", "GET", "listAdminAudit", "admin-audit", false, []string{"200", "400", "401", "403"}},
}

func TestAdminContract_Operations(t *testing.T) {
	t.Parallel()
	spec := loadFullSpec(t)

	for _, tt := range adminOperations {
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
			if len(op.Tags) != 1 || op.Tags[0] != tt.tag {
				t.Errorf("tags = %v, want [%s]", op.Tags, tt.tag)
			}
			for _, status := range tt.statuses {
				if op.Responses.Value(status) == nil {
					t.Errorf("%s %s has no %s response", tt.method, tt.path, status)
				}
			}
			checkAdminErrorResponses(t, op)
			if hasParamRef(op.Parameters, "#/components/parameters/IdempotencyKey") != tt.idempotent {
				t.Errorf("Idempotency-Key = %v, want %v", !tt.idempotent, tt.idempotent)
			}
			if op.Security != nil {
				t.Errorf("%s must use the global cognitoJwt requirement, not override security", tt.operationID)
			}
		})
	}
}

// checkAdminErrorResponses: every admin operation answers a non-admin with the shared 403 (US-30)
// and any other error with the shared Problem.
func checkAdminErrorResponses(t *testing.T, op *openapi3.Operation) {
	t.Helper()
	if r := op.Responses.Value("403"); r != nil && r.Ref != "#/components/responses/AdminForbidden" {
		t.Errorf("403 = %q, want the shared AdminForbidden response (US-30)", r.Ref)
	}
	if r := op.Responses.Default(); r == nil || r.Ref != "#/components/responses/Problem" {
		t.Error("default response must be the shared Problem")
	}
}

func hasParamRef(params openapi3.Parameters, ref string) bool {
	for _, p := range params {
		if p.Ref == ref {
			return true
		}
	}
	return false
}

// TestAdminContract_NoUndeclaredAdminOperations: every operation under /admin is one of the
// operations above, so a new admin route cannot be added without its contract test.
func TestAdminContract_NoUndeclaredAdminOperations(t *testing.T) {
	t.Parallel()
	spec := loadFullSpec(t)

	want := map[string]bool{}
	for _, tt := range adminOperations {
		want[tt.method+" "+tt.path] = true
	}
	got := map[string]bool{}
	for path, item := range spec.Paths.Map() {
		if !strings.HasPrefix(path, "/admin/") {
			continue
		}
		for method := range item.Operations() {
			got[method+" "+path] = true
		}
	}
	for key := range got {
		if !want[key] {
			t.Errorf("%s is under /admin but not covered by adminOperations", key)
		}
	}
	for key := range want {
		if !got[key] {
			t.Errorf("%s is expected but missing", key)
		}
	}
}

// TestAdminContract_SchemasAreSeparateFromEntryModels: admin responses describe accounts only. No
// admin operation reaches an entry, client, category or schedule schema, directly or through
// another schema (S5-02 acceptance criterion, FR-ADM-10, ARC-18).
func TestAdminContract_SchemasAreSeparateFromEntryModels(t *testing.T) {
	t.Parallel()
	spec := loadFullSpec(t)

	reached := adminReachableSchemas(spec)
	if !reached["AdminUser"] || !reached["AdminOrganization"] || !reached["AdminAuditEntry"] {
		t.Fatalf("walk did not reach the admin schemas: %v", sortedKeys(reached))
	}
	forbidden := map[string]bool{
		"Entry": true, "EntryRequest": true, "EntryListResponse": true, "EntryStatus": true,
		"ConflictingEntry": true, "EntryConflictProblem": true, "Client": true, "ClientRequest": true,
		"ClientListResponse": true, "ClientConflictProblem": true, "Category": true, "CategoryRef": true,
		"CategoryListResponse": true, "ImportReport": true, "MeResponse": true,
	}
	for name := range reached {
		if forbidden[name] {
			t.Errorf("an admin operation reaches %s; admin schemas must stay separate from entry models", name)
		}
	}
	for _, field := range []string{"entries", "notes", "clients", "hours", "category"} {
		for _, schema := range []string{"AdminUser", "AdminOrganization", "AdminAuditEntry"} {
			if _, ok := spec.Components.Schemas[schema].Value.Properties[field]; ok {
				t.Errorf("%s has %q; the admin console must not expose entry data (FR-ADM-10)", schema, field)
			}
		}
	}
}

// adminReachableSchemas returns the names of the component schemas that any /admin request or
// response reaches, directly or through another schema.
func adminReachableSchemas(spec *openapi3.T) map[string]bool {
	reached := map[string]bool{}
	var walk func(ref *openapi3.SchemaRef)
	walk = func(ref *openapi3.SchemaRef) {
		if ref == nil {
			return
		}
		if name, ok := strings.CutPrefix(ref.Ref, "#/components/schemas/"); ok {
			if reached[name] {
				return
			}
			reached[name] = true
		}
		s := ref.Value
		if s == nil {
			return
		}
		for _, p := range s.Properties {
			walk(p)
		}
		walk(s.Items)
		for _, list := range []openapi3.SchemaRefs{s.AllOf, s.AnyOf, s.OneOf} {
			for _, sub := range list {
				walk(sub)
			}
		}
		walk(s.AdditionalProperties.Schema)
	}
	for _, root := range adminRootSchemas(spec) {
		walk(root)
	}
	return reached
}

// adminRootSchemas lists the schemas that the /admin request bodies and responses use directly.
func adminRootSchemas(spec *openapi3.T) []*openapi3.SchemaRef {
	var roots []*openapi3.SchemaRef
	addContent := func(content openapi3.Content) {
		for _, media := range content {
			roots = append(roots, media.Schema)
		}
	}
	for path, item := range spec.Paths.Map() {
		if !strings.HasPrefix(path, "/admin/") {
			continue
		}
		for _, op := range item.Operations() {
			if op.RequestBody != nil && op.RequestBody.Value != nil {
				addContent(op.RequestBody.Value.Content)
			}
			for _, resp := range op.Responses.Map() {
				if resp.Value != nil {
					addContent(resp.Value.Content)
				}
			}
		}
	}
	return roots
}

func TestAdminContract_RequestSchemas(t *testing.T) {
	t.Parallel()
	spec := loadFullSpec(t)

	create := func() map[string]any {
		return map[string]any{"first_name": "Pat", "last_name": "Example", "email": "pat@example.test", "roles": []any{"supervisor"}, "organization_id": adminOrg}
	}
	with := func(base map[string]any, key string, v any) map[string]any { base[key] = v; return base }
	without := func(base map[string]any, key string) map[string]any { delete(base, key); return base }

	tests := []struct {
		name    string
		schema  string
		body    map[string]any
		wantErr bool
	}{
		{"create user valid", "AdminUserCreateRequest", create(), false},
		{"create user without roles", "AdminUserCreateRequest", without(create(), "roles"), false},
		{"create user needs an organization", "AdminUserCreateRequest", without(create(), "organization_id"), true},
		{"create user needs an email", "AdminUserCreateRequest", without(create(), "email"), true},
		{"create user needs names", "AdminUserCreateRequest", without(create(), "last_name"), true},
		{"create user unknown role", "AdminUserCreateRequest", with(create(), "roles", []any{"owner"}), true},
		{"create user duplicate roles", "AdminUserCreateRequest", with(create(), "roles", []any{"admin", "admin"}), true},
		{"create user cannot set status", "AdminUserCreateRequest", with(create(), "status", "deactivated"), true},
		{"create user cannot set an id", "AdminUserCreateRequest", with(create(), "id", adminUser), true},
		{"update user name", "AdminUserUpdateRequest", map[string]any{"first_name": "Sam"}, false},
		{"update user needs a field", "AdminUserUpdateRequest", map[string]any{}, true},
		{"update user cannot change roles here", "AdminUserUpdateRequest", map[string]any{"roles": []any{"admin"}}, true},
		{"update user cannot change email here", "AdminUserUpdateRequest", map[string]any{"email": "x@example.test"}, true},
		{"update user empty name", "AdminUserUpdateRequest", map[string]any{"last_name": ""}, true},
		{"roles valid", "AdminUserRolesRequest", map[string]any{"roles": []any{"admin", "supervisor"}}, false},
		{"roles may be empty", "AdminUserRolesRequest", map[string]any{"roles": []any{}}, false},
		{"roles required", "AdminUserRolesRequest", map[string]any{}, true},
		{"roles unknown value", "AdminUserRolesRequest", map[string]any{"roles": []any{"user"}}, true},
		{"email valid", "AdminUserEmailRequest", map[string]any{"email": "new@example.test"}, false},
		{"email required", "AdminUserEmailRequest", map[string]any{}, true},
		{"organization change confirmed", "AdminUserOrganizationRequest", map[string]any{"organization_id": adminOrg, "confirm_consequences": true}, false},
		{"organization change needs confirmation", "AdminUserOrganizationRequest", map[string]any{"organization_id": adminOrg}, true},
		{"organization change refuses false", "AdminUserOrganizationRequest", map[string]any{"organization_id": adminOrg, "confirm_consequences": false}, true},
		{"organization name valid", "AdminOrganizationRequest", map[string]any{"name": "North Clinic"}, false},
		{"organization name empty", "AdminOrganizationRequest", map[string]any{"name": ""}, true},
		{"organization name too long", "AdminOrganizationRequest", map[string]any{"name": strings.Repeat("a", 201)}, true},
		{"organization unknown field", "AdminOrganizationRequest", map[string]any{"name": "North", "user_count": 3}, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			ref := spec.Components.Schemas[tt.schema]
			if ref == nil {
				t.Fatalf("schema %s not found", tt.schema)
			}
			if err := ref.Value.VisitJSON(roundTrip(t, tt.body)); (err != nil) != tt.wantErr {
				t.Fatalf("VisitJSON error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}

func TestAdminContract_ResponseSchemas(t *testing.T) {
	t.Parallel()
	spec := loadFullSpec(t)

	user := func() map[string]any {
		return map[string]any{
			"id": adminUser, "first_name": "Pat", "last_name": "Example", "name": "Pat Example", "email": "pat@example.test",
			"roles": []any{"admin"}, "organization": map[string]any{"id": adminOrg, "name": "Default Organization"},
			"status": "active", "has_signed_in": true, "created_at": "2026-11-03T10:00:00Z",
		}
	}
	audit := func() map[string]any {
		return map[string]any{
			"id": adminUser, "created_at": "2026-11-03T10:00:00Z", "action": "user.roles_change",
			"actor_id": adminUser, "actor_name": "Pat Example", "target_user_id": adminUser, "target_name": "Sam Example",
			"details": map[string]any{"old_roles": []any{}, "new_roles": []any{"supervisor"}},
		}
	}
	tests := []struct {
		name    string
		schema  string
		body    map[string]any
		wantErr bool
	}{
		{"user valid", "AdminUser", user(), false},
		{"deactivated user", "AdminUser", func() map[string]any {
			m := user()
			m["status"], m["deactivated_at"] = "deactivated", "2026-11-03T11:00:00Z"
			return m
		}(), false},
		{"invited user not yet signed in", "AdminUser", func() map[string]any {
			m := user()
			m["has_signed_in"], m["invitation_status"], m["invitation_expires_at"] = false, "pending", "2026-11-17T10:00:00Z"
			return m
		}(), false},
		{"user has no entries field", "AdminUser", func() map[string]any { m := user(); m["entries"] = []any{}; return m }(), true},
		{"user has no notes field", "AdminUser", func() map[string]any { m := user(); m["notes"] = "x"; return m }(), true},
		{"user unknown status", "AdminUser", func() map[string]any { m := user(); m["status"] = "deleted"; return m }(), true},
		{"user missing organization", "AdminUser", func() map[string]any { m := user(); delete(m, "organization"); return m }(), true},
		{"audit row valid", "AdminAuditEntry", audit(), false},
		{"audit row survives a deleted actor and target", "AdminAuditEntry", func() map[string]any {
			m := audit()
			delete(m, "actor_id")
			delete(m, "target_user_id")
			return m
		}(), false},
		{"audit row for an action on no user", "AdminAuditEntry", func() map[string]any {
			m := audit()
			delete(m, "target_user_id")
			delete(m, "target_name")
			m["action"] = "organization.create"
			return m
		}(), false},
		{"audit action must be dotted lowercase", "AdminAuditEntry", func() map[string]any { m := audit(); m["action"] = "Delete User"; return m }(), true},
		{"audit details must be an object", "AdminAuditEntry", func() map[string]any { m := audit(); m["details"] = "text"; return m }(), true},
		{"organization valid", "AdminOrganization", map[string]any{"id": adminOrg, "name": "North", "user_count": 0, "created_at": "2026-11-03T10:00:00Z"}, false},
		{"organization user count not negative", "AdminOrganization", map[string]any{"id": adminOrg, "name": "North", "user_count": -1, "created_at": "2026-11-03T10:00:00Z"}, true},
		{"not-empty conflict carries the count", "AdminOrganizationConflictProblem", map[string]any{
			"type": "urn:therapy-service-hours:problem:organization-not-empty", "title": "Organization has members", "status": 409, "user_count": 3,
		}, false},
		{"preview valid", "AdminOrganizationChangePreview", map[string]any{
			"user_id": adminUser, "is_supervisor": false,
			"current_organization": map[string]any{"id": adminOrg, "name": "A"}, "target_organization": map[string]any{"id": adminOrg, "name": "B"},
			"consequences": map[string]any{"links_ended": 0, "requests_cancelled": 0},
		}, false},
		{"preview needs both counts", "AdminOrganizationChangePreview", map[string]any{
			"user_id": adminUser, "is_supervisor": false,
			"current_organization": map[string]any{"id": adminOrg, "name": "A"}, "target_organization": map[string]any{"id": adminOrg, "name": "B"},
			"consequences": map[string]any{"links_ended": 0},
		}, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			ref := spec.Components.Schemas[tt.schema]
			if ref == nil {
				t.Fatalf("schema %s not found", tt.schema)
			}
			if err := ref.Value.VisitJSON(roundTrip(t, tt.body)); (err != nil) != tt.wantErr {
				t.Fatalf("VisitJSON error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}

// TestAdminContract_ListsArePagedAndSortedFromAnAllowlist: the lists take the shared page
// parameters; the user list sorts only by allowlisted columns, and none of the lists accept a
// filter that could widen the scope beyond accounts.
func TestAdminContract_ListsArePagedAndSortedFromAnAllowlist(t *testing.T) {
	t.Parallel()
	spec := loadFullSpec(t)

	for _, path := range []string{"/admin/organizations", "/admin/users", "/admin/audit"} {
		params := spec.Paths.Find(path).Get.Parameters
		for _, want := range []string{"#/components/parameters/Page", "#/components/parameters/PageSize"} {
			if !hasParamRef(params, want) {
				t.Errorf("GET %s has no %s", path, want)
			}
		}
	}

	byName := map[string]*openapi3.Parameter{}
	for _, p := range spec.Paths.Find("/admin/users").Get.Parameters {
		if p.Value != nil && p.Ref == "" {
			byName[p.Value.Name] = p.Value
		}
	}
	sortParam := byName["sort"]
	if sortParam == nil {
		t.Fatal("listAdminUsers has no sort parameter")
	}
	var allowed []string
	for _, v := range sortParam.Schema.Value.Enum {
		allowed = append(allowed, v.(string))
	}
	sort.Strings(allowed)
	if got := strings.Join(allowed, ","); got != "created_at,email,name,organization" {
		t.Errorf("sort allowlist = %s", got)
	}
	search := byName["search"]
	if search == nil || search.Schema.Value.MaxLength == nil || *search.Schema.Value.MaxLength != 64 {
		t.Error("search must be capped at 64 characters (SEC-24)")
	}

	// The audit listing is newest first with no sort or filter to change that.
	for _, p := range spec.Paths.Find("/admin/audit").Get.Parameters {
		if p.Ref == "" {
			t.Errorf("listAdminAudit has the unexpected parameter %q", p.Value.Name)
		}
	}
}

// TestAdminContract_WorkflowProblems: identity failure and timeout map to 502 and 504 problems that
// say whether anything changed (ADR-0002).
func TestAdminContract_WorkflowProblems(t *testing.T) {
	t.Parallel()
	spec := loadFullSpec(t)

	for name, wantType := range map[string]string{
		"IdentityFailure": "urn:therapy-service-hours:problem:identity-failure",
		"WorkflowTimeout": "urn:therapy-service-hours:problem:workflow-timeout",
		"AdminForbidden":  "urn:therapy-service-hours:problem:forbidden",
	} {
		resp := spec.Components.Responses[name]
		if resp == nil || resp.Value == nil {
			t.Fatalf("response %s missing", name)
		}
		if desc := *resp.Value.Description; !strings.Contains(desc, wantType) {
			t.Errorf("%s does not document %s", name, wantType)
		}
		if resp.Value.Content.Get("application/problem+json") == nil {
			t.Errorf("%s must be application/problem+json", name)
		}
	}
	for _, tt := range adminOperations {
		op := spec.Paths.Find(tt.path).GetOperation(tt.method)
		want := strings.Contains(strings.Join(tt.statuses, ","), "502")
		got := op.Responses.Value("502") != nil
		if got != want {
			t.Errorf("%s: 502 = %v, want %v", tt.operationID, got, want)
		}
	}
}

func roundTrip(t *testing.T, body map[string]any) any {
	t.Helper()
	raw, err := json.Marshal(body)
	if err != nil {
		t.Fatal(err)
	}
	var decoded any
	if err := json.Unmarshal(raw, &decoded); err != nil {
		t.Fatal(err)
	}
	return decoded
}

func sortedKeys(m map[string]bool) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}
