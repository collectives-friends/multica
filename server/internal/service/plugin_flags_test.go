package service

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/multica-ai/multica/server/internal/featureflags"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
	"github.com/multica-ai/multica/server/pkg/featureflag"
	"github.com/multica-ai/multica/server/pkg/plugincontract"
)

type pluginFlagSource struct {
	workspace   map[string]featureflags.Override
	member      map[string]bool
	definitions []featureflags.Definition
	err         error
}

func (s pluginFlagSource) LoadWorkspace(context.Context, string) (map[string]featureflags.Override, []featureflags.Definition, error) {
	return s.workspace, s.definitions, s.err
}
func (s pluginFlagSource) LoadMember(context.Context, string, string) (map[string]bool, error) {
	return s.member, nil
}

func TestPluginSkillResolutionPreservesLookupErrors(t *testing.T) {
	lookupErr := errors.New("installation lookup unavailable")
	skill := db.Skill{PluginInstallationID: testInstallationID(t)}
	s := &PluginService{Queries: db.New(scanErrDBTX{err: lookupErr})}
	if active, err := s.ResolveSkillActive(context.Background(), skill); active || !errors.Is(err, lookupErr) {
		t.Fatalf("lookup error must stop skill resolution: active=%v err=%v", active, err)
	}
	s.Queries = db.New(scanErrDBTX{err: pgx.ErrNoRows})
	if active, err := s.ResolveSkillActive(context.Background(), skill); active || err != nil {
		t.Fatalf("removed installation: active=%v err=%v", active, err)
	}
	if active, err := s.ResolveSkillActive(context.Background(), db.Skill{}); !active || err != nil {
		t.Fatalf("user-owned skill: active=%v err=%v", active, err)
	}
}
func flagTestPlugin(t *testing.T) db.PluginInstallation {
	t.Helper()
	raw := []byte(`{"manifest_version":2,"key":"com.example.flags","name":"Flags","version":"1.0.0","author":{"name":"Test"},"scopes":["issues:read"],"contributes":{"flags":[{"key":"review","label":"Review","default":true,"scope_policy":"member_overridable"}],"surfaces":[{"key":"review","name":"Review","type":"issue_panel","entry":"ui.js","requires_flag":"review"}]}}`)
	return db.PluginInstallation{ID: testInstallationID(t), WorkspaceID: testInstallationID(t), PluginKey: "com.example.flags", Enabled: true, Manifest: raw}
}
func TestContributionActiveUsesActorAndInstallation(t *testing.T) {
	installation := flagTestPlugin(t)
	key := "plugin:com.example.flags:review"
	source := pluginFlagSource{workspace: map[string]featureflags.Override{featureflags.PluginsV1: {Enabled: true}, key: {Enabled: true}}, member: map[string]bool{key: false}, definitions: []featureflags.Definition{{Key: key, Default: true, ScopePolicy: featureflags.MemberOverridable, Parent: featureflags.PluginsV1}}}
	s := &PluginService{FlagResolver: featureflags.NewResolver(featureflags.HostRegistry(), nil, source)}
	if !s.ContributionActive(context.Background(), installation, "review", "") {
		t.Fatal("workspace gate used a member decision")
	}
	if s.ContributionActive(context.Background(), installation, "review", "member") {
		t.Fatal("member gate ignored own preference")
	}
	if s.ContributionActive(context.Background(), installation, "missing", "") {
		t.Fatal("unknown gate enabled")
	}
	installation.Enabled = false
	if s.ContributionActive(context.Background(), installation, "", "") {
		t.Fatal("disabled installation enabled")
	}
	installation.Enabled = true
	provider := featureflag.NewStaticProvider()
	provider.Set(featureflags.PluginsV1, featureflag.Rule{Default: false})
	s.FlagResolver = featureflags.NewResolver(featureflags.HostRegistry(), featureflag.NewService(provider), source)
	if s.ContributionActive(context.Background(), installation, "review", "") {
		t.Fatal("plugins deployment gate bypassed")
	}
}
func TestContributionFlagsDoNotExposeOtherPluginsOrPrivateHost(t *testing.T) {
	installation := flagTestPlugin(t)
	key := "plugin:com.example.flags:review"
	s := &PluginService{FlagResolver: featureflags.NewResolver(featureflags.HostRegistry(), nil, pluginFlagSource{workspace: map[string]featureflags.Override{featureflags.PluginsV1: {Enabled: true}}, definitions: []featureflags.Definition{{Key: key, Default: true, ScopePolicy: featureflags.MemberOverridable}, {Key: "plugin:com.other.plugin:secret", Default: true}}})}
	flags := s.ContributionFlags(context.Background(), installation, "")
	if !flags["review"] || !flags["host:plugins_v1"] {
		t.Fatalf("missing values: %v", flags)
	}
	if _, ok := flags["host:composio_mcp_apps"]; ok {
		t.Fatal("unreferenced public host flag leaked")
	}
	if _, ok := flags["host:local_search_index"]; ok {
		t.Fatal("private host flag leaked")
	}
	if _, ok := flags["plugin:com.other.plugin:secret"]; ok {
		t.Fatal("other plugin flag leaked")
	}
}
func TestManifestReadbackRejectsNonPublicHostFlags(t *testing.T) {
	installation := flagTestPlugin(t)
	var manifest plugincontract.Manifest
	if err := json.Unmarshal(installation.Manifest, &manifest); err != nil {
		t.Fatal(err)
	}
	manifest.Contributes.Surfaces[0].RequiresFlag = "host:local_search_index"
	installation.Manifest, _ = json.Marshal(manifest)
	if _, err := ParseInstallationManifest(installation); err == nil {
		t.Fatal("private host ref accepted at readback")
	}
	if _, _, err := ManifestForVersion(db.PluginPackageVersion{Manifest: installation.Manifest}); err == nil {
		t.Fatal("private host ref accepted at preview/install")
	}
}
func TestFlagOffHookRefusedBeforeNetwork(t *testing.T) {
	installation := flagTestPlugin(t)
	s := &PluginService{FlagResolver: featureflags.NewResolver(featureflags.HostRegistry(), nil, nil)}
	result, err := s.InvokeHook(context.Background(), HookInvocation{Installation: installation, Hook: plugincontract.Hook{Key: "review", RequiresFlag: "review", Triggers: []string{"event"}, Transport: plugincontract.HookTransport{Type: "http", URL: "https://example.com"}}, Trigger: "event", Actor: HookActor{Type: "plugin"}}, 1)
	if err == nil || result.Status != "refused" || result.Error != "flag_off" {
		t.Fatalf("got %+v %v", result, err)
	}
}

