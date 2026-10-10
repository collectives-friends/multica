package main

import (
	"context"
	"errors"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/multica-ai/multica/server/internal/analytics"
	"github.com/multica-ai/multica/server/internal/events"
	"github.com/multica-ai/multica/server/internal/featureflags"
	composiointeg "github.com/multica-ai/multica/server/internal/integrations/composio"
	"github.com/multica-ai/multica/server/internal/realtime"
	composiosdk "github.com/multica-ai/multica/server/pkg/composio"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
	"github.com/multica-ai/multica/server/pkg/featureflag"
)

// Unexpected catalog access is rejected locally, including on a failing
// regression run; these routing tests must never contact Composio.
type composioGateSDK struct{ composiointeg.SDK }

func (composioGateSDK) ListAuthConfigs(context.Context, composiosdk.ListAuthConfigsRequest) (*composiosdk.ListAuthConfigsResponse, error) {
	return nil, errors.New("unexpected catalog access in routing test")
}

func TestComposioWorkspaceFeatureGate(t *testing.T) {
	t.Setenv("COMPOSIO_API_KEY", "")
	ctx := context.Background()
	createWorkspace := func(member, enabled bool) (string, string) {
		t.Helper()
		id, slug := uuid.NewString(), "composio-flags-"+uuid.NewString()
		if _, err := testPool.Exec(ctx, `INSERT INTO workspace (id,name,slug) VALUES ($1,'Composio gate test',$2)`, id, slug); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() {
			testPool.Exec(ctx, `DELETE FROM workspace_feature_flag_override WHERE workspace_id=$1`, id)
			testPool.Exec(ctx, `DELETE FROM member WHERE workspace_id=$1`, id)
			testPool.Exec(ctx, `DELETE FROM workspace WHERE id=$1`, id)
		})
		if member {
			if _, err := testPool.Exec(ctx, `INSERT INTO member (workspace_id,user_id,role) VALUES ($1,$2,'member')`, id, testUserID); err != nil {
				t.Fatal(err)
			}
		}
		if _, err := testPool.Exec(ctx, `INSERT INTO workspace_feature_flag_override (workspace_id,scope_type,scope_id,flag_key,enabled,updated_by_type) VALUES ($1,'workspace',$1,$2,$3,'system')`, id, featureflags.ComposioMCPApps, enabled); err != nil {
			t.Fatal(err)
		}
		return id, slug
	}
	onID, onSlug := createWorkspace(true, true)
	_, offSlug := createWorkspace(true, false)
	_, foreignSlug := createWorkspace(false, true)
	provider := featureflag.NewStaticProvider()
	router, h := NewRouterWithOptions(testPool, realtime.NewHub(), events.New(), analytics.NoopClient{}, nil, RouterOptions{FeatureFlags: featureflag.NewService(provider)})
	var err error
	h.Composio, err = composiointeg.NewService(composioGateSDK{}, db.New(testPool), composiointeg.Config{StateSecret: []byte("test-composio-secret"), CallbackBaseURL: "http://localhost"})
	if err != nil {
		t.Fatal(err)
	}
	request := func(method, path, header, value string, want int) {
		t.Helper()
		req := httptest.NewRequest(method, path, nil)
		req.Header.Set("Authorization", "Bearer "+testToken)
		if header != "" {
			req.Header.Set(header, value)
		}
		w := httptest.NewRecorder()
		router.ServeHTTP(w, req)
		if want == 404 && !strings.Contains(w.Body.String(), "workspace not found") {
			t.Errorf("expected membership middleware rejection, got %s", w.Body.String())
		}
		if w.Code != want {
			t.Errorf("%s %s %s=%s: got %d (%s), want %d", method, path, header, value, w.Code, w.Body.String(), want)
		}
	}
	const connections = "/api/integrations/composio/connections"
	request("GET", connections, "X-Workspace-Slug", onSlug, 200)
	request("GET", connections, "X-Workspace-ID", onID, 200)
	request("GET", connections+"?workspace_slug="+onSlug, "", "", 200)
	request("GET", connections+"?workspace_id="+onID, "", "", 200)
	request("GET", connections, "X-Workspace-Slug", offSlug, 403)
	request("GET", connections, "X-Workspace-Slug", foreignSlug, 404)
	request("GET", connections, "", "", 403)
	// Legacy clients without a selection retain the explicit deployment override.
	provider.Set(featureflags.ComposioMCPApps, featureflag.Rule{Default: true})
	request("GET", connections, "", "", 200)
	// An invalid explicit selector must never fall back to the deployment flag;
	// verify the membership middleware is installed on all four management routes.
	for _, route := range []struct{ method, path string }{
		{"GET", connections}, {"GET", "/api/integrations/composio/toolkits"},
		{"POST", "/api/integrations/composio/connect/init"},
		{"DELETE", connections + "/" + uuid.NewString()},
	} {
		request(route.method, route.path, "X-Workspace-Slug", "no-such-composio-workspace", 404)
	}
}
