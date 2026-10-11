package handler

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/multica-ai/multica/server/internal/featureflags"
	"github.com/multica-ai/multica/server/internal/service"
	publicapiv1 "github.com/multica-ai/multica/server/pkg/publicapi/v1"
)

// issueInstallTokenForTest mints the standing credential a plugin backend uses.
func issueInstallTokenForTest(t *testing.T, installationID string) string {
	t.Helper()
	token, err := testHandler.PluginService.IssueInstallToken(context.Background(), parseUUID(installationID))
	if err != nil {
		t.Fatalf("issue install token: %v", err)
	}
	return token
}

func pluginConnectionRequest(method, path, token string, body any, params map[string]string) *http.Request {
	return pluginInstallTokenRequest(method, path, token, body, params)
}

func TestPluginConnectionsRequireTheFeatureFlag(t *testing.T) {
	if testHandler == nil {
		t.Skip("database not available")
	}
	withFeatureFlag(t, testHandler, featureflags.ConnectionsV1, false)
	recorder := httptest.NewRecorder()

	testHandler.ListPluginConnections(recorder, pluginConnectionRequest(http.MethodGet, "/v1/connections", "mpi_invalid", nil, nil))

	if recorder.Code != http.StatusForbidden {
		t.Fatalf("status=%d body=%s, want 403", recorder.Code, recorder.Body.String())
	}
	var problem publicapiv1.Problem
	if err := json.Unmarshal(recorder.Body.Bytes(), &problem); err != nil {
		t.Fatalf("decode problem: %v", err)
	}
	if problem.Code != "connections_disabled" {
		t.Fatalf("problem code = %q, want connections_disabled", problem.Code)
	}
}

// A plugin sees exactly the connections an administrator bound to it, and
// nothing about the credential.
func TestPluginConnectionsListIsBoundOnlyAndSecretFree(t *testing.T) {
	enableConnectionSecretsForTest(t)
	installationID := installPluginForAction(t, []string{"connections:read"})
	token := issueInstallTokenForTest(t, installationID)

	recorder := httptest.NewRecorder()
	testHandler.ListPluginConnections(recorder, pluginConnectionRequest(http.MethodGet, "/v1/connections", token, nil, nil))
	if recorder.Code != http.StatusOK {
		t.Fatalf("list status=%d body=%s", recorder.Code, recorder.Body.String())
	}
	var empty struct {
		Connections []pluginConnectionDTO `json:"connections"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &empty); err != nil {
		t.Fatalf("decode list: %v", err)
	}
	if len(empty.Connections) != 0 {
		t.Fatalf("unbound installation saw %d connections, want none", len(empty.Connections))
	}

	code, decoded, raw := createConnectionForTest(t, map[string]any{
		"provider_key": "attio",
		"name":         "Attio plugin binding",
	})
	if code != http.StatusCreated {
		t.Fatalf("create connection: %d %s", code, raw)
	}
	connectionID := connectionIDFromCreate(t, decoded)

	req, w := connectionRequest(http.MethodPut, connectionID, "/secret", map[string]any{"key": "api_key", "value": connectionTestSecret})
	testHandler.SetConnectionSecret(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("set secret: %d %s", w.Code, w.Body.String())
	}

	installationUUID := parseUUID(installationID)
	connectionUUID := parseUUID(connectionID)
	workspaceUUID := parseUUID(testWorkspaceID)
	connection, err := testHandler.ConnectionService.GetWorkspaceConnection(context.Background(), workspaceUUID, connectionUUID)
	if err != nil {
		t.Fatalf("load connection: %v", err)
	}
	if _, err := testHandler.ConnectionService.BindConsumer(
		context.Background(),
		connection,
		service.ConnectionConsumerPluginInstallation,
		installationUUID,
		workspaceUUID,
	); err != nil {
		t.Fatalf("bind connection: %v", err)
	}

	recorder = httptest.NewRecorder()
	testHandler.ListPluginConnections(recorder, pluginConnectionRequest(http.MethodGet, "/v1/connections", token, nil, nil))
	if recorder.Code != http.StatusOK {
		t.Fatalf("list status=%d body=%s", recorder.Code, recorder.Body.String())
	}
	body := recorder.Body.String()
	if !strings.Contains(body, `"provider_key":"attio"`) {
		t.Fatalf("bound connection missing from list: %s", body)
	}
	if strings.Contains(body, connectionTestSecret) || strings.Contains(body, "configured_secrets") {
		t.Fatalf("list response leaked credential material: %s", body)
	}
}

// The binding is the grant: an installation without one is refused before any
// credential is touched.
func TestInvokePluginConnectionRefusesUnboundInstallation(t *testing.T) {
	installationID := installPluginForAction(t, []string{"connections:invoke"})
	token := issueInstallTokenForTest(t, installationID)

	code, decoded, raw := createConnectionForTest(t, map[string]any{
		"provider_key": "attio",
		"name":         "Attio unbound invoke",
	})
	if code != http.StatusCreated {
		t.Fatalf("create connection: %d %s", code, raw)
	}
	connectionID := connectionIDFromCreate(t, decoded)

	recorder := httptest.NewRecorder()
	testHandler.InvokePluginConnection(recorder, pluginConnectionRequest(
		http.MethodPost,
		"/v1/connections/"+connectionID+"/request",
		token,
		map[string]any{"method": "GET", "path": "/v2/self"},
		map[string]string{"connection_id": connectionID},
	))
	if recorder.Code != http.StatusForbidden {
		t.Fatalf("unbound invoke status=%d body=%s, want 403", recorder.Code, recorder.Body.String())
	}
}

// A reference connection cannot be proxied: the host has no value to attach,
// and saying so is better than dialing with nothing.
func TestInvokePluginConnectionRefusesReferenceMode(t *testing.T) {
	installationID := installPluginForAction(t, []string{"connections:invoke"})
	token := issueInstallTokenForTest(t, installationID)

	code, decoded, raw := createConnectionForTest(t, map[string]any{
		"provider_key": "attio",
		"name":         "Attio reference invoke",
		"secret_mode":  "reference",
	})
	if code != http.StatusCreated {
		t.Fatalf("create connection: %d %s", code, raw)
	}
	connectionID := connectionIDFromCreate(t, decoded)

	installationUUID := parseUUID(installationID)
	connectionUUID := parseUUID(connectionID)
	workspaceUUID := parseUUID(testWorkspaceID)
	connection, err := testHandler.ConnectionService.GetWorkspaceConnection(context.Background(), workspaceUUID, connectionUUID)
	if err != nil {
		t.Fatalf("load connection: %v", err)
	}
	if _, err := testHandler.ConnectionService.BindConsumer(
		context.Background(),
		connection,
		service.ConnectionConsumerPluginInstallation,
		installationUUID,
		workspaceUUID,
	); err != nil {
		t.Fatalf("bind connection: %v", err)
	}

	recorder := httptest.NewRecorder()
	testHandler.InvokePluginConnection(recorder, pluginConnectionRequest(
		http.MethodPost,
		"/v1/connections/"+connectionID+"/request",
		token,
		map[string]any{"method": "GET", "path": "/v2/self"},
		map[string]string{"connection_id": connectionID},
	))
	if recorder.Code != http.StatusForbidden {
		t.Fatalf("reference invoke status=%d body=%s, want 403", recorder.Code, recorder.Body.String())
	}
}
