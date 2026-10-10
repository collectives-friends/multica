package plugincontract

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
)

func flagDocument(t *testing.T, edit func(map[string]any)) []byte {
	t.Helper()
	return mutate(t, func(doc map[string]any) {
		doc["manifest_version"] = 2
		doc["contributes"].(map[string]any)["flags"] = []any{map[string]any{
			"key": "review_enabled", "label": "Review", "description": "Enable reviews", "default": true, "scope_policy": "member_overridable",
		}}
		if edit != nil {
			edit(doc)
		}
	})
}

func TestManifestV2FlagRoundTrip(t *testing.T) {
	raw := flagDocument(t, func(doc map[string]any) {
		c := doc["contributes"].(map[string]any)
		for _, kind := range []string{"surfaces", "hooks", "resources"} {
			c[kind].([]any)[0].(map[string]any)["requires_flag"] = "review_enabled"
		}
	})
	manifest, canonical, err := ParseManifest(raw)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := ParseManifest(canonical); err != nil {
		t.Fatalf("canonical roundtrip: %v", err)
	}
	var doc map[string]any
	if err := json.Unmarshal(canonical, &doc); err != nil {
		t.Fatal(err)
	}
	c := doc["contributes"].(map[string]any)
	if len(c["flags"].([]any)) != 1 {
		t.Fatal("flag lost")
	}
	for _, kind := range []string{"surfaces", "hooks", "resources"} {
		if c[kind].([]any)[0].(map[string]any)["requires_flag"] != "review_enabled" {
			t.Fatalf("%s gate lost", kind)
		}
	}
	if err := manifest.CheckCapabilities(HostCapabilities()); err != nil {
		t.Fatal(err)
	}
	if err := manifest.CheckCapabilities(Capabilities{}); err == nil || !strings.Contains(err.Error(), "flags") {
		t.Fatalf("flags capability missing: %v", err)
	}
}

func TestManifestV1RejectsV2FieldsEvenEmpty(t *testing.T) {
	for _, value := range []any{nil, "", []any{}} {
		for _, kind := range []string{"flags", "surfaces", "hooks", "resources"} {
			t.Run(fmt.Sprintf("%s/%v", kind, value), func(t *testing.T) {
				raw := mutate(t, func(doc map[string]any) {
					c := doc["contributes"].(map[string]any)
					if kind == "flags" {
						c["flags"] = value
					} else {
						c[kind].([]any)[0].(map[string]any)["requires_flag"] = value
					}
				})
				if _, _, err := ParseManifest(raw); err == nil || !strings.Contains(err.Error(), "require manifest_version 2") {
					t.Fatalf("v1 accepted v2 field or unclear error: %v", err)
				}
			})
		}
	}
}

func TestManifestVersionDispatch(t *testing.T) {
	for _, version := range []int{0, 3, -1, 99} {
		raw := mutate(t, func(doc map[string]any) { doc["manifest_version"] = version; doc["future_field"] = true })
		if _, _, err := ParseManifest(raw); err == nil || !strings.Contains(err.Error(), "unsupported manifest_version") {
			t.Fatalf("version %d: %v", version, err)
		}
	}
	for _, version := range []int{1, 2} {
		raw := mutate(t, func(doc map[string]any) { doc["manifest_version"] = version })
		if _, _, err := ParseManifest(raw); err != nil {
			t.Fatal(err)
		}
	}
}

