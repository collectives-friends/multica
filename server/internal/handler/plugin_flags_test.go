package handler

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/multica-ai/multica/server/internal/events"
	"github.com/multica-ai/multica/server/internal/featureflags"
	"github.com/multica-ai/multica/server/internal/service"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
	"github.com/multica-ai/multica/server/pkg/plugincontract"
	"github.com/multica-ai/multica/server/pkg/protocol"
)

func installFlagHandlerPlugin(t *testing.T) db.PluginInstallation {
	t.Helper()
	withPluginsV1Flag(t, testHandler, true)
	cleanupPluginInstallations(t)
	s := testHandler.PluginService
	oldFlags, oldResolver := s.FeatureFlags, s.FlagResolver
	s.FeatureFlags = testHandler.FeatureFlags
	s.FlagResolver = featureflags.NewResolver(featureflags.HostRegistry(), s.FeatureFlags, featureflags.DBSource{Queries: s.Queries, PluginDefinitions: func(ctx context.Context, w string) ([]featureflags.Definition, error) {
		return service.PluginFlagDefinitions(ctx, s.Queries, w)
	}})
	t.Cleanup(func() { s.FeatureFlags = oldFlags; s.FlagResolver = oldResolver })
	raw := strings.Replace(handlerTestManifest, `"manifest_version": 1`, `"manifest_version": 2`, 1)
	raw = strings.Replace(raw, `"contributes": {`, `"contributes": {"flags":[{"key":"review","label":"Review","default":true,"scope_policy":"member_overridable"}],`, 1)
	raw = strings.Replace(raw, `"key": "hello", "type"`, `"requires_flag":"review", "key": "hello", "type"`, 1)
	versionID := withLocalPluginSource(t, raw)
	installation, err := s.InstallPlugin(context.Background(), parseUUID(testWorkspaceID), parseUUID(testUserID), versionID, []string{"issues:read", "comments:write", "storage:user"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Uninstall(context.Background(), installation) })
	preview, err := s.PreviewPlugin(context.Background(), installation.WorkspaceID, versionID)
	if err != nil || len(preview.Flags) != 1 {
		t.Fatalf("preview flags=%+v %v", preview, err)
	}
	return installation
}
func setHandlerPluginMemberFlag(t *testing.T, installation db.PluginInstallation, enabled bool) {
	t.Helper()
	_, err := testHandler.Queries.UpsertFlagOverride(context.Background(), db.UpsertFlagOverrideParams{WorkspaceID: installation.WorkspaceID, ScopeType: "member", ScopeID: parseUUID(testUserID), FlagKey: service.PluginFlagKey(installation.PluginKey, "review"), Enabled: enabled, UpdatedByType: "member", UpdatedByID: parseUUID(testUserID)})
	if err != nil {
		t.Fatal(err)
	}
	testHandler.PluginService.FlagResolver.Invalidate(testWorkspaceID)
}
func TestPluginFlagsBridgeStopsAStaleSurface(t *testing.T) {
	installation := installFlagHandlerPlugin(t)
	request := func() *http.Request {
		r := pluginActionRequest(http.MethodGet, "/api/plugin-bridge/v1/context", uuidToString(installation.ID), nil, nil)
		r.Header.Set("X-Multica-Plugin-Surface", "hello")
		return r
	}
	recorder := httptest.NewRecorder()
	testHandler.GetPluginContext(recorder, request())
	if recorder.Code != http.StatusOK {
		t.Fatalf("enabled context=%d %s", recorder.Code, recorder.Body.String())
	}
	var payload struct {
		Flags map[string]bool `json:"flags"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &payload); err != nil {
		t.Fatal(err)
	}
	if !payload.Flags["review"] || !payload.Flags["host:plugins_v1"] {
		t.Fatalf("flags=%v", payload.Flags)
	}
	setHandlerPluginMemberFlag(t, installation, false)
	recorder = httptest.NewRecorder()
	testHandler.GetPluginContext(recorder, request())
	if recorder.Code != 403 || !strings.Contains(recorder.Body.String(), service.PluginContributionDisabled) {
		t.Fatalf("stale bridge=%d %s", recorder.Code, recorder.Body.String())
	}
}
func TestPluginFlagsSurfaceLaunchTokenRechecksItsMember(t *testing.T) {
	installation := installFlagHandlerPlugin(t)
	oldCfg, oldTokens := testHandler.cfg, testHandler.PluginSurfaceTokens
	testHandler.cfg.PluginSurfaceOrigin = "https://plugin-content.example.test"
	testHandler.PluginSurfaceTokens = pluginSurfaceTokenHandler(t).PluginSurfaceTokens
	t.Cleanup(func() { testHandler.cfg = oldCfg; testHandler.PluginSurfaceTokens = oldTokens })
	r := pluginHandlerRequest(http.MethodGet, "/launch", nil, map[string]string{"id": testWorkspaceID, "installationId": uuidToString(installation.ID), "surfaceKey": "hello"})
	recorder := httptest.NewRecorder()
	testHandler.GetPluginSurfaceLaunch(recorder, r)
	if recorder.Code != 200 {
		t.Fatalf("launch=%d %s", recorder.Code, recorder.Body.String())
	}
	var launch pluginSurfaceLaunch
	if err := json.Unmarshal(recorder.Body.Bytes(), &launch); err != nil {
		t.Fatal(err)
	}
	parsed, err := url.Parse(launch.URL)
	if err != nil {
		t.Fatal(err)
	}
	setHandlerPluginMemberFlag(t, installation, false)
	request := httptest.NewRequest(http.MethodGet, launch.URL, nil)
	route := chi.NewRouteContext()
	route.URLParams.Add("token", strings.TrimPrefix(parsed.Path, "/plugin-surfaces/"))
	request = request.WithContext(context.WithValue(request.Context(), chi.RouteCtxKey, route))
	recorder = httptest.NewRecorder()
	testHandler.ServePluginSurface(recorder, request)
	if recorder.Code != 403 || !strings.Contains(recorder.Body.String(), service.PluginContributionDisabled) {
		t.Fatalf("stale content=%d %s", recorder.Code, recorder.Body.String())
	}
	recorder = httptest.NewRecorder()
	testHandler.GetPluginSurfaceLaunch(recorder, r)
	if recorder.Code != 403 || !strings.Contains(recorder.Body.String(), service.PluginContributionDisabled) {
		t.Fatalf("disabled launch=%d %s", recorder.Code, recorder.Body.String())
	}
}
func TestPluginFlagLifecycleSharesWorkspaceWriteLock(t *testing.T) {
	installation := installFlagHandlerPlugin(t)
	tx, err := testPool.Begin(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(context.Background())
	if err := testHandler.Queries.WithTx(tx).LockWorkspaceFlagWrites(context.Background(), testWorkspaceID); err != nil {
		t.Fatal(err)
	}
	result := make(chan error, 1)
	go func() {
		_, err := testHandler.PluginService.SetEnabled(context.Background(), installation, false)
		result <- err
	}()
	select {
	case err := <-result:
		t.Fatalf("mutation escaped workspace lock: %v", err)
	case <-time.After(50 * time.Millisecond):
	}
	if err := tx.Commit(context.Background()); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-result:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("mutation did not resume after lock release")
	}
}
func TestPluginFlagsUnknownHostRefRefusedAtPublish(t *testing.T) {
	installFlagHandlerPlugin(t)
	raw := strings.Replace(handlerTestManifest, `"manifest_version": 1`, `"manifest_version": 2`, 1)
	raw = strings.Replace(raw, `"key": "hello", "type"`, `"requires_flag":"host:local_search_index", "key": "hello", "type"`, 1)
	dir := t.TempDir()
	writeLocalPluginManifest(t, dir, raw)
	old := testHandler.PluginService.LocalDir
	testHandler.PluginService.LocalDir = dir
	t.Cleanup(func() { testHandler.PluginService.LocalDir = old })
	_, err := testHandler.PluginService.PublishLocalBundle(context.Background(), parseUUID(testWorkspaceID), parseUUID(testUserID), "hello")
	if err == nil || !strings.Contains(err.Error(), "non-public or unknown") {
		t.Fatalf("private host reference error=%v", err)
	}
}

func TestPluginFlagsInstallationPayloadFiltersContributionsForMember(t *testing.T) {
	installation := installFlagHandlerPlugin(t)
	manifest, err := service.ParseInstallationManifest(installation)
	if err != nil {
		t.Fatal(err)
	}
	manifest.Scopes = append(manifest.Scopes, "net:example.com")
	manifest.Contributes.Hooks = []plugincontract.Hook{
		{Key: "review", Name: "Review", Description: "Review", RequiresFlag: "review", Triggers: []string{"manual", "agent"}, Transport: plugincontract.HookTransport{Type: "http", URL: "https://example.com/review"}},
		{Key: "review_background", Name: "Background review", Description: "Review", RequiresFlag: "review", Triggers: []string{"agent"}, Transport: plugincontract.HookTransport{Type: "http", URL: "https://example.com/review"}},
	}
	manifest.Contributes.Resources = []plugincontract.Resource{{Type: "skill", Key: "review", Entry: "skills/review/SKILL.md", RequiresFlag: "review"}}
	raw, _ := json.Marshal(manifest)
	_, err = testHandler.Queries.UpdatePluginInstallationManifest(context.Background(), db.UpdatePluginInstallationManifestParams{ID: installation.ID, PackageVersionID: installation.PackageVersionID, Version: installation.Version, Manifest: raw, GrantedScopes: installation.GrantedScopes, Config: installation.Config})
	if err != nil {
		t.Fatal(err)
	}
	setHandlerPluginMemberFlag(t, installation, false)
	recorder := httptest.NewRecorder()
	testHandler.ListPlugins(recorder, pluginHandlerRequest(http.MethodGet, "/plugins", nil, map[string]string{"id": testWorkspaceID}))
	if recorder.Code != 200 {
		t.Fatalf("list=%d %s", recorder.Code, recorder.Body.String())
	}
	var payload struct {
		Plugins []pluginInstallationResponse `json:"plugins"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &payload); err != nil {
		t.Fatal(err)
	}
	rows := payload.Plugins
	if len(rows) != 1 || len(rows[0].Surfaces) != 0 || len(rows[0].Hooks) != 1 || rows[0].Hooks[0].Key != "review_background" || len(rows[0].Resources) != 1 {
		t.Fatalf("member preference must preserve workspace contributions: %+v", rows)
	}
	_, err = testHandler.Queries.UpsertFlagOverride(context.Background(), db.UpsertFlagOverrideParams{WorkspaceID: installation.WorkspaceID, ScopeType: "workspace", ScopeID: installation.WorkspaceID, FlagKey: service.PluginFlagKey(installation.PluginKey, "review"), Enabled: false, UpdatedByType: "member", UpdatedByID: parseUUID(testUserID)})
	if err != nil {
		t.Fatal(err)
	}
	testHandler.PluginService.FlagResolver.Invalidate(testWorkspaceID)
	recorder = httptest.NewRecorder()
	testHandler.ListPlugins(recorder, pluginHandlerRequest(http.MethodGet, "/plugins", nil, map[string]string{"id": testWorkspaceID}))
	if recorder.Code != 200 {
		t.Fatalf("workspace-off list=%d %s", recorder.Code, recorder.Body.String())
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &payload); err != nil {
		t.Fatal(err)
	}
	rows = payload.Plugins
	if len(rows) != 1 || len(rows[0].Surfaces) != 0 || len(rows[0].Hooks) != 0 || len(rows[0].Resources) != 0 {
		t.Fatalf("workspace-disabled contributions visible: %+v", rows)
	}
}
func TestPluginFlagsCallbackRejectsRemovedHook(t *testing.T) {
	withPluginsV1Flag(t, testHandler, true)
	cleanupPluginInstallations(t)
	withCallbackTokens(t)
	installationID := installHookPlugin(t)
	token := issueCallbackToken(t, installationID, service.HookActor{Type: "plugin", ID: parseUUID(installationID)})
	installation, err := testHandler.Queries.GetPluginInstallation(context.Background(), parseUUID(installationID))
	if err != nil {
		t.Fatal(err)
	}
	var manifest map[string]any
	if err := json.Unmarshal(installation.Manifest, &manifest); err != nil {
		t.Fatal(err)
	}
	hooks := manifest["contributes"].(map[string]any)["hooks"].([]any)
	manifest["contributes"].(map[string]any)["hooks"] = hooks[1:]
	raw, _ := json.Marshal(manifest)
	_, err = testHandler.Queries.UpdatePluginInstallationManifest(context.Background(), db.UpdatePluginInstallationManifestParams{ID: installation.ID, PackageVersionID: installation.PackageVersionID, Version: installation.Version, Manifest: raw, GrantedScopes: installation.GrantedScopes, Config: installation.Config})
	if err != nil {
		t.Fatal(err)
	}
	recorder := httptest.NewRecorder()
	testHandler.GetPluginContext(recorder, callbackRequest(token, http.MethodGet, "/v1/context", nil, nil))
	if recorder.Code != 403 || !strings.Contains(recorder.Body.String(), service.PluginContributionDisabled) {
		t.Fatalf("removed hook callback=%d %s", recorder.Code, recorder.Body.String())
	}
}
func TestPluginFlagLifecyclePublishesCatalogInvalidation(t *testing.T) {
	installation := installFlagHandlerPlugin(t)
	oldBus := testHandler.Bus
	testHandler.Bus = events.New()
	t.Cleanup(func() { testHandler.Bus = oldBus })
	var received []events.Event
	testHandler.Bus.Subscribe(protocol.EventFeatureFlagChanged, func(event events.Event) { received = append(received, event) })
	request := func() *http.Request {
		return pluginHandlerRequest(http.MethodPost, "/plugins", nil, map[string]string{"id": testWorkspaceID, "installationId": uuidToString(installation.ID)})
	}
	recorder := httptest.NewRecorder()
	testHandler.DisablePlugin(recorder, request())
	if recorder.Code != 200 {
		t.Fatal(recorder.Body.String())
	}
	recorder = httptest.NewRecorder()
	testHandler.EnablePlugin(recorder, request())
	if recorder.Code != 200 {
		t.Fatal(recorder.Body.String())
	}
	body, _ := json.Marshal(map[string]any{"version_id": uuidToString(installation.PackageVersionID), "granted_scopes": []string{"issues:read", "comments:write", "storage:user"}})
	recorder = httptest.NewRecorder()
	testHandler.InstallPlugin(recorder, pluginHandlerRequest(http.MethodPost, "/plugins", body, map[string]string{"id": testWorkspaceID}))
	if recorder.Code != 201 {
		t.Fatal(recorder.Body.String())
	}
	recorder = httptest.NewRecorder()
	testHandler.UninstallPlugin(recorder, request())
	if recorder.Code != 204 {
		t.Fatal(recorder.Body.String())
	}
	if len(received) != 4 {
		t.Fatalf("catalog invalidations=%d, want4", len(received))
	}
	for _, event := range received {
		payload := event.Payload.(map[string]any)
		if event.WorkspaceID != testWorkspaceID || event.RecipientUserID != "" || payload["action"] != "catalog_changed" || payload["key"] != featureflags.PluginsV1 || payload["enabled"] != nil || payload["locked"] != nil {
			t.Fatalf("event=%+v", event)
		}
	}
}

