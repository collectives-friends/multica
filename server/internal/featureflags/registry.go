package featureflags

import (
	"fmt"
	"sort"
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

func HostRegistry() *Registry {
	yes := true
	r, err := NewRegistry([]Definition{
		{Key: BillingWorkspaceSubscriptions, Label: "Workspace subscriptions", Description: "Workspace billing and subscription management.", Group: "Workspace", Public: true},
		{Key: ComposioMCPApps, Label: "Connected apps", Description: "Manage Composio apps and their agent access.", Group: "Integrations", Public: true},
		{Key: PluginsV1, Label: "Plugins", Description: "Install and run workspace plugins.", Group: "Integrations", Public: true},
		{Key: TriageV1, Label: "Triage", Description: "Reserved compatibility flag; this build has no active triage gate.", Group: "Compatibility"},
		{Key: LocalSearchIndex, Label: "Local search index", Description: "Allow clients to synchronize the local search index.", Group: "Search", Default: true},
		{Key: agentBuilderCompat, Label: "Agent builder", Group: "Compatibility", Default: true, Frozen: &yes, Public: true},
		{Key: agentSkillTogglesCompat, Label: "Agent skill toggles", Group: "Compatibility", Default: true, Frozen: &yes, Public: true},
		{Key: resourceLabelsCompat, Label: "Resource labels", Group: "Compatibility", Default: true, Frozen: &yes, Public: true},
	})
	if err != nil {
		panic(err)
	}
	return r
}
