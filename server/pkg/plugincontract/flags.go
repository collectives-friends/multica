package plugincontract

import (
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
)

const (
	MaxFlags                   = 32
	FlagScopeWorkspaceOnly     = "workspace_only"
	FlagScopeMemberOverridable = "member_overridable"
)

var flagKeyPattern = regexp.MustCompile(`^[a-z][a-z0-9_]{1,63}$`)

// FlagDefinition declares an installation-owned flag. Parent and RequiresFlag
// reference an own key or host:<public key>; plugins cannot reference each other.
type FlagDefinition struct {
	Key         string `json:"key"`
	Label       string `json:"label"`
	Description string `json:"description,omitempty"`
	Default     bool   `json:"default"`
	ScopePolicy string `json:"scope_policy"`
	Parent      string `json:"parent,omitempty"`
}

// version2Field rejects presence, including null, empty strings and empty lists.
// A separate presence pass preserves the v1 wire contract without duplicating
// the shared schema. The regular strict decoder still rejects unknown fields.
type version2Field struct{}

func (version2Field) UnmarshalJSON([]byte) error {
	return fmt.Errorf("flags and requires_flag require manifest_version 2")
}

func rejectV2Fields(raw []byte) error {
	type gate struct {
		RequiresFlag version2Field `json:"requires_flag"`
	}
	var fields struct {
		Contributes struct {
			Flags     version2Field `json:"flags"`
			Surfaces  []gate        `json:"surfaces"`
			Hooks     []gate        `json:"hooks"`
			Resources []gate        `json:"resources"`
		} `json:"contributes"`
	}
	return json.Unmarshal(raw, &fields)
}

func (m Manifest) usesFlags() bool {
	if len(m.Contributes.Flags) > 0 {
		return true
	}
	for _, s := range m.Contributes.Surfaces {
		if s.RequiresFlag != "" {
			return true
		}
	}
	for _, h := range m.Contributes.Hooks {
		if h.RequiresFlag != "" {
			return true
		}
	}
	for _, r := range m.Contributes.Resources {
		if r.RequiresFlag != "" {
			return true
		}
	}
	return false
}

// ValidateFlagReferences checks declarations, own references, parent cycles and
// public host membership. Keys in publicHostKeys have no "host:" prefix; only
// true entries grant visibility. A nil/empty map permits no host references.
// ParseManifest and Validate check host reference syntax only, so hosts must call
// this validator with their registry at install, preview and snapshot readback.
func (m Manifest) ValidateFlagReferences(publicHostKeys map[string]bool) error {
	return m.validateFlags(func(key string) bool { return publicHostKeys[key] })
}

func (m Manifest) validateFlags(isPublicHost func(string) bool) error {
	if m.ManifestVersion != ManifestVersion1 && m.ManifestVersion != ManifestVersion2 {
		return fmt.Errorf("unsupported manifest_version %d (supported: 1, 2)", m.ManifestVersion)
	}
	if m.ManifestVersion == ManifestVersion1 && (m.Contributes.Flags != nil || m.usesFlags()) {
		return fmt.Errorf("flags and requires_flag require manifest_version 2")
	}
	if len(m.Contributes.Flags) > MaxFlags {
		return fmt.Errorf("contributes.flags must not exceed %d entries", MaxFlags)
	}
	own := map[string]FlagDefinition{}
	for i, f := range m.Contributes.Flags {
		field := fmt.Sprintf("contributes.flags[%d]", i)
		if !flagKeyPattern.MatchString(f.Key) {
			return fmt.Errorf("%s.key is invalid", field)
		}
		if len("plugin:"+m.Key+":"+f.Key) > 255 {
			return fmt.Errorf("%s.key namespaced with plugin key exceeds 255 bytes", field)
		}
		if _, ok := own[f.Key]; ok {
			return fmt.Errorf("duplicate flag key %q", f.Key)
		}
		own[f.Key] = f
		if err := validateDisplayText(field+".label", f.Label, 160); err != nil {
			return err
		}
		if len(f.Description) > 2000 || strings.ContainsRune(f.Description, '\r') {
			return fmt.Errorf("%s.description must be at most 2000 bytes without carriage returns", field)
		}
		if f.ScopePolicy != FlagScopeWorkspaceOnly && f.ScopePolicy != FlagScopeMemberOverridable {
			return fmt.Errorf("%s.scope_policy is unsupported: %q", field, f.ScopePolicy)
		}
	}
	reference := func(field, ref string) error {
		if ref == "" {
			return nil
		}
		if key, ok := strings.CutPrefix(ref, "host:"); ok {
			if !flagKeyPattern.MatchString(key) {
				return fmt.Errorf("%s has invalid host flag reference %q", field, ref)
			}
			if isPublicHost != nil && !isPublicHost(key) {
				return fmt.Errorf("%s references a non-public or unknown host flag %q", field, ref)
			}
			return nil
		}
		if _, ok := own[ref]; !ok {
			return fmt.Errorf("%s references unknown own flag %q", field, ref)
		}
		return nil
	}
	for _, f := range m.Contributes.Flags {
		if err := reference("contributes.flags."+f.Key+".parent", f.Parent); err != nil {
			return err
		}
	}
	// Each node has at most one parent. All own parents have already resolved.
	state := map[string]int{}
	var visit func(string) error
	visit = func(key string) error {
		if state[key] == 1 {
			return fmt.Errorf("contributes.flags parent cycle at %q", key)
		}
		if state[key] == 2 {
			return nil
		}
		state[key] = 1
		parent := own[key].Parent
		if _, ok := own[parent]; ok {
			if err := visit(parent); err != nil {
				return err
			}
		}
		state[key] = 2
		return nil
	}
	for _, f := range m.Contributes.Flags {
		if err := visit(f.Key); err != nil {
			return err
		}
	}
	for i, s := range m.Contributes.Surfaces {
		if err := reference(fmt.Sprintf("contributes.surfaces[%d].requires_flag", i), s.RequiresFlag); err != nil {
			return err
		}
	}
	for i, h := range m.Contributes.Hooks {
		if err := reference(fmt.Sprintf("contributes.hooks[%d].requires_flag", i), h.RequiresFlag); err != nil {
			return err
		}
	}
	for i, r := range m.Contributes.Resources {
		if err := reference(fmt.Sprintf("contributes.resources[%d].requires_flag", i), r.RequiresFlag); err != nil {
			return err
		}
	}
	return nil
}
