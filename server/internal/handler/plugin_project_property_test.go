package handler

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/google/uuid"
	"github.com/multica-ai/multica/server/internal/featureflags"
	"github.com/multica-ai/multica/server/internal/service"
	"github.com/multica-ai/multica/server/internal/testutil"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
	"github.com/multica-ai/multica/server/pkg/featureflag"
)

// projectPropertyPluginManifest grants the projects scopes and the manual
// "summarize" hook the admin callback path requires.
const projectPropertyPluginManifest = `{
  "manifest_version": 1,
  "key": "com.example.properties",
  "name": "Properties",
  "version": "1.0.0",
  "author": { "name": "example" },
  "scopes": ["projects:read", "projects:write", "installation:read", "net:example.com"],
  "contributes": {
    "hooks": [{
      "key": "summarize",
      "name": "Publish",
      "description": "Publish mappings.",
      "triggers": ["ui"],
      "transport": { "type": "http", "url": "https://example.com/hooks/publish" }
    }]
  }
}`

func installProjectPropertyPlugin(t *testing.T) (string, string) {
	t.Helper()
	withPluginsV1Flag(t, testHandler, true)
	cleanupPluginInstallations(t)
	versionID := withLocalPluginSource(t, projectPropertyPluginManifest)
	scopes := []string{"projects:read", "projects:write", "installation:read", "net:example.com"}
	body, _ := json.Marshal(map[string]any{"version_id": versionID, "granted_scopes": scopes})
	recorder := httptest.NewRecorder()
	testHandler.InstallPlugin(recorder, pluginHandlerRequest(http.MethodPost, "/plugins", body, map[string]string{"id": testWorkspaceID}))
	if recorder.Code != http.StatusCreated {
		t.Fatalf("install property plugin: status=%d body=%s", recorder.Code, recorder.Body.String())
	}
	var installed struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &installed); err != nil {
		t.Fatal(err)
	}
	provider := featureflag.NewStaticProvider()
	provider.Set(featureflags.PluginsV1, featureflag.Rule{Default: true})
	provider.Set(featureflags.PluginIntegrationsV1, featureflag.Rule{Default: true})
	oldFlags, oldResolver := testHandler.PluginService.FeatureFlags, testHandler.PluginService.FlagResolver
	testHandler.PluginService.FeatureFlags = featureflag.NewService(provider)
	testHandler.PluginService.FlagResolver = nil
	t.Cleanup(func() {
		testHandler.PluginService.FeatureFlags = oldFlags
		testHandler.PluginService.FlagResolver = oldResolver
	})
	token, err := testHandler.PluginService.IssueInstallToken(context.Background(), parseUUID(installed.ID))
	if err != nil {
		t.Fatal(err)
	}
	return installed.ID, token
}

// withProjectPropertiesCapability registers tc.projectproperties (a
// deployment-declared flag, absent from the built-in registry) for the fixture
// workspace and optionally enables it. Must run after withFeatureFlag, which
// clears h.FlagResolver. Project-type definitions created by a test are
// removed at cleanup so name uniqueness does not leak across tests.
func withProjectPropertiesCapability(t *testing.T, enabled bool) {
	t.Helper()
	old := testHandler.FlagResolver
	registry, err := featureflags.NewRegistry(append(featureflags.HostRegistry().Definitions(),
		featureflags.Definition{Key: ProjectPropertiesFlag, Label: "Project properties", Group: "True Collective", ScopePolicy: featureflags.WorkspaceOnly}))
	if err != nil {
		t.Fatal(err)
	}
	testHandler.FlagResolver = featureflags.NewResolver(registry, testHandler.FeatureFlags, featureflags.DBSource{Queries: testHandler.Queries})
	t.Cleanup(func() {
		testHandler.FlagResolver = old
		_, _ = testHandler.DB.Exec(context.Background(), "DELETE FROM issue_property WHERE workspace_id=$1 AND resource_type='project'", testWorkspaceID)
	})
	if !enabled {
		return
	}
	dbfx.Cleanup(t, "DELETE FROM workspace_feature_flag_override WHERE workspace_id=$1 AND flag_key=$2", testWorkspaceID, ProjectPropertiesFlag)
	dbfx.Insert(t, "workspace_feature_flag_override", testutil.Cols{
		"workspace_id": testWorkspaceID, "scope_type": "workspace", "scope_id": testWorkspaceID,
		"flag_key": ProjectPropertiesFlag, "enabled": true, "updated_by_type": "system",
	})
}

func propertyUpsertRequest(token string, body any) *http.Request {
	return pluginInstallTokenRequest(http.MethodPut, "/v1/plugin-project-properties", token, body, nil)
}

func dealFaseBody() map[string]any {
	return map[string]any{
		"name": "Deal-fase", "type": "select", "description": "Attio deal stage",
		"config": map[string]any{"options": []map[string]any{
			{"name": "Lead", "color": "#3b82f6"},
			{"name": "Meeting booked", "color": "#22c55e"},
		}},
	}
}

