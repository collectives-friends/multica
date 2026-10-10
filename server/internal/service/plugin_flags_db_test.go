package service

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/multica-ai/multica/server/internal/featureflags"
	"github.com/multica-ai/multica/server/internal/testutil"
	"github.com/multica-ai/multica/server/internal/util"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
	"github.com/multica-ai/multica/server/pkg/featureflag"
	"github.com/multica-ai/multica/server/pkg/plugincontract"
)

func pluginFlagsDB(t *testing.T) (context.Context, *PluginService, pgx.Tx, pgtype.UUID, pgtype.UUID) {
	t.Helper()
	ctx := context.Background()
	url := os.Getenv("DATABASE_URL")
	if url == "" {
		t.Skip("isolated DATABASE_URL required")
	}
	pool, err := pgxpool.New(ctx, url)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	fx := testutil.New(pool, "", "")
	suffix := fmt.Sprint(time.Now().UnixNano())
	w := util.MustParseUUID(fx.Workspace(t, "Flag test", "flag-test-"+suffix))
	u := util.MustParseUUID(fx.User(t, "Flag user", "flags-"+suffix+"@example.com"))
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = tx.Rollback(ctx) })
	s := NewPluginService(db.New(tx), tx)
	provider := featureflag.NewStaticProvider()
	provider.Set(featureflags.PluginsV1, featureflag.Rule{Default: true})
	s.FeatureFlags = featureflag.NewService(provider)
	s.FlagResolver = featureflags.NewResolver(featureflags.HostRegistry(), s.FeatureFlags, featureflags.DBSource{Queries: s.Queries, PluginDefinitions: func(ctx context.Context, w string) ([]featureflags.Definition, error) {
		return PluginFlagDefinitions(ctx, s.Queries, w)
	}})
	return ctx, s, tx, w, u
}
func publishFlagFixture(t *testing.T, ctx context.Context, s *PluginService, w, u pgtype.UUID, version string, review bool) db.PluginInstallation {
	t.Helper()
	installation := flagTestPlugin(t)
	var manifest plugincontract.Manifest
	if err := json.Unmarshal(installation.Manifest, &manifest); err != nil {
		t.Fatal(err)
	}
	manifest.Version = version
	manifest.Scopes = []string{"issues:read", "net:example.com"}
	manifest.Contributes.Flags = append(manifest.Contributes.Flags, plugincontract.FlagDefinition{Key: "keep", Label: "Keep", Default: true, ScopePolicy: plugincontract.FlagScopeWorkspaceOnly})
	if !review {
		manifest.Contributes.Flags = manifest.Contributes.Flags[1:]
		manifest.Contributes.Surfaces[0].RequiresFlag = "keep"
	}
	manifest.Contributes.Hooks = []plugincontract.Hook{{Key: "review", Name: "Review", Description: "Review an issue", RequiresFlag: manifest.Contributes.Surfaces[0].RequiresFlag, Triggers: []string{"agent", "event", "manual"}, Events: []string{"issue.created"}, Transport: plugincontract.HookTransport{Type: "http", URL: "https://example.com/hooks/review"}}}
	raw, err := json.Marshal(manifest)
	if err != nil {
		t.Fatal(err)
	}
	bundle, err := plugincontract.ParseBundleFromDir(func(entry string) ([]byte, bool, error) {
		if entry == plugincontract.ManifestFilename {
			return raw, true, nil
		}
		return []byte("console.log('fixture');"), true, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	summary, err := s.publish(ctx, w, u, bundle, false)
	if err != nil {
		t.Fatal(err)
	}
	versionID := ""
	for _, v := range summary.Versions {
		if v.Version == version {
			versionID = v.ID
		}
	}
	if versionID == "" {
		t.Fatal("published version absent")
	}
	installed, err := s.InstallPlugin(ctx, w, u, versionID, manifest.Scopes)
	if err != nil {
		t.Fatal(err)
	}
	return installed
}
func overrideFlagFixture(t *testing.T, ctx context.Context, s *PluginService, w, u pgtype.UUID, key string, enabled bool, scope string) {
	t.Helper()
	scopeID := w
	if scope == "member" {
		scopeID = u
	}
	_, err := s.Queries.UpsertFlagOverride(ctx, db.UpsertFlagOverrideParams{WorkspaceID: w, ScopeType: scope, ScopeID: scopeID, FlagKey: key, Enabled: enabled, UpdatedByType: "member", UpdatedByID: u})
	if err != nil {
		t.Fatal(err)
	}
	s.FlagResolver.Invalidate(uuidString(w))
}
func TestPluginFlagLifecycleRetainsAuditAndUnchangedOverrides(t *testing.T) {
	ctx, s, tx, w, u := pluginFlagsDB(t)
	installation := publishFlagFixture(t, ctx, s, w, u, "1.0.0", true)
	review := PluginFlagKey(installation.PluginKey, "review")
	keep := PluginFlagKey(installation.PluginKey, "keep")
	overrideFlagFixture(t, ctx, s, w, u, review, false, "workspace")
	overrideFlagFixture(t, ctx, s, w, u, review, true, "member")
	overrideFlagFixture(t, ctx, s, w, u, keep, false, "workspace")
	before, err := s.Queries.GetFlagOverride(ctx, db.GetFlagOverrideParams{WorkspaceID: w, ScopeType: "workspace", ScopeID: w, FlagKey: keep})
	if err != nil {
		t.Fatal(err)
	}
	installation = publishFlagFixture(t, ctx, s, w, u, "1.1.0", false)
	after, err := s.Queries.GetFlagOverride(ctx, db.GetFlagOverrideParams{WorkspaceID: w, ScopeType: "workspace", ScopeID: w, FlagKey: keep})
	if err != nil {
		t.Fatal(err)
	}
	if before.UpdatedAt != after.UpdatedAt || after.Enabled {
		t.Fatal("retained override changed")
	}
	var count int
	if err := tx.QueryRow(ctx, "SELECT count(*) FROM workspace_feature_flag_audit WHERE workspace_id=$1 AND flag_key=$2 AND action='clear' AND actor_type='system' AND reason='plugin_upgrade'", w, review).Scan(&count); err != nil || count != 2 {
		t.Fatalf("upgrade audits=%d err=%v", count, err)
	}
	installation, err = s.SetEnabled(ctx, installation, false)
	if err != nil {
		t.Fatal(err)
	}
	defs, err := PluginFlagDefinitions(ctx, s.Queries, uuidString(w))
	if err != nil || len(defs) != 0 {
		t.Fatalf("disabled definitions: %v %v", defs, err)
	}
	if _, err := s.Queries.GetFlagOverride(ctx, db.GetFlagOverrideParams{WorkspaceID: w, ScopeType: "workspace", ScopeID: w, FlagKey: keep}); err != nil {
		t.Fatal("disable removed overrides", err)
	}
	installation, err = s.SetEnabled(ctx, installation, true)
	if err != nil {
		t.Fatal(err)
	}
	overrideFlagFixture(t, ctx, s, w, u, "plugin:com.example.other:keep", true, "workspace")
	if err := s.Uninstall(ctx, installation); err != nil {
		t.Fatal(err)
	}
	if err := tx.QueryRow(ctx, "SELECT count(*) FROM workspace_feature_flag_audit WHERE workspace_id=$1", w).Scan(&count); err != nil || count != 3 {
		t.Fatalf("audit retained count=%d err=%v", count, err)
	}
	rows, err := s.Queries.ListWorkspaceFlagOverrides(ctx, w)
	if err != nil || len(rows) != 1 || rows[0].FlagKey != "plugin:com.example.other:keep" {
		t.Fatalf("uninstall prefix cleanup: %+v %v", rows, err)
	}
}
func TestPluginFlagGatesAgentToolsAndRecordsRefusedAttempt(t *testing.T) {
	ctx, s, _, w, u := pluginFlagsDB(t)
	installation := publishFlagFixture(t, ctx, s, w, u, "1.0.0", true)
	key := PluginFlagKey(installation.PluginKey, "review")
	tools, err := s.AgentHookTools(ctx, w)
	if err != nil || len(tools) != 1 {
		t.Fatalf("enabled tools=%v %v", tools, err)
	}
	overrideFlagFixture(t, ctx, s, w, u, key, false, "workspace")
	tools, err = s.AgentHookTools(ctx, w)
	if err != nil || len(tools) != 0 {
		t.Fatalf("disabled tools=%v %v", tools, err)
	}
	hook, err := FindHook(installation, "review")
	if err != nil {
		t.Fatal(err)
	}
	result, err := s.InvokeHook(ctx, HookInvocation{Installation: installation, Hook: hook, Trigger: "event", Actor: HookActor{Type: "plugin", ID: installation.ID}}, 1)
	if err == nil || result.Status != "refused" || result.Error != "flag_off" {
		t.Fatalf("result=%+v err=%v", result, err)
	}
	rows, err := s.Queries.ListPluginInvocations(ctx, db.ListPluginInvocationsParams{InstallationID: installation.ID, Limit: 10})
	if err != nil || len(rows) != 1 || rows[0].Status != "refused" || rows[0].Error.String != "flag_off" {
		t.Fatalf("audit rows=%+v err=%v", rows, err)
	}
	// Background delivery ignores member preferences; UI honors them.
	overrideFlagFixture(t, ctx, s, w, u, key, true, "workspace")
	overrideFlagFixture(t, ctx, s, w, u, key, false, "member")
	if !s.ContributionActive(ctx, installation, hook.RequiresFlag, "") || s.ContributionActive(ctx, installation, hook.RequiresFlag, uuidString(u)) {
		t.Fatal("actor scope was not honored")
	}
	// A delayed call holds an old enabled row, but execution reloads installation state.
	if _, err := s.SetEnabled(ctx, installation, false); err != nil {
		t.Fatal(err)
	}
	result, err = s.InvokeHook(ctx, HookInvocation{Installation: installation, Hook: hook, Trigger: "agent"}, 1)
	if err == nil || result.Error != "flag_off" {
		t.Fatalf("stale installation call=%+v %v", result, err)
	}
}
