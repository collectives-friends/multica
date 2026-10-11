package featureflags

import (
	"encoding/json"
	"errors"
	"fmt"
	"github.com/multica-ai/multica/server/pkg/featureflag"
	"io"
	"os"
	"regexp"
	"sort"
	"strings"
	"sync"
)

const (
	WorkspaceOnly     = "workspace_only"
	MemberOverridable = "member_overridable"
)

// Definition belongs to the host registry or one installed plugin snapshot.
type Definition struct {
	Key                  string `json:"key"`
	Label                string `json:"label"`
	Description          string `json:"description"`
	Group                string `json:"group"`
	Default              bool   `json:"default"`
	ScopePolicy          string `json:"scope_policy"`
	Parent               string `json:"parent,omitempty"`
	Public               bool   `json:"public"`
	Frozen               *bool  `json:"frozen,omitempty"`
	PluginInstallationID string `json:"plugin_installation_id,omitempty"`
	PluginName           string `json:"plugin_name,omitempty"`
}

type Registry struct{ definitions map[string]Definition }

func NewRegistry(definitions []Definition) (*Registry, error) {
	r := &Registry{definitions: make(map[string]Definition, len(definitions))}
	for _, d := range definitions {
		if d.Key == "" || len(d.Key) > 255 {
			return nil, fmt.Errorf("invalid flag key %q", d.Key)
		}
		if _, ok := r.definitions[d.Key]; ok {
			return nil, fmt.Errorf("duplicate flag %q", d.Key)
		}
		if d.ScopePolicy == "" {
			d.ScopePolicy = WorkspaceOnly
		}
		if d.ScopePolicy != WorkspaceOnly && d.ScopePolicy != MemberOverridable {
			return nil, fmt.Errorf("invalid scope policy for %q", d.Key)
		}
		if d.Frozen != nil && d.Parent != "" {
			return nil, fmt.Errorf("frozen flag %q cannot have a parent", d.Key)
		}
		r.definitions[d.Key] = d
	}
	seen := map[string]uint8{}
	var visit func(string) error
	visit = func(k string) error {
		if seen[k] == 1 {
			return fmt.Errorf("flag parent cycle at %q", k)
		}
		if seen[k] == 2 {
			return nil
		}
		seen[k] = 1
		d, ok := r.definitions[k]
		if !ok {
			return fmt.Errorf("unknown flag parent %q", k)
		}
		if d.Parent != "" {
			if err := visit(d.Parent); err != nil {
				return err
			}
		}
		seen[k] = 2
		return nil
	}
	for k := range r.definitions {
		if err := visit(k); err != nil {
			return nil, err
		}
	}
	return r, nil
}

func (r *Registry) Definitions() []Definition {
	out := make([]Definition, 0, len(r.definitions))
	for _, d := range r.definitions {
		out = append(out, d)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Key < out[j].Key })
	return out
}
func (r *Registry) Definition(key string) (Definition, bool) {
	d, ok := r.definitions[key]
	return d, ok
}
func (r *Registry) PublicKeys() map[string]bool {
	out := map[string]bool{}
	for k, d := range r.definitions {
		if d.Public {
			out[k] = true
		}
	}
	return out
}

// DefinitionsEnv names a JSON array of extra host definitions declared by the
// deployment. It is read once per process; main validates it before serving.
const DefinitionsEnv = "MULTICA_FEATURE_FLAG_DEFINITIONS"

const maxDeploymentDefinitions = 64

var deploymentKeyPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]{0,99}$`)

func builtinDefinitions() []Definition {
	yes := true
	return []Definition{
		{Key: BillingWorkspaceSubscriptions, Label: "Workspace subscriptions", Description: "Workspace billing and subscription management.", Group: "Workspace", Public: true},
		{Key: ComposioMCPApps, Label: "Connected apps", Description: "Manage Composio apps and their agent access.", Group: "Integrations", Public: true},
		{Key: ConnectionsV1, Label: "Connections", Description: "Maintain workspace connections to external systems and let plugins use them.", Group: "Integrations", Parent: PluginsV1, Public: true},
		{Key: PluginIntegrationsV1, Label: "Plugin integrations", Description: "Workspace and project integration surfaces and project API.", Group: "Integrations", Parent: PluginsV1, Public: true},
		{Key: PluginsV1, Label: "Plugins", Description: "Install and run workspace plugins.", Group: "Integrations", Public: true},
		{Key: TriageV1, Label: "Triage", Description: "Reserved compatibility flag; this build has no active triage gate.", Group: "Compatibility"},
		{Key: LocalSearchIndex, Label: "Local search index", Description: "Allow clients to synchronize the local search index.", Group: "Search", Default: true},
		{Key: agentBuilderCompat, Label: "Agent builder", Group: "Compatibility", Default: true, Frozen: &yes, Public: true},
		{Key: agentSkillTogglesCompat, Label: "Agent skill toggles", Group: "Compatibility", Default: true, Frozen: &yes, Public: true},
		{Key: resourceLabelsCompat, Label: "Resource labels", Group: "Compatibility", Default: true, Frozen: &yes, Public: true},
	}
}

// ParseDefinitions validates deployment-declared definitions. They are plain
// workspace flags: no parent, frozen value, public exposure or plugin owner.
// The key pattern excludes ':' so they never collide with plugin keys.
func ParseDefinitions(raw string) ([]Definition, error) {
	if strings.TrimSpace(raw) == "" {
		return nil, nil
	}
	var input []struct {
		Key         string `json:"key"`
		Label       string `json:"label"`
		Description string `json:"description"`
		Group       string `json:"group"`
		Default     bool   `json:"default"`
		ScopePolicy string `json:"scope_policy"`
	}
	decoder := json.NewDecoder(strings.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&input); err != nil {
		return nil, fmt.Errorf("decode: %w", err)
	}
	if _, err := decoder.Token(); !errors.Is(err, io.EOF) {
		return nil, errors.New("decode: unexpected data after the JSON array")
	}
	if len(input) > maxDeploymentDefinitions {
		return nil, fmt.Errorf("at most %d definitions are allowed, got %d", maxDeploymentDefinitions, len(input))
	}
	builtin := map[string]bool{}
	// FF_<KEY> deployment overrides normalize keys; an alias would share one override.
	envNames := map[string]string{}
	for _, d := range builtinDefinitions() {
		builtin[d.Key] = true
		envNames[featureflag.EnvName(d.Key)] = d.Key
	}
	seen := map[string]bool{}
	defs := make([]Definition, 0, len(input))
	for i, in := range input {
		switch {
		case !deploymentKeyPattern.MatchString(in.Key):
			return nil, fmt.Errorf("definition %d: key %q must match %s", i, in.Key, deploymentKeyPattern)
		case builtin[in.Key]:
			return nil, fmt.Errorf("definition %d: key %q is a built-in host flag", i, in.Key)
		case seen[in.Key]:
			return nil, fmt.Errorf("definition %d: duplicate key %q", i, in.Key)
		case in.Key == "audit":
			return nil, fmt.Errorf("definition %d: key %q is reserved by the feature-flag API", i, in.Key)
		case envNames[featureflag.EnvName(in.Key)] != "":
			return nil, fmt.Errorf("definition %d: key %q shares deployment override FF_%s with %q", i, in.Key, featureflag.EnvName(in.Key), envNames[featureflag.EnvName(in.Key)])
		case strings.TrimSpace(in.Label) == "":
			return nil, fmt.Errorf("definition %d: label is required for %q", i, in.Key)
		}
		seen[in.Key] = true
		envNames[featureflag.EnvName(in.Key)] = in.Key
		if in.ScopePolicy == "" {
			in.ScopePolicy = WorkspaceOnly
		}
		if in.ScopePolicy != WorkspaceOnly && in.ScopePolicy != MemberOverridable {
			return nil, fmt.Errorf("definition %d: scope_policy for %q must be %s or %s", i, in.Key, WorkspaceOnly, MemberOverridable)
		}
		if in.Group == "" {
			in.Group = "Other"
		}
		defs = append(defs, Definition{Key: in.Key, Label: in.Label, Description: in.Description, Group: in.Group, Default: in.Default, ScopePolicy: in.ScopePolicy})
	}
	return defs, nil
}

// HostRegistryWith returns the built-in host registry plus extra definitions.
func HostRegistryWith(extra []Definition) (*Registry, error) {
	return NewRegistry(append(builtinDefinitions(), extra...))
}

var hostRegistry = sync.OnceValues(func() (*Registry, error) {
	extra, err := ParseDefinitions(os.Getenv(DefinitionsEnv))
	if err != nil {
		return nil, fmt.Errorf("%s: %w", DefinitionsEnv, err)
	}
	return HostRegistryWith(extra)
})

// LoadHostRegistry reports invalid deployment definitions so startup can fail
// before the first request reaches HostRegistry.
func LoadHostRegistry() (*Registry, error) { return hostRegistry() }

// HostRegistry is immutable and shared; callers copy Definitions before
// extending it.
func HostRegistry() *Registry {
	r, err := hostRegistry()
	if err != nil {
		panic(err)
	}
	return r
}
