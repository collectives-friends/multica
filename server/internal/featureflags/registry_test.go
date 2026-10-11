package featureflags

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"testing"

	"github.com/multica-ai/multica/server/pkg/featureflag"
)

func TestParseDefinitions(t *testing.T) {
	for _, raw := range []string{"", "  \n", "[]"} {
		defs, err := ParseDefinitions(raw)
		if err != nil || len(defs) != 0 {
			t.Fatalf("%q: want no definitions, got %+v, %v", raw, defs, err)
		}
	}
	defs, err := ParseDefinitions(`[
		{"key":"acme.reports","label":"Reports","description":"Monthly reports.","group":"Acme","default":true,"scope_policy":"member_overridable"},
		{"key":"acme-beta_2","label":"Beta"}
	]`)
	if err != nil {
		t.Fatal(err)
	}
	want := []Definition{
		{Key: "acme.reports", Label: "Reports", Description: "Monthly reports.", Group: "Acme", Default: true, ScopePolicy: MemberOverridable},
		{Key: "acme-beta_2", Label: "Beta", Group: "Other", ScopePolicy: WorkspaceOnly},
	}
	if fmt.Sprint(defs) != fmt.Sprint(want) {
		t.Fatalf("got %+v, want %+v", defs, want)
	}
}

func TestParseDefinitionsRejects(t *testing.T) {
	many := make([]string, 65)
	for i := range many {
		many[i] = fmt.Sprintf(`{"key":"flag_%d","label":"Flag"}`, i)
	}
	cases := map[string]string{
		"not an array":      `{"key":"a","label":"A"}`,
		"malformed":         `[{"key":"a",`,
		"trailing data":     `[] []`,
		"empty key":         `[{"key":"","label":"A"}]`,
		"uppercase key":     `[{"key":"Acme","label":"A"}]`,
		"leading dot":       `[{"key":".acme","label":"A"}]`,
		"plugin namespace":  `[{"key":"plugin:x:y","label":"A"}]`,
		"key too long":      `[{"key":"` + strings.Repeat("a", 101) + `","label":"A"}]`,
		"host key":          `[{"key":"plugins_v1","label":"A"}]`,
		"frozen host key":   `[{"key":"agents_agent_builder","label":"A"}]`,
		"duplicate":         `[{"key":"a","label":"A"},{"key":"a","label":"B"}]`,
		"scope policy":      `[{"key":"a","label":"A","scope_policy":"member_only"}]`,
		"missing label":     `[{"key":"a"}]`,
		"blank label":       `[{"key":"a","label":"  "}]`,
		"parent":            `[{"key":"a","label":"A","parent":"plugins_v1"}]`,
		"frozen":            `[{"key":"a","label":"A","frozen":true}]`,
		"public":            `[{"key":"a","label":"A","public":true}]`,
		"plugin install id": `[{"key":"a","label":"A","plugin_installation_id":"x"}]`,
		"unknown field":     `[{"key":"a","label":"A","owner":"ops"}]`,
		"too many":          "[" + strings.Join(many, ",") + "]",
		"env alias of host": `[{"key":"plugins.v1","label":"A"}]`,
		"env alias pair":    `[{"key":"a.b","label":"A"},{"key":"a_b","label":"B"}]`,
		"reserved route":    `[{"key":"audit","label":"A"}]`,
	}
	for name, raw := range cases {
		t.Run(name, func(t *testing.T) {
			if defs, err := ParseDefinitions(raw); err == nil {
				t.Fatalf("accepted %+v", defs)
			}
		})
	}
	if _, err := ParseDefinitions("[" + strings.Join(many[:64], ",") + "]"); err != nil {
		t.Fatalf("64 definitions must be accepted: %v", err)
	}
	if _, err := ParseDefinitions(`[{"key":"a` + strings.Repeat("b", 99) + `","label":"A"}]`); err != nil {
		t.Fatalf("100-character key must be accepted: %v", err)
	}
}

