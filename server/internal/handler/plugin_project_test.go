package handler

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync"
	"testing"

	"github.com/google/uuid"
	"github.com/multica-ai/multica/server/internal/featureflags"
	"github.com/multica-ai/multica/server/internal/service"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
	"github.com/multica-ai/multica/server/pkg/featureflag"
	"github.com/multica-ai/multica/server/pkg/plugincontract"
)

func integrationPlugin(t *testing.T) (string, string, *featureflag.StaticProvider) {
	t.Helper()
	id := installPluginForAction(t, []string{"projects:read", "projects:write", "installation:read"})
	old := testHandler.PluginService.FeatureFlags
	provider := featureflag.NewStaticProvider()
	provider.Set(featureflags.PluginsV1, featureflag.Rule{Default: true})
	testHandler.PluginService.FeatureFlags = featureflag.NewService(provider)
	t.Cleanup(func() { testHandler.PluginService.FeatureFlags = old })
	token, err := testHandler.PluginService.IssueInstallToken(context.Background(), parseUUID(id))
	if err != nil {
		t.Fatal(err)
	}
	return id, token, provider
}
func projectRequest(token string, body any, id string) *http.Request {
	params := map[string]string{}
	if id != "" {
		params["project_id"] = id
	}
	return pluginInstallTokenRequest("POST", "/v1/projects", token, body, params)
}
func TestPluginProjectGateScopesAndOperationReplay(t *testing.T) {
	id, token, provider := integrationPlugin(t)
	body := map[string]any{"operation_id": uuid.NewString(), "title": "Isolated integration project", "description": nil}
	rec := httptest.NewRecorder()
	testHandler.CreatePluginProject(rec, projectRequest(token, body, ""))
	if rec.Code != 403 {
		t.Fatalf("default-off bypass: %d %s", rec.Code, rec.Body.String())
	}
	provider.Set(featureflags.PluginIntegrationsV1, featureflag.Rule{Default: true})
	rec = httptest.NewRecorder()
	testHandler.CreatePluginProject(rec, projectRequest(token, body, ""))
	if rec.Code != 201 {
		t.Fatalf("create: %d %s", rec.Code, rec.Body.String())
	}
	var project pluginProject
	if err := json.Unmarshal(rec.Body.Bytes(), &project); err != nil {
		t.Fatal(err)
	}
	if project.ID != body["operation_id"] {
		t.Fatalf("create readback identity %s differs from operation", project.ID)
	}
	t.Cleanup(func() {
		testHandler.Queries.DeleteProject(context.Background(), db.DeleteProjectParams{ID: parseUUID(project.ID), WorkspaceID: parseUUID(testWorkspaceID)})
	})
	replay := httptest.NewRecorder()
	testHandler.CreatePluginProject(replay, projectRequest(token, body, ""))
	if replay.Code != 200 || !sameJSON(replay.Body.Bytes(), rec.Body.Bytes()) {
		t.Fatalf("receipt replay: %d %s", replay.Code, replay.Body.String())
	}
	body["title"] = "Different payload"
	conflict := httptest.NewRecorder()
	testHandler.CreatePluginProject(conflict, projectRequest(token, body, ""))
	if conflict.Code != 409 {
		t.Fatalf("reused key=%d", conflict.Code)
	}
	// A normal native edit increments the same revision, so integration CAS sees it.
	_, err := testHandler.DB.Exec(context.Background(), "UPDATE project SET title='Manual edit' WHERE id=$1", project.ID)
	if err != nil {
		t.Fatal(err)
	}
	patch := map[string]any{"operation_id": uuid.NewString(), "expected_revision": project.Revision, "title": "Overwrite"}
	conflict = httptest.NewRecorder()
	testHandler.PatchPluginProject(conflict, projectRequest(token, patch, project.ID))
	if conflict.Code != 409 {
		t.Fatalf("manual edit CAS=%d %s", conflict.Code, conflict.Body.String())
	}
	patch["expected_revision"] = project.Revision + 1
	patch["description"] = nil
	updated := httptest.NewRecorder()
	testHandler.PatchPluginProject(updated, projectRequest(token, patch, project.ID))
	if updated.Code != 200 {
		t.Fatalf("patch=%d %s", updated.Code, updated.Body.String())
	}
	// Disabled and revoked credentials stop a subsequent dispatch, even with a receipt.
	if err := testHandler.PluginService.RevokeInstallToken(context.Background(), parseUUID(id)); err != nil {
		t.Fatal(err)
	}
	denied := httptest.NewRecorder()
	testHandler.ListPluginProjects(denied, projectRequest(token, nil, ""))
	if denied.Code != 403 {
		t.Fatalf("revoke=%d", denied.Code)
	}
}
func TestPluginProjectConcurrentCreateAndCAS(t *testing.T) {
	_, token, provider := integrationPlugin(t)
	provider.Set(featureflags.PluginIntegrationsV1, featureflag.Rule{Default: true})
	body := map[string]any{"operation_id": uuid.NewString(), "title": "Concurrent project"}
	var wg sync.WaitGroup
	responses := make([]*httptest.ResponseRecorder, 2)
	for i := range responses {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			responses[i] = httptest.NewRecorder()
			testHandler.CreatePluginProject(responses[i], projectRequest(token, body, ""))
		}(i)
	}
	wg.Wait()
	if !sameJSON(responses[0].Body.Bytes(), responses[1].Body.Bytes()) {
		t.Fatalf("duplicate create receipts differ: %s %s", responses[0].Body.String(), responses[1].Body.String())
	}
	var project pluginProject
	json.Unmarshal(responses[0].Body.Bytes(), &project)
	if project.ID == "" {
		t.Fatal(responses[0].Body.String())
	}
	t.Cleanup(func() {
		testHandler.Queries.DeleteProject(context.Background(), db.DeleteProjectParams{ID: parseUUID(project.ID), WorkspaceID: parseUUID(testWorkspaceID)})
	})
	for i := range responses {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			responses[i] = httptest.NewRecorder()
			testHandler.PatchPluginProject(responses[i], projectRequest(token, map[string]any{"operation_id": uuid.NewString(), "expected_revision": project.Revision, "title": "Concurrent patch"}, project.ID))
		}(i)
	}
	wg.Wait()
	if !(responses[0].Code == 200 && responses[1].Code == 409 || responses[1].Code == 200 && responses[0].Code == 409) {
		t.Fatalf("CAS race=%d/%d", responses[0].Code, responses[1].Code)
	}
}
func TestPluginAuthorizationBindsInputAndRechecksAdmin(t *testing.T) {
	id, standing, provider := integrationPlugin(t)
	provider.Set(featureflags.PluginIntegrationsV1, featureflag.Rule{Default: true})
	installation, err := testHandler.Queries.GetPluginInstallation(context.Background(), parseUUID(id))
	if err != nil {
		t.Fatal(err)
	}
	// Add a real declared hook: unknown hook grants must fail the caller check.
	var manifest map[string]any
	json.Unmarshal(installation.Manifest, &manifest)
	manifest["contributes"].(map[string]any)["hooks"] = []any{map[string]any{"key": "action", "name": "Action", "description": "A test action", "triggers": []string{"ui"}, "transport": map[string]any{"type": "http", "url": "https://example.com/hook"}}}
	manifest["scopes"] = []string{"projects:read", "projects:write", "installation:read", "net:example.com"}
	raw, _ := json.Marshal(manifest)
	_, err = testHandler.DB.Exec(context.Background(), "UPDATE plugin_installation SET manifest=$1 WHERE id=$2", raw, id)
	if err != nil {
		t.Fatal(err)
	}
	installation.Manifest = raw
	old := testHandler.PluginService.Callbacks
	testHandler.PluginService.Callbacks = service.NewCallbackTokens()
	t.Cleanup(func() { testHandler.PluginService.Callbacks = old })
	input := map[string]any{"action": "publish", "revision": 7}
	callback, err := testHandler.PluginService.Callbacks.Issue(context.Background(), service.HookInvocation{Installation: installation, Hook: plugincontract.Hook{Key: "action"}, Trigger: "ui", Actor: service.HookActor{Type: "member", ID: parseUUID(testUserID)}, Input: input})
	if err != nil {
		t.Fatal(err)
	}
	probe := func(token string) *httptest.ResponseRecorder {
		r := httptest.NewRecorder()
		testHandler.GetPluginInstallationAuthorization(r, pluginInstallTokenRequest("GET", "/v1/installation/authorization", token, nil, nil))
		return r
	}
	admin := probe(callback)
	if admin.Code != 200 || !strings.Contains(admin.Body.String(), `"admin":true`) || !strings.Contains(admin.Body.String(), `"action":"publish"`) {
		t.Fatalf("callback proof=%d %s", admin.Code, admin.Body.String())
	}
	if p := probe(standing); p.Code != 200 || !strings.Contains(p.Body.String(), `"admin":false`) {
		t.Fatalf("standing escalation=%d %s", p.Code, p.Body.String())
	}
	_, err = testHandler.DB.Exec(context.Background(), "UPDATE member SET role='member' WHERE workspace_id=$1 AND user_id=$2", testWorkspaceID, testUserID)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		testHandler.DB.Exec(context.Background(), "UPDATE member SET role='owner' WHERE workspace_id=$1 AND user_id=$2", testWorkspaceID, testUserID)
	})
	if p := probe(callback); p.Code != 200 || !strings.Contains(p.Body.String(), `"admin":false`) {
		t.Fatalf("demotion ignored=%d %s", p.Code, p.Body.String())
	}
	writes := httptest.NewRecorder()
	testHandler.CreatePluginProject(writes, projectRequest(callback, map[string]any{"operation_id": uuid.NewString(), "title": "Forbidden"}, ""))
	if writes.Code != 403 {
		t.Fatalf("callback project writes=%d", writes.Code)
	}
	contextRead := httptest.NewRecorder()
	testHandler.GetPluginContext(contextRead, pluginInstallTokenRequest("GET", "/v1/context?project_id="+uuid.NewString(), callback, nil, nil))
	if contextRead.Code != 403 {
		t.Fatalf("callback project context=%d", contextRead.Code)
	}
	testHandler.PluginService.Callbacks.Revoke(callback)
	if p := probe(callback); p.Code != 403 {
		t.Fatalf("callback revocation=%d", p.Code)
	}
}

