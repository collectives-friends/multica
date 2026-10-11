package plugincontract

import "testing"

func TestIntegrationSurfacesAndScopes(t *testing.T) {
	for _, surface := range []string{SurfaceSettingsPanel, SurfaceWorkspacePanel, SurfaceProjectPanel} {
		raw := []byte(`{"manifest_version":2,"key":"test.integration","name":"Integration","version":"1.0.0","author":{"name":"Test"},"scopes":["projects:read","projects:write","installation:read"],"contributes":{"surfaces":[{"key":"panel","type":"` + surface + `","name":"Panel","entry":"ui.js"}]}}`)
		manifest, _, err := ParseManifest(raw)
		if err != nil {
			t.Fatal(err)
		}
		if err = manifest.CheckCapabilities(HostCapabilities()); err != nil {
			t.Fatal(err)
		}
	}
}
