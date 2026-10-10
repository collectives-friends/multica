package handler

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/multica-ai/multica/server/internal/featureflags"
	"github.com/multica-ai/multica/server/internal/testutil"
	"github.com/multica-ai/multica/server/pkg/featureflag"
)

func TestWorkspaceFlagRoundTripAndAudit(t *testing.T) {
	old := testHandler.FlagResolver
	registry, err := featureflags.NewRegistry(append(featureflags.HostRegistry().Definitions(), featureflags.Definition{Key: "personal_test", ScopePolicy: featureflags.MemberOverridable}))
	if err != nil {
		t.Fatal(err)
	}
	testHandler.FlagResolver = featureflags.NewResolver(registry, nil, featureflags.DBSource{Queries: testHandler.Queries})
	t.Cleanup(func() { testHandler.FlagResolver = old })
	dbfx.Cleanup(t, "DELETE FROM workspace_feature_flag_override WHERE workspace_id=$1", testWorkspaceID)
	dbfx.Cleanup(t, "DELETE FROM workspace_feature_flag_audit WHERE workspace_id=$1", testWorkspaceID)
	request := func(method, key, body string) *http.Request {
		return pluginHandlerRequest(method, "/api/workspaces/"+testWorkspaceID+"/feature-flags/"+key, []byte(body), map[string]string{"id": testWorkspaceID, "key": key})
	}
	testutil.Call(t, testHandler.SetWorkspaceFeatureFlag, request("PUT", "personal_test", `{"enabled":true,"locked":false,"reason":"rollout"}`)).Want(200)
	testutil.Call(t, testHandler.SetMemberFeatureFlag, request("PUT", "personal_test", `{"enabled":false}`)).Want(200)
	var view featureflags.View
	testutil.Call(t, testHandler.GetFeatureFlags, request("GET", "", "")).Want(200).JSON(&view)
	if d := view.Resolved["personal_test"]; d.Value || d.Source != "member" {
		t.Fatalf("personal resolution: %+v", d)
	}
	testutil.Call(t, testHandler.SetWorkspaceFeatureFlag, request("PUT", "personal_test", `{"enabled":true,"locked":true}`)).Want(200)
	testutil.Call(t, testHandler.GetFeatureFlags, request("GET", "", "")).Want(200).JSON(&view)
	if d := view.Resolved["personal_test"]; !d.Value || !d.Locked {
		t.Fatalf("forced resolution: %+v", d)
	}
	testutil.Call(t, testHandler.DeleteMemberFeatureFlag, request("DELETE", "personal_test", "")).Want(204)
	testutil.Call(t, testHandler.DeleteWorkspaceFeatureFlag, request("DELETE", "personal_test", "")).Want(204)
	var audits struct {
		Entries []struct {
			Action     string `json:"action"`
			ActorType  string `json:"actor_type"`
			OldEnabled *bool  `json:"old_enabled"`
			NewEnabled *bool  `json:"new_enabled"`
		}
	}
	testutil.Call(t, testHandler.GetFeatureFlagAudit, request("GET", "", "")).Want(200).JSON(&audits)
	if len(audits.Entries) != 5 {
		t.Fatalf("want five audits, got %+v", audits)
	}
	if audits.Entries[0].Action != "clear" || audits.Entries[0].ActorType != "member" || audits.Entries[0].OldEnabled == nil || audits.Entries[0].NewEnabled != nil {
		t.Fatalf("bad clear audit: %+v", audits.Entries[0])
	}
}

func TestHostWorkspaceGate(t *testing.T) {
	dbfx.Insert(t, "workspace_feature_flag_override", testutil.Cols{"workspace_id": testWorkspaceID, "scope_type": "workspace", "scope_id": testWorkspaceID, "flag_key": "local_search_index", "enabled": false, "updated_by_type": "system"})
	ctx := featureflag.WithEvalContext(context.Background(), featureflag.EvalContext{WorkspaceID: testWorkspaceID, UserID: testUserID})
	req := httptest.NewRequest("GET", "/api/search-index", nil).WithContext(ctx)
	req.Header.Set("X-Workspace-ID", testWorkspaceID)
	recorder := httptest.NewRecorder()
	testHandler.RequireLocalSearchIndex(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(204) })).ServeHTTP(recorder, req)
	if recorder.Code != 404 {
		t.Fatalf("workspace override ignored: got %d", recorder.Code)
	}
}

func TestFlagPermissionsAndUnknownKeys(t *testing.T) {
	dbfx.Cleanup(t, "DELETE FROM workspace_feature_flag_override WHERE workspace_id=$1", testWorkspaceID)
	dbfx.Cleanup(t, "DELETE FROM workspace_feature_flag_audit WHERE workspace_id=$1", testWorkspaceID)
	req := func(key string) *http.Request {
		return pluginHandlerRequest("PUT", "/", []byte(`{"enabled":true}`), map[string]string{"id": testWorkspaceID, "key": key})
	}
	for _, tc := range []struct {
		key    string
		status int
		code   string
	}{{"not_real", 404, "flag_unknown"}, {"plugins_v1", 409, "flag_workspace_only"}, {"agents_agent_builder", 409, "flag_frozen"}} {
		recorder := httptest.NewRecorder()
		testHandler.SetMemberFeatureFlag(recorder, req(tc.key))
		if recorder.Code != tc.status {
			t.Fatalf("%s got %d: %s", tc.key, recorder.Code, recorder.Body.String())
		}
		var body map[string]any
		json.Unmarshal(recorder.Body.Bytes(), &body)
		if body["code"] != tc.code {
			t.Fatalf("missing code %s: %v", tc.code, body)
		}
	}
	user := dbfx.Insert(t, "user", testutil.Cols{"name": "Flag member", "email": "flag-member@example.test"})
	dbfx.Insert(t, "member", testutil.Cols{"workspace_id": testWorkspaceID, "user_id": user, "role": "member"})
	r := req("plugins_v1")
	r.Header.Set("X-User-ID", user)
	testutil.Call(t, testHandler.SetWorkspaceFeatureFlag, r).Want(403)
	testutil.Call(t, testHandler.GetFeatureFlagAudit, r).Want(403)
	r = req("plugins_v1")
	r.Header.Set("X-Actor-Source", "task_token")
	r.Header.Set("X-Workspace-ID", "11111111-1111-1111-1111-111111111111")
	testutil.Call(t, testHandler.SetWorkspaceFeatureFlag, r).Want(403)
	var count int
	if err := testPool.QueryRow(context.Background(), "SELECT count(*) FROM workspace_feature_flag_audit WHERE workspace_id=$1", testWorkspaceID).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Fatal("rejected writes created audits")
	}
}