func sameJSON(a, b []byte) bool {
	var x, y any
	if json.Unmarshal(a, &x) != nil || json.Unmarshal(b, &y) != nil {
		return false
	}
	return reflect.DeepEqual(x, y)
}

func TestPluginProjectsRejectCrossWorkspaceAndDisabledDirectCalls(t *testing.T) {
	id, token, provider := integrationPlugin(t)
	provider.Set(featureflags.PluginIntegrationsV1, featureflag.Rule{Default: true})
	foreign := dbfx.Workspace(t, "Foreign project workspace", "project-"+uuid.NewString())
	p, err := testHandler.Queries.CreatePluginProject(context.Background(), db.CreatePluginProjectParams{ID: parseUUID(uuid.NewString()), WorkspaceID: parseUUID(foreign), Title: "Private foreign project", Status: "planned"})
	if err != nil {
		t.Fatal(err)
	}
	get := httptest.NewRecorder()
	r := projectRequest(token, nil, uuidToString(p.ID))
	r.Header.Set("X-Workspace-ID", foreign)
	testHandler.GetPluginProject(get, r)
	if get.Code != 404 {
		t.Fatalf("foreign get=%d", get.Code)
	}
	patch := httptest.NewRecorder()
	testHandler.PatchPluginProject(patch, projectRequest(token, map[string]any{"operation_id": uuid.NewString(), "expected_revision": p.Revision, "title": "cross tenant"}, uuidToString(p.ID)))
	if patch.Code != 409 {
		t.Fatalf("foreign patch=%d", patch.Code)
	}
	collision := httptest.NewRecorder()
	testHandler.CreatePluginProject(collision, projectRequest(token, map[string]any{"operation_id": uuidToString(p.ID), "title": "Must not rebind"}, ""))
	if collision.Code != 409 {
		t.Fatalf("foreign identity collision=%d", collision.Code)
	}
	_, err = testHandler.DB.Exec(context.Background(), "UPDATE plugin_installation SET enabled=false WHERE id=$1", id)
	if err != nil {
		t.Fatal(err)
	}
	disabled := httptest.NewRecorder()
	testHandler.ListPluginProjects(disabled, projectRequest(token, nil, ""))
	if disabled.Code != 403 {
		t.Fatalf("disabled direct API=%d", disabled.Code)
	}
}

func TestPluginProjectContextRequiresReadConsent(t *testing.T) {
	id := installPluginForAction(t, []string{"issues:read"})
	token, err := testHandler.PluginService.IssueInstallToken(context.Background(), parseUUID(id))
	if err != nil {
		t.Fatal(err)
	}
	r := httptest.NewRecorder()
	testHandler.GetPluginContext(r, pluginInstallTokenRequest("GET", "/v1/context?project_id="+uuid.NewString(), token, nil, nil))
	if r.Code != 403 {
		t.Fatalf("missing project consent=%d %s", r.Code, r.Body.String())
	}
}