func optionIDs(def PropertyResponse) map[string]string {
	out := map[string]string{}
	for _, opt := range def.Config.Options {
		out[opt.Name] = opt.ID
	}
	return out
}

func TestPluginProjectPropertyRequiresAdminCallback(t *testing.T) {
	id, token := installProjectPropertyPlugin(t)
	withCallbackTokens(t)
	withProjectPropertiesCapability(t, true)

	// A standing installation token cannot create schema: 403 callback_required.
	recorder := httptest.NewRecorder()
	testHandler.UpsertPluginProjectProperty(recorder, propertyUpsertRequest(token, dealFaseBody()))
	if recorder.Code != http.StatusForbidden {
		t.Fatalf("standing token: status=%d body=%s, want 403", recorder.Code, recorder.Body.String())
	}

	// A plugin-actor callback (event hook, no person) is refused too.
	eventToken := issueCallbackToken(t, id, service.HookActor{Type: "plugin", ID: parseUUID(id)})
	recorder = httptest.NewRecorder()
	testHandler.UpsertPluginProjectProperty(recorder, propertyUpsertRequest(eventToken, dealFaseBody()))
	if recorder.Code != http.StatusForbidden {
		t.Fatalf("plugin callback: status=%d body=%s, want 403", recorder.Code, recorder.Body.String())
	}
}

func TestPluginProjectPropertyUpsertReconcilesSoftDrift(t *testing.T) {
	id, _ := installProjectPropertyPlugin(t)
	withCallbackTokens(t)
	withProjectPropertiesCapability(t, true)
	admin := issueCallbackToken(t, id, service.HookActor{Type: "member", ID: parseUUID(testUserID)})

	recorder := httptest.NewRecorder()
	testHandler.UpsertPluginProjectProperty(recorder, propertyUpsertRequest(admin, dealFaseBody()))
	if recorder.Code != http.StatusCreated {
		t.Fatalf("create: status=%d body=%s", recorder.Code, recorder.Body.String())
	}
	var def PropertyResponse
	if err := json.Unmarshal(recorder.Body.Bytes(), &def); err != nil {
		t.Fatal(err)
	}
	if def.ResourceType != propertyResourceTypeProject || def.OwnerInstallationID == nil || *def.OwnerInstallationID != id {
		t.Fatalf("definition not integration-owned: %+v", def)
	}
	leadID, meetingID := optionIDs(def)["Lead"], optionIDs(def)["Meeting booked"]
	if leadID == "" || meetingID == "" {
		t.Fatalf("options missing ids: %+v", def.Config.Options)
	}

	// Soft drift: rename by ID, add an option, drop none.
	body := map[string]any{
		"name": "Deal-fase", "type": "select", "description": "Attio deal stage",
		"config": map[string]any{"options": []map[string]any{
			{"id": leadID, "name": "Lead (renamed)", "color": "#ef4444"},
			{"id": meetingID, "name": "Meeting booked", "color": "#22c55e"},
			{"name": "Term sheet", "color": "#a855f7"},
		}},
	}
	recorder = httptest.NewRecorder()
	testHandler.UpsertPluginProjectProperty(recorder, propertyUpsertRequest(admin, body))
	if recorder.Code != http.StatusOK {
		t.Fatalf("drift: status=%d body=%s", recorder.Code, recorder.Body.String())
	}
	var updated PropertyResponse
	if err := json.Unmarshal(recorder.Body.Bytes(), &updated); err != nil {
		t.Fatal(err)
	}
	if updated.ID != def.ID {
		t.Fatalf("reconcile changed definition id: %s != %s", updated.ID, def.ID)
	}
	ids := optionIDs(updated)
	if ids["Lead (renamed)"] != leadID || ids["Meeting booked"] != meetingID {
		t.Fatalf("option ids not stable: %+v", ids)
	}
	if ids["Term sheet"] == "" {
		t.Fatalf("new option not added: %+v", ids)
	}
}