func TestManifestV2RejectsInvalidFlags(t *testing.T) {
	cases := []struct {
		name string
		edit func(map[string]any)
		want string
	}{
		{"short", func(f map[string]any) { f["key"] = "a" }, "key"},
		{"hyphen", func(f map[string]any) { f["key"] = "review-enabled" }, "key"},
		{"uppercase", func(f map[string]any) { f["key"] = "Review" }, "key"},
		{"namespace", func(f map[string]any) { f["key"] = "host:reviews" }, "key"},
		{"long", func(f map[string]any) { f["key"] = strings.Repeat("a", 65) }, "key"},
		{"scope", func(f map[string]any) { f["scope_policy"] = "global" }, "scope_policy"},
		{"label", func(f map[string]any) { f["label"] = " " }, "label"},
		{"missing parent", func(f map[string]any) { f["parent"] = "missing" }, "parent"},
		{"foreign parent", func(f map[string]any) { f["parent"] = "other:review" }, "parent"},
		{"self cycle", func(f map[string]any) { f["parent"] = "review_enabled" }, "cycle"},
		{"unknown field", func(f map[string]any) { f["surprise"] = true }, "unknown field"},
		{"nonbool", func(f map[string]any) { f["default"] = "true" }, "bool"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			raw := flagDocument(t, func(doc map[string]any) {
				tc.edit(doc["contributes"].(map[string]any)["flags"].([]any)[0].(map[string]any))
			})
			if _, _, err := ParseManifest(raw); err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("got %v, want %s", err, tc.want)
			}
		})
	}
	for _, n := range []int{32, 33} {
		raw := flagDocument(t, func(doc map[string]any) {
			fs := []any{}
			for i := 0; i < n; i++ {
				fs = append(fs, map[string]any{"key": fmt.Sprintf("flag_%d", i), "label": "Flag", "default": false, "scope_policy": "workspace_only"})
			}
			doc["contributes"].(map[string]any)["flags"] = fs
		})
		_, _, err := ParseManifest(raw)
		if (err != nil) != (n > 32) || (err != nil && !strings.Contains(err.Error(), "must not exceed 32")) {
			t.Fatalf("count %d: %v", n, err)
		}
	}
	for parent, want := range map[string]string{"": "duplicate flag key", "review_enabled": "parent cycle"} {
		raw := flagDocument(t, func(doc map[string]any) {
			c := doc["contributes"].(map[string]any)
			f := c["flags"].([]any)[0].(map[string]any)
			key := "review_enabled"
			if parent != "" {
				key = "other_flag"
				f["parent"] = key
			}
			c["flags"] = append(c["flags"].([]any), map[string]any{"key": key, "label": "Other", "default": false, "scope_policy": "workspace_only", "parent": parent})
		})
		if _, _, err := ParseManifest(raw); err == nil || !strings.Contains(err.Error(), want) {
			t.Fatalf("parent %q: got %v, want %s", parent, err, want)
		}
	}
	for _, kind := range []string{"surfaces", "hooks", "resources"} {
		for _, ref := range []string{"missing", "other:review", "host:", "host:Bad"} {
			raw := flagDocument(t, func(doc map[string]any) {
				doc["contributes"].(map[string]any)[kind].([]any)[0].(map[string]any)["requires_flag"] = ref
			})
			if _, _, err := ParseManifest(raw); err == nil || !strings.Contains(err.Error(), "requires_flag") {
				t.Fatalf("%s %s: %v", kind, ref, err)
			}
		}
	}
}

func TestManifestV2PublicHostReferences(t *testing.T) {
	for _, location := range []string{"parent", "surfaces", "hooks", "resources"} {
		t.Run(location, func(t *testing.T) {
			raw := flagDocument(t, func(doc map[string]any) {
				c := doc["contributes"].(map[string]any)
				if location == "parent" {
					c["flags"].([]any)[0].(map[string]any)["parent"] = "host:reviews"
				} else {
					c[location].([]any)[0].(map[string]any)["requires_flag"] = "host:reviews"
				}
			})
			m, _, err := ParseManifest(raw)
			if err != nil {
				t.Fatal(err)
			}
			if err := m.ValidateFlagReferences(map[string]bool{"reviews": true}); err != nil {
				t.Fatal(err)
			}
			for _, public := range []map[string]bool{nil, {}, {"reviews": false}, {"other": true}, {"host:reviews": true}} {
				if err := m.ValidateFlagReferences(public); err == nil || !strings.Contains(err.Error(), "non-public or unknown") {
					t.Fatalf("public=%v: %v", public, err)
				}
			}
		})
	}
}

func TestManifestV2ParentChainAndBoundaryKeys(t *testing.T) {
	raw := flagDocument(t, func(doc map[string]any) {
		c := doc["contributes"].(map[string]any)
		c["flags"] = []any{
			map[string]any{"key": "ab", "label": "A", "default": false, "scope_policy": "workspace_only", "parent": "host:reviews"},
			map[string]any{"key": strings.Repeat("z", 64), "label": "B", "default": true, "scope_policy": "member_overridable", "parent": "ab"},
		}
		c["surfaces"].([]any)[0].(map[string]any)["requires_flag"] = strings.Repeat("z", 64)
	})
	m, canonical, err := ParseManifest(raw)
	if err != nil {
		t.Fatal(err)
	}
	if err := m.ValidateFlagReferences(map[string]bool{"reviews": true}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(canonical), `"default":false`) {
		t.Fatal("false default lost")
	}
	if m.Contributes.Flags[0].Default || m.Contributes.Flags[1].Parent != "ab" || m.Contributes.Surfaces[0].RequiresFlag != strings.Repeat("z", 64) {
		t.Fatal("typed values lost")
	}
}

