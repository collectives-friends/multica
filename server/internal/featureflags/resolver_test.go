package featureflags

import (
	"context"
	"errors"
	"testing"

	"github.com/multica-ai/multica/server/pkg/featureflag"
)

type memorySource struct {
	workspace   map[string]Override
	member      map[string]bool
	definitions []Definition
	calls       int
	err         error
}

func (s *memorySource) LoadWorkspace(context.Context, string) (map[string]Override, []Definition, error) {
	s.calls++
	return s.workspace, s.definitions, s.err
}
func (s *memorySource) LoadMember(context.Context, string, string) (map[string]bool, error) {
	s.calls++
	return s.member, s.err
}

func TestResolutionOrder(t *testing.T) {
	yes := true
	cases := []struct {
		name      string
		def       Definition
		workspace map[string]Override
		member    map[string]bool
		rule      *featureflag.Rule
		parent    *Definition
		key       string
		fallback  bool
		want      Decision
	}{
		{name: "default", def: Definition{Default: true}, want: Decision{true, "default", false}},
		{name: "workspace", workspace: map[string]Override{"flag": {Enabled: true}}, want: Decision{true, "workspace", false}},
		{name: "member over workspace", workspace: map[string]Override{"flag": {Enabled: false}}, member: map[string]bool{"flag": true}, want: Decision{true, "member", false}},
		{name: "locked over member", workspace: map[string]Override{"flag": {Enabled: false, Locked: true}}, member: map[string]bool{"flag": true}, want: Decision{false, "workspace_locked", true}},
		{name: "workspace only", def: Definition{ScopePolicy: WorkspaceOnly}, member: map[string]bool{"flag": true}, want: Decision{false, "default", false}},
		{name: "deployment", rule: &featureflag.Rule{Default: false}, workspace: map[string]Override{"flag": {Enabled: true, Locked: true}}, want: Decision{false, "deployment", true}},
		{name: "target rule", rule: &featureflag.Rule{Allow: []string{"w"}, AllowBy: "workspace_id"}, want: Decision{true, "rule", false}},
		{name: "member over target rule", rule: &featureflag.Rule{Allow: []string{"w"}, AllowBy: "workspace_id"}, member: map[string]bool{"flag": false}, want: Decision{false, "member", false}},
		{name: "parent over deployment", def: Definition{Parent: "parent"}, parent: &Definition{Key: "parent", ScopePolicy: WorkspaceOnly}, rule: &featureflag.Rule{Default: true}, want: Decision{false, "parent:parent", true}},
		{name: "frozen", def: Definition{Frozen: &yes}, workspace: map[string]Override{"flag": {Enabled: false, Locked: true}}, want: Decision{true, "frozen", true}},
		{name: "unknown", key: "unknown", fallback: true, want: Decision{true, "default", false}},
		{name: "disabled plugin unknown", key: "plugin:example:beta", want: Decision{false, "default", false}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			tc.def.Key = "flag"
			if tc.def.ScopePolicy == "" {
				tc.def.ScopePolicy = MemberOverridable
			}
			defs := []Definition{tc.def}
			if tc.parent != nil {
				defs = append(defs, *tc.parent)
			}
			registry, err := NewRegistry(defs)
			if err != nil {
				t.Fatal(err)
			}
			provider := featureflag.NewStaticProvider()
			if tc.rule != nil {
				provider.Set("flag", *tc.rule)
			}
			source := &memorySource{workspace: tc.workspace, member: tc.member}
			resolver := NewResolver(registry, featureflag.NewService(provider), source)
			key := tc.key
			if key == "" {
				key = "flag"
			}
			got := resolver.Explain(context.Background(), "w", "u", key, tc.fallback)
			if got != tc.want {
				t.Fatalf("got %+v want %+v", got, tc.want)
			}
		})
	}
}

func TestWorkspaceResolutionIgnoresMembersThroughParents(t *testing.T) {
	registry, err := NewRegistry([]Definition{{Key: "parent", Default: true, ScopePolicy: MemberOverridable}, {Key: "child", Default: true, Parent: "parent", ScopePolicy: WorkspaceOnly}})
	if err != nil {
		t.Fatal(err)
	}
	resolver := NewResolver(registry, nil, &memorySource{member: map[string]bool{"parent": false, "child": false}})
	if !resolver.EnabledFor(context.Background(), "w", "u", "child") {
		t.Fatal("workspace-only child must not depend on personal parent")
	}
	if !resolver.ResolveWorkspace(context.Background(), "w", "child").Value {
		t.Fatal("workspace resolution used member")
	}
	if resolver.EnabledFor(context.Background(), "w", "u", "parent") {
		t.Fatal("personal parent override ignored")
	}
}

func TestRegistryRejectsCyclesUnknownParentsAndFrozenParent(t *testing.T) {
	yes := true
	for _, defs := range [][]Definition{
		{{Key: "a", Parent: "b"}, {Key: "b", Parent: "a"}},
		{{Key: "a", Parent: "missing"}},
		{{Key: "a", Frozen: &yes, Parent: "b"}, {Key: "b"}},
		{{Key: "a"}, {Key: "a"}},
	} {
		if _, err := NewRegistry(defs); err == nil {
			t.Fatalf("accepted invalid registry: %+v", defs)
		}
	}
}

func TestRequestStateAndCacheInvalidation(t *testing.T) {
	registry, _ := NewRegistry([]Definition{{Key: "flag", ScopePolicy: MemberOverridable}})
	source := &memorySource{workspace: map[string]Override{"flag": {Enabled: true}}}
	resolver := NewResolver(registry, nil, source)
	ctx, err := resolver.Prepare(context.Background(), "w", "u")
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 5; i++ {
		if !resolver.Enabled(ctx, "flag") {
			t.Fatal("missing prepared state")
		}
	}
	if source.calls != 2 {
		t.Fatalf("want two loads, got %d", source.calls)
	}
	resolver.EnabledFor(context.Background(), "w", "u", "flag")
	calls := source.calls
	resolver.EnabledFor(context.Background(), "w", "u", "flag")
	if calls != source.calls {
		t.Fatal("cache missed")
	}
	source.workspace = map[string]Override{"flag": {Enabled: false}}
	resolver.Invalidate("w")
	if resolver.EnabledFor(context.Background(), "w", "u", "flag") {
		t.Fatal("stale after invalidation")
	}
}

func TestSourceFailureFailsClosed(t *testing.T) {
	registry, _ := NewRegistry([]Definition{{Key: "flag", Default: true}})
	resolver := NewResolver(registry, nil, &memorySource{err: errors.New("offline")})
	if resolver.EnabledFor(context.Background(), "w", "u", "flag") {
		t.Fatal("database failure enabled flag")
	}
	if _, err := resolver.Prepare(context.Background(), "w", "u"); err == nil {
		t.Fatal("API must observe source failure")
	}
}

func TestFrozenHostDoesNotDependOnDatabase(t *testing.T) {
	yes := true
	registry, _ := NewRegistry([]Definition{{Key: "compat", Frozen: &yes}})
	resolver := NewResolver(registry, nil, &memorySource{err: errors.New("offline")})
	if got := resolver.Explain(context.Background(), "w", "u", "compat"); got != (Decision{true, "frozen", true}) {
		t.Fatalf("frozen host value changed on database failure: %+v", got)
	}
}