func TestPluginFlagsUpgradeRefreshesPreparedRequestSnapshot(t *testing.T) {
	installation := installFlagHandlerPlugin(t)
	manifest, err := service.ParseInstallationManifest(installation)
	if err != nil {
		t.Fatal(err)
	}
	manifest.Version = "2.0.0"
	manifest.Contributes.Flags[0].Key = "new_review"
	manifest.Contributes.Surfaces[0].RequiresFlag = "new_review"
	raw, _ := json.Marshal(manifest)
	versionID := withLocalPluginSource(t, string(raw))
	body, _ := json.Marshal(map[string]any{"version_id": versionID, "granted_scopes": manifest.Scopes})
	request := pluginHandlerRequest(http.MethodPost, "/plugins", body, map[string]string{"id": testWorkspaceID})
	prepared, err := testHandler.PluginService.FlagResolver.Prepare(request.Context(), testWorkspaceID, testUserID)
	if err != nil {
		t.Fatal(err)
	}
	recorder := httptest.NewRecorder()
	testHandler.InstallPlugin(recorder, request.WithContext(prepared))
	if recorder.Code != 201 {
		t.Fatalf("upgrade=%d %s", recorder.Code, recorder.Body.String())
	}
	var payload pluginInstallationResponse
	if err := json.Unmarshal(recorder.Body.Bytes(), &payload); err != nil {
		t.Fatal(err)
	}
	if len(payload.Surfaces) != 1 || payload.Surfaces[0].RequiresFlag != "new_review" {
		t.Fatalf("upgrade retained old prepared flags: %+v", payload.Surfaces)
	}
}