func TestPluginProjectPropertyValueWriteAndOwnership(t *testing.T) {
	id, token := installProjectPropertyPlugin(t)
	withCallbackTokens(t)
	withProjectPropertiesCapability(t, true)
	admin := issueCallbackToken(t, id, service.HookActor{Type: "member", ID: parseUUID(testUserID)})

	defRec := httptest.NewRecorder()
	testHandler.UpsertPluginProjectProperty(defRec, propertyUpsertRequest(admin, dealFaseBody()))
	if defRec.Code != http.StatusCreated {
		t.Fatalf("create definition: %d %s", defRec.Code, defRec.Body.String())
	}
	var def PropertyResponse
	if err := json.Unmarshal(defRec.Body.Bytes(), &def); err != nil {
		t.Fatal(err)
	}
	leadID := optionIDs(def)["Lead"]

	// A project created by the installation, then a property value written to
	// it via the standing credential.
	createBody := map[string]any{"operation_id": uuid.NewString(), "title": "Deal room", "description": nil}
	createRec := httptest.NewRecorder()
	testHandler.CreatePluginProject(createRec, projectRequest(token, createBody, ""))
	if createRec.Code != http.StatusCreated {
		t.Fatalf("create project: %d %s", createRec.Code, createRec.Body.String())
	}
	var project pluginProject
	if err := json.Unmarshal(createRec.Body.Bytes(), &project); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		testHandler.Queries.DeleteProject(context.Background(), db.DeleteProjectParams{ID: parseUUID(project.ID), WorkspaceID: parseUUID(testWorkspaceID)})
	})

	patchBody := map[string]any{"operation_id": uuid.NewString(), "expected_revision": project.Revision,
		"properties": map[string]any{def.ID: leadID}}
	patchRec := httptest.NewRecorder()
	testHandler.PatchPluginProject(patchRec, projectRequest(token, patchBody, project.ID))
	if patchRec.Code != http.StatusOK {
		t.Fatalf("write value: %d %s", patchRec.Code, patchRec.Body.String())
	}
	var withProps pluginProject
	if err := json.Unmarshal(patchRec.Body.Bytes(), &withProps); err != nil {
		t.Fatal(err)
	}
	var bag map[string]string
	if err := json.Unmarshal(withProps.Properties, &bag); err != nil {
		t.Fatal(err)
	}
	if bag[def.ID] != leadID {
		t.Fatalf("value not persisted: %s", withProps.Properties)
	}

	// A project property written with an unrelated (unknown) definition id fails.
	badBody := map[string]any{"operation_id": uuid.NewString(), "expected_revision": withProps.Revision,
		"properties": map[string]any{uuid.NewString(): leadID}}
	badRec := httptest.NewRecorder()
	testHandler.PatchPluginProject(badRec, projectRequest(token, badBody, project.ID))
	if badRec.Code != http.StatusBadRequest {
		t.Fatalf("unknown definition: status=%d body=%s, want 400", badRec.Code, badRec.Body.String())
	}
}

func TestProjectPropertiesResourceTypeAndCreationGate(t *testing.T) {
	// A fresh resolver each test avoids the resolver's 2s workspace-state cache.
	withProjectPropertiesCapability(t, true)
	body := map[string]any{
		"name": "Deal-fase", "type": "select", "resource_type": "project",
		"config": map[string]any{"options": []map[string]any{{"name": "Lead", "color": "#3b82f6"}}},
	}
	recorder := httptest.NewRecorder()
	testHandler.CreateProperty(recorder, newRequest("POST", "/api/properties", body))
	if recorder.Code != http.StatusCreated {
		t.Fatalf("create project property: %d %s", recorder.Code, recorder.Body.String())
	}
	var created PropertyResponse
	if err := json.Unmarshal(recorder.Body.Bytes(), &created); err != nil {
		t.Fatal(err)
	}
	if created.ResourceType != propertyResourceTypeProject {
		t.Fatalf("resource_type = %q, want project", created.ResourceType)
	}

	list := func(resourceType string) map[string]bool {
		w := httptest.NewRecorder()
		testHandler.ListProperties(w, newRequest("GET", "/api/properties?resource_type="+resourceType+"&include_archived=true", nil))
		if w.Code != http.StatusOK {
			t.Fatalf("list %s: %d %s", resourceType, w.Code, w.Body.String())
		}
		var resp struct {
			Properties []PropertyResponse `json:"properties"`
		}
		json.NewDecoder(w.Body).Decode(&resp)
		out := map[string]bool{}
		for _, p := range resp.Properties {
			out[p.ID] = true
		}
		return out
	}
	if !list("project")[created.ID] {
		t.Fatal("project definition missing from resource_type=project list")
	}
	if list("issue")[created.ID] {
		t.Fatal("project definition leaked into the issue property list")
	}
}

func TestProjectPropertyCreationRefusedWhenFlagOff(t *testing.T) {
	withProjectPropertiesCapability(t, false)
	body := map[string]any{
		"name": "Deal-fase", "type": "select", "resource_type": "project",
		"config": map[string]any{"options": []map[string]any{{"name": "Lead", "color": "#3b82f6"}}},
	}
	recorder := httptest.NewRecorder()
	testHandler.CreateProperty(recorder, newRequest("POST", "/api/properties", body))
	if recorder.Code != http.StatusForbidden {
		t.Fatalf("flag off: status=%d body=%s, want 403", recorder.Code, recorder.Body.String())
	}
}

func TestPluginProjectPropertyUpsertRefusedWhenFlagOff(t *testing.T) {
	id, _ := installProjectPropertyPlugin(t)
	withCallbackTokens(t)
	withProjectPropertiesCapability(t, false)

	admin := issueCallbackToken(t, id, service.HookActor{Type: "member", ID: parseUUID(testUserID)})
	recorder := httptest.NewRecorder()
	testHandler.UpsertPluginProjectProperty(recorder, propertyUpsertRequest(admin, dealFaseBody()))
	if recorder.Code != http.StatusForbidden {
		t.Fatalf("flag off: status=%d body=%s, want 403", recorder.Code, recorder.Body.String())
	}
}