func TestHostRegistryWith(t *testing.T) {
	r, err := HostRegistryWith([]Definition{{Key: "acme.reports", Label: "Reports", Group: "Acme", ScopePolicy: WorkspaceOnly}})
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := r.Definition(PluginsV1); !ok {
		t.Fatal("built-in host definitions missing")
	}
	if d, ok := r.Definition("acme.reports"); !ok || d.Label != "Reports" {
		t.Fatalf("deployment definition missing: %+v", d)
	}
	if r.PublicKeys()["acme.reports"] {
		t.Fatal("deployment definitions must not become public host keys")
	}
	if _, err := HostRegistryWith([]Definition{{Key: PluginsV1, Label: "Shadow"}}); err == nil {
		t.Fatal("collision with a built-in host key accepted")
	}
}

func TestDeploymentDefinitionResolution(t *testing.T) {
	r, err := HostRegistryWith([]Definition{{Key: "acme.reports", Label: "Reports", ScopePolicy: WorkspaceOnly}})
	if err != nil {
		t.Fatal(err)
	}
	source := &memorySource{}
	resolver := NewResolver(r, nil, source)
	if d := resolver.ResolveWorkspace(context.Background(), "w", "acme.reports"); d != (Decision{false, "default", false}) {
		t.Fatalf("default: %+v", d)
	}
	source.workspace = map[string]Override{"acme.reports": {Enabled: true}}
	resolver.Invalidate("w")
	if d := resolver.ResolveWorkspace(context.Background(), "w", "acme.reports"); d != (Decision{true, "workspace", false}) {
		t.Fatalf("workspace override: %+v", d)
	}
	// Deployment configuration addresses declared keys like any host key.
	t.Setenv("FF_ACME_REPORTS", "off")
	resolver.Deployment = featureflag.NewService(featureflag.NewEnvProvider(featureflag.EnvOverridePrefix))
	if d := resolver.ResolveWorkspace(context.Background(), "w", "acme.reports"); d != (Decision{false, "deployment", true}) {
		t.Fatalf("FF_ env override: %+v", d)
	}
	yaml := featureflag.NewStaticProvider()
	yaml.Set("acme.reports", featureflag.Rule{Default: true})
	resolver.Deployment = featureflag.NewService(yaml)
	if d := resolver.ResolveWorkspace(context.Background(), "w", "acme.reports"); d != (Decision{true, "deployment", true}) {
		t.Fatalf("YAML override: %+v", d)
	}
}

// The environment is read once per process, so each case runs in a fresh
// child process instead of mutating this process's cached registry.
func TestHostRegistryReadsDeploymentDefinitionsAtStartup(t *testing.T) {
	if os.Getenv("FEATUREFLAGS_STARTUP_CHILD") == "1" {
		if _, err := LoadHostRegistry(); err != nil {
			fmt.Print("error: ", err)
			os.Exit(3)
		}
		fmt.Print("keys:")
		for _, d := range HostRegistry().Definitions() {
			fmt.Print(" ", d.Key)
		}
		os.Exit(0)
	}
	run := func(value string, set bool) (string, int) {
		cmd := exec.Command(os.Args[0], "-test.run=^TestHostRegistryReadsDeploymentDefinitionsAtStartup$")
		cmd.Env = []string{"FEATUREFLAGS_STARTUP_CHILD=1"}
		for _, kv := range os.Environ() {
			if !strings.HasPrefix(kv, DefinitionsEnv+"=") {
				cmd.Env = append(cmd.Env, kv)
			}
		}
		if set {
			cmd.Env = append(cmd.Env, DefinitionsEnv+"="+value)
		}
		out, err := cmd.Output()
		if exit, ok := err.(*exec.ExitError); ok {
			return string(out), exit.ExitCode()
		} else if err != nil {
			t.Fatal(err)
		}
		return string(out), 0
	}
	out, code := run(`[{"key":"acme.reports","label":"Reports"}]`, true)
	if code != 0 || !strings.Contains(out, " acme.reports") || !strings.Contains(out, " "+PluginsV1) {
		t.Fatalf("valid definitions: exit %d, %s", code, out)
	}
	out, code = run(`[{"key":"plugins_v1","label":"Shadow"}]`, true)
	if code != 3 || !strings.Contains(out, DefinitionsEnv) {
		t.Fatalf("invalid definitions must fail startup with the variable name: exit %d, %s", code, out)
	}
	for _, set := range []bool{false, true} {
		out, code = run("", set)
		if code != 0 || strings.Contains(out, "acme") {
			t.Fatalf("absent/empty variable (set=%v): exit %d, %s", set, code, out)
		}
	}
}