func TestFlagOffMCPDiscoveryAndCredentialStopBeforeNetwork(t *testing.T) {
	installation := mcpInstallation(t, `{}`)
	var m plugincontract.Manifest
	if err := json.Unmarshal(installation.Manifest, &m); err != nil {
		t.Fatal(err)
	}
	m.ManifestVersion = 2
	m.Contributes.Hooks[0].RequiresFlag = "host:composio_mcp_apps"
	installation.Manifest, _ = json.Marshal(m)
	resolver := featureflags.NewResolver(featureflags.HostRegistry(), nil, pluginFlagSource{workspace: map[string]featureflags.Override{featureflags.PluginsV1: {Enabled: true}}})
	s := &PluginService{FlagResolver: resolver}
	if _, err := s.DiscoverMCPHookTools(context.Background(), installation, "toolbox"); err == nil || pluginErrKind(t, err) != PluginErrorContributionDisabled {
		t.Fatalf("discovery=%v", err)
	}
	if _, _, err := s.MCPHookCredential(context.Background(), installation, "toolbox"); err == nil || pluginErrKind(t, err) != PluginErrorContributionDisabled {
		t.Fatalf("credential=%v", err)
	}
}

func TestHookFlagsFollowActorResolution(t *testing.T) {
	installation := flagTestPlugin(t)
	key := PluginFlagKey(installation.PluginKey, "review")
	s := &PluginService{FlagResolver: featureflags.NewResolver(featureflags.HostRegistry(), nil, pluginFlagSource{workspace: map[string]featureflags.Override{featureflags.PluginsV1: {Enabled: true}}, member: map[string]bool{key: false}, definitions: []featureflags.Definition{{Key: key, Default: true, ScopePolicy: featureflags.MemberOverridable}}})}
	for _, trigger := range []string{"manual", "ui", "event", "schedule", "agent"} {
		body, err := s.buildHookBody(context.Background(), HookInvocation{Installation: installation, Trigger: trigger, Actor: HookActor{Type: "member", ID: installation.ID}})
		if err != nil {
			t.Fatal(err)
		}
		want := trigger != "manual" && trigger != "ui"
		if body.Flags["review"] != want {
			t.Fatalf("%s flags=%v", trigger, body.Flags)
		}
	}
}