func TestManifestV2StrictUnknownFields(t *testing.T) {
	for _, location := range []string{"root", "contributes", "surfaces", "hooks", "resources"} {
		raw := flagDocument(t, func(doc map[string]any) {
			c := doc["contributes"].(map[string]any)
			switch location {
			case "root":
				doc["surprise"] = true
			case "contributes":
				c["surprise"] = true
			default:
				c[location].([]any)[0].(map[string]any)["surprise"] = true
			}
		})
		if _, _, err := ParseManifest(raw); err == nil || !strings.Contains(err.Error(), "unknown field") {
			t.Fatalf("%s: %v", location, err)
		}
	}
}

func TestManifestV1RejectsPresenceAcrossDuplicateAndCaseFoldedFields(t *testing.T) {
	raw := strings.Replace(validManifest, `"contributes": {`, `"CONTRIBUTES":{"FLAGS":null}, "contributes": {`, 1)
	if _, _, err := ParseManifest([]byte(raw)); err == nil {
		t.Fatal("case folded earlier flags accepted")
	}
	raw = strings.Replace(validManifest, `"key": "hello",`, `"REQUIRES_FLAG":null,"key": "hello",`, 1)
	if _, _, err := ParseManifest([]byte(raw)); err == nil {
		t.Fatal("case folded gate accepted")
	}
	raw = strings.Replace(validManifest, `"type": "object", "properties":`, `"type": "object", "flags": [], "requires_flag":"custom schema extension", "properties":`, 1)
	if _, _, err := ParseManifest([]byte(raw)); err != nil {
		t.Fatalf("arbitrary input schema changed: %v", err)
	}
}

func TestManifestFlagCapabilityForHostOnlyGate(t *testing.T) {
	for _, kind := range []string{"surfaces", "hooks", "resources"} {
		raw := mutate(t, func(doc map[string]any) {
			doc["manifest_version"] = 2
			doc["contributes"].(map[string]any)[kind].([]any)[0].(map[string]any)["requires_flag"] = "host:reviews"
		})
		m, _, err := ParseManifest(raw)
		if err != nil {
			t.Fatal(err)
		}
		host := HostCapabilities()
		if !host.Flags {
			t.Fatal("host does not advertise flags")
		}
		host.Flags = false
		if err := m.CheckCapabilities(host); err == nil || !strings.Contains(err.Error(), "flags") {
			t.Fatalf("%s: %v", kind, err)
		}
	}
}

func TestManifestValidateRejectsV1FlagValues(t *testing.T) {
	m, _, err := ParseManifest(flagDocument(t, nil))
	if err != nil {
		t.Fatal(err)
	}
	m.ManifestVersion = 1
	if err := m.Validate(); err == nil || !strings.Contains(err.Error(), "require manifest_version 2") {
		t.Fatalf("programmatic v1 accepted flags: %v", err)
	}
	m.Contributes.Flags = []FlagDefinition{}
	if err := m.Validate(); err == nil || !strings.Contains(err.Error(), "require manifest_version 2") {
		t.Fatalf("programmatic v1 accepted empty flags: %v", err)
	}
	m.Contributes.Flags = nil
	m.Contributes.Hooks[0].RequiresFlag = "host:reviews"
	if err := m.ValidateFlagReferences(map[string]bool{"reviews": true}); err == nil || !strings.Contains(err.Error(), "require manifest_version 2") {
		t.Fatalf("programmatic v1 accepted gates: %v", err)
	}
}

func TestManifestV2NamespacedFlagLength(t *testing.T) {
	for _, size := range []int{255, 256} {
		raw := flagDocument(t, func(doc map[string]any) {
			// plugin:<plugin-key>:<flag-key> fits the host's 255-byte key bound.
			doc["key"] = "com." + strings.Repeat("a", size-len("plugin:")-1-len("review_enabled")-len("com."))
		})
		_, _, err := ParseManifest(raw)
		if size == 255 && err != nil {
			t.Fatalf("exact bound rejected: %v", err)
		}
		if size == 256 && (err == nil || !strings.Contains(err.Error(), "namespaced")) {
			t.Fatalf("oversized key: %v", err)
		}
	}
}
