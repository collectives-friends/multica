package handler

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/multica-ai/multica/server/internal/service"
	"github.com/multica-ai/multica/server/internal/util"
	"github.com/multica-ai/multica/server/internal/util/secretbox"
)

const connectionTestSecret = "sk-live-attio-connection-never-echo"

// enableConnectionSecretsForTest wires a throwaway encryption box, then
// restores the previous one so no other test inherits a key it did not ask for.
func enableConnectionSecretsForTest(t *testing.T) {
	t.Helper()
	if testHandler == nil || testHandler.ConnectionService == nil {
		t.Skip("database not available")
	}
	previous := testHandler.ConnectionService.Secrets
	box, err := secretbox.New([]byte("0123456789abcdef0123456789abcdef"))
	if err != nil {
		t.Fatalf("secretbox.New: %v", err)
	}
	testHandler.ConnectionService.Secrets = box
	t.Cleanup(func() { testHandler.ConnectionService.Secrets = previous })
}

// createConnectionForTest drives the real handler so the request shape and the
// response are the ones the product uses.
func createConnectionForTest(t *testing.T, body map[string]any) (int, map[string]any, string) {
	t.Helper()
	if testHandler == nil {
		t.Skip("database not available")
	}
	req := newRequest(http.MethodPost, "/api/workspaces/"+testWorkspaceID+"/connections", body)
	req = withURLParam(req, "id", testWorkspaceID)
	w := httptest.NewRecorder()
	testHandler.CreateConnection(w, req)
	var decoded map[string]any
	raw := w.Body.String()
	if w.Code == http.StatusCreated {
		if err := json.Unmarshal([]byte(raw), &decoded); err != nil {
			t.Fatalf("decode create response: %v", err)
		}
	}
	return w.Code, decoded, raw
}

func connectionIDFromCreate(t *testing.T, decoded map[string]any) string {
	t.Helper()
	connection, ok := decoded["connection"].(map[string]any)
	if !ok {
		t.Fatalf("create response has no connection object: %v", decoded)
	}
	id, _ := connection["id"].(string)
	if id == "" {
		t.Fatalf("create response has no connection id: %v", decoded)
	}
	t.Cleanup(func() {
		ctx := context.Background()
		testPool.Exec(ctx, `DELETE FROM connection_binding WHERE connection_id = $1`, id)
		testPool.Exec(ctx, `DELETE FROM connection_secret WHERE connection_id = $1`, id)
		testPool.Exec(ctx, `DELETE FROM connection WHERE id = $1`, id)
	})
	return id
}

func connectionRequest(method, connectionID, suffix string, body any) (*http.Request, *httptest.ResponseRecorder) {
	req := newRequest(method, "/api/workspaces/"+testWorkspaceID+"/connections/"+connectionID+suffix, body)
	req = withURLParams(req, "id", testWorkspaceID, "connectionId", connectionID)
	return req, httptest.NewRecorder()
}

func TestListConnectionProviders_ExposesBuiltins(t *testing.T) {
	if testHandler == nil {
		t.Skip("database not available")
	}
	req := newRequest(http.MethodGet, "/api/workspaces/"+testWorkspaceID+"/connections/providers", nil)
	req = withURLParam(req, "id", testWorkspaceID)
	w := httptest.NewRecorder()
	testHandler.ListConnectionProviders(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}
	raw := w.Body.String()
	for _, key := range []string{"attio", "generic-api-key"} {
		if !strings.Contains(raw, key) {
			t.Fatalf("provider %q missing from registry response: %s", key, raw)
		}
	}
}

func TestCreateConnection_RejectsUnknownProvider(t *testing.T) {
	if testHandler == nil {
		t.Skip("database not available")
	}
	code, _, raw := createConnectionForTest(t, map[string]any{
		"provider_key": "not-a-provider",
		"name":         "Nope",
	})
	if code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d: %s", code, raw)
	}
}

// The secret is write-only: no read path returns it, and the database stores
// ciphertext rather than the value. Asserted on the raw response body so a
// secret-bearing field added later fails here.
func TestConnectionSecret_IsWriteOnlyAndEncryptedAtRest(t *testing.T) {
	enableConnectionSecretsForTest(t)
	code, decoded, raw := createConnectionForTest(t, map[string]any{
		"provider_key": "attio",
		"name":         "Attio production",
	})
	if code != http.StatusCreated {
		t.Fatalf("create failed: %d %s", code, raw)
	}
	connectionID := connectionIDFromCreate(t, decoded)

	req, w := connectionRequest(http.MethodPut, connectionID, "/secret", map[string]any{
		"key":   "api_key",
		"value": connectionTestSecret,
	})
	testHandler.SetConnectionSecret(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("set secret failed: %d %s", w.Code, w.Body.String())
	}
	if strings.Contains(w.Body.String(), connectionTestSecret) {
		t.Fatalf("set-secret response echoed the value: %s", w.Body.String())
	}

	req, w = connectionRequest(http.MethodGet, connectionID, "", nil)
	testHandler.GetConnection(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("get connection failed: %d %s", w.Code, w.Body.String())
	}
	if strings.Contains(w.Body.String(), connectionTestSecret) {
		t.Fatalf("get response echoed the value: %s", w.Body.String())
	}
	if !strings.Contains(w.Body.String(), `"configured_secrets":["api_key"]`) {
		t.Fatalf("configured_secrets does not report the stored key: %s", w.Body.String())
	}

	req = newRequest(http.MethodGet, "/api/workspaces/"+testWorkspaceID+"/connections", nil)
	req = withURLParam(req, "id", testWorkspaceID)
	w = httptest.NewRecorder()
	testHandler.ListConnections(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("list failed: %d %s", w.Code, w.Body.String())
	}
	if strings.Contains(w.Body.String(), connectionTestSecret) {
		t.Fatalf("list response echoed the value: %s", w.Body.String())
	}

	// At rest: the plaintext must not appear in the row at all, and the
	// ciphertext must be the secretbox output the service can open.
	var ciphertext []byte
	var reference string
	if err := testPool.QueryRow(context.Background(),
		`SELECT ciphertext, reference FROM connection_secret WHERE connection_id = $1 AND key = 'api_key'`,
		connectionID).Scan(&ciphertext, &reference); err != nil {
		t.Fatalf("read stored secret: %v", err)
	}
	if len(ciphertext) == 0 || reference != "" {
		t.Fatalf("host-mode secret stored ciphertext=%d reference=%q", len(ciphertext), reference)
	}
	if strings.Contains(string(ciphertext), connectionTestSecret) {
		t.Fatal("stored ciphertext contains the plaintext")
	}
	opened, err := testHandler.ConnectionService.Secrets.Open(ciphertext)
	if err != nil || string(opened) != connectionTestSecret {
		t.Fatalf("stored ciphertext did not decrypt to the value (err=%v)", err)
	}
}

func TestConnectionReferenceMode_StoresPointerNotSecret(t *testing.T) {
	code, decoded, raw := createConnectionForTest(t, map[string]any{
		"provider_key": "attio",
		"name":         "Attio reference",
		"secret_mode":  "reference",
	})
	if code != http.StatusCreated {
		t.Fatalf("create failed: %d %s", code, raw)
	}
	connectionID := connectionIDFromCreate(t, decoded)

	req, w := connectionRequest(http.MethodPut, connectionID, "/secret", map[string]any{
		"key":   "api_key",
		"value": "tc-data-kv/attio-api-key",
	})
	testHandler.SetConnectionSecret(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("set reference failed: %d %s", w.Code, w.Body.String())
	}

	var ciphertext []byte
	var reference string
	if err := testPool.QueryRow(context.Background(),
		`SELECT ciphertext, reference FROM connection_secret WHERE connection_id = $1 AND key = 'api_key'`,
		connectionID).Scan(&ciphertext, &reference); err != nil {
		t.Fatalf("read stored reference: %v", err)
	}
	if len(ciphertext) != 0 {
		t.Fatalf("reference mode stored %d bytes of ciphertext", len(ciphertext))
	}
	if reference != "tc-data-kv/attio-api-key" {
		t.Fatalf("reference = %q, want the operator pointer", reference)
	}
}

func TestUpdateConnection_StaleRevisionConflicts(t *testing.T) {
	code, decoded, raw := createConnectionForTest(t, map[string]any{
		"provider_key": "attio",
		"name":         "Attio revision",
	})
	if code != http.StatusCreated {
		t.Fatalf("create failed: %d %s", code, raw)
	}
	connectionID := connectionIDFromCreate(t, decoded)

	req, w := connectionRequest(http.MethodPatch, connectionID, "", map[string]any{
		"name":              "Renamed",
		"expected_revision": 99,
	})
	testHandler.UpdateConnection(w, req)
	if w.Code != http.StatusConflict {
		t.Fatalf("stale revision expected 409, got %d: %s", w.Code, w.Body.String())
	}

	req, w = connectionRequest(http.MethodPatch, connectionID, "", map[string]any{
		"name":              "Renamed",
		"expected_revision": 1,
	})
	testHandler.UpdateConnection(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("current revision expected 200, got %d: %s", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), `"revision":2`) {
		t.Fatalf("accepted update did not advance the revision: %s", w.Body.String())
	}
}

// A binding is the grant. Without one, a consumer is refused even though the
// connection exists and is enabled; revoking the binding restores that refusal.
func TestConnectionBindings_GrantAndRevoke(t *testing.T) {
	code, decoded, raw := createConnectionForTest(t, map[string]any{
		"provider_key": "attio",
		"name":         "Attio bindings",
	})
	if code != http.StatusCreated {
		t.Fatalf("create failed: %d %s", code, raw)
	}
	connectionID := connectionIDFromCreate(t, decoded)
	connectionUUID, err := util.ParseUUID(connectionID)
	if err != nil {
		t.Fatalf("parse connection id: %v", err)
	}
	consumerUUID, err := util.ParseUUID(testUserID)
	if err != nil {
		t.Fatalf("parse consumer id: %v", err)
	}

	if _, err := testHandler.ConnectionService.AuthorizeConsumer(context.Background(), connectionUUID, service.ConnectionConsumerNative, consumerUUID); err == nil {
		t.Fatal("an unbound consumer was authorized")
	}

	req, w := connectionRequest(http.MethodPost, connectionID, "/bindings", map[string]any{
		"consumer_type": service.ConnectionConsumerNative,
		"consumer_id":   testUserID,
	})
	testHandler.BindConnection(w, req)
	if w.Code != http.StatusCreated {
		t.Fatalf("bind failed: %d %s", w.Code, w.Body.String())
	}
	if _, err := testHandler.ConnectionService.AuthorizeConsumer(context.Background(), connectionUUID, service.ConnectionConsumerNative, consumerUUID); err != nil {
		t.Fatalf("bound consumer was refused: %v", err)
	}

	req, w = connectionRequest(http.MethodDelete, connectionID, "/bindings/"+service.ConnectionConsumerNative+"/"+testUserID, nil)
	req = withURLParams(req, "id", testWorkspaceID, "connectionId", connectionID,
		"consumerType", service.ConnectionConsumerNative, "consumerId", testUserID)
	testHandler.UnbindConnection(w, req)
	if w.Code != http.StatusNoContent {
		t.Fatalf("unbind failed: %d %s", w.Code, w.Body.String())
	}
	if _, err := testHandler.ConnectionService.AuthorizeConsumer(context.Background(), connectionUUID, service.ConnectionConsumerNative, consumerUUID); err == nil {
		t.Fatal("a revoked consumer was still authorized")
	}
}

// Disabling a connection revokes every consumer's access without touching the
// bindings or the secret.
func TestConnectionDisable_RevokesConsumerAccess(t *testing.T) {
	code, decoded, raw := createConnectionForTest(t, map[string]any{
		"provider_key": "attio",
		"name":         "Attio disable",
	})
	if code != http.StatusCreated {
		t.Fatalf("create failed: %d %s", code, raw)
	}
	connectionID := connectionIDFromCreate(t, decoded)
	connectionUUID, _ := util.ParseUUID(connectionID)
	consumerUUID, _ := util.ParseUUID(testUserID)

	req, w := connectionRequest(http.MethodPost, connectionID, "/bindings", map[string]any{
		"consumer_type": service.ConnectionConsumerNative,
		"consumer_id":   testUserID,
	})
	testHandler.BindConnection(w, req)
	if w.Code != http.StatusCreated {
		t.Fatalf("bind failed: %d %s", w.Code, w.Body.String())
	}

	req, w = connectionRequest(http.MethodPost, connectionID, "/disable", nil)
	testHandler.DisableConnection(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("disable failed: %d %s", w.Code, w.Body.String())
	}
	if _, err := testHandler.ConnectionService.AuthorizeConsumer(context.Background(), connectionUUID, service.ConnectionConsumerNative, consumerUUID); err == nil {
		t.Fatal("a disabled connection still authorized its consumer")
	}

	req, w = connectionRequest(http.MethodPost, connectionID, "/enable", nil)
	testHandler.EnableConnection(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("enable failed: %d %s", w.Code, w.Body.String())
	}
	if _, err := testHandler.ConnectionService.AuthorizeConsumer(context.Background(), connectionUUID, service.ConnectionConsumerNative, consumerUUID); err != nil {
		t.Fatalf("re-enabled connection refused its consumer: %v", err)
	}
}

func TestTestConnection_RequiresStoredCredential(t *testing.T) {
	code, decoded, raw := createConnectionForTest(t, map[string]any{
		"provider_key": "attio",
		"name":         "Attio unverified",
	})
	if code != http.StatusCreated {
		t.Fatalf("create failed: %d %s", code, raw)
	}
	connectionID := connectionIDFromCreate(t, decoded)

	req, w := connectionRequest(http.MethodPost, connectionID, "/test", nil)
	testHandler.TestConnection(w, req)
	// No secret stored yet: the probe must fail before it ever dials out.
	if w.Code != http.StatusBadRequest && w.Code != http.StatusServiceUnavailable {
		t.Fatalf("expected a fail-closed status, got %d: %s", w.Code, w.Body.String())
	}
}

func TestDeleteConnection_RemovesSecretsAndBindings(t *testing.T) {
	enableConnectionSecretsForTest(t)
	code, decoded, raw := createConnectionForTest(t, map[string]any{
		"provider_key": "attio",
		"name":         "Attio delete",
	})
	if code != http.StatusCreated {
		t.Fatalf("create failed: %d %s", code, raw)
	}
	connectionID := connectionIDFromCreate(t, decoded)

	req, w := connectionRequest(http.MethodPut, connectionID, "/secret", map[string]any{"key": "api_key", "value": connectionTestSecret})
	testHandler.SetConnectionSecret(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("set secret failed: %d %s", w.Code, w.Body.String())
	}
	req, w = connectionRequest(http.MethodPost, connectionID, "/bindings", map[string]any{
		"consumer_type": service.ConnectionConsumerNative,
		"consumer_id":   testUserID,
	})
	testHandler.BindConnection(w, req)
	if w.Code != http.StatusCreated {
		t.Fatalf("bind failed: %d %s", w.Code, w.Body.String())
	}

	req, w = connectionRequest(http.MethodDelete, connectionID, "", nil)
	testHandler.DeleteConnection(w, req)
	if w.Code != http.StatusNoContent {
		t.Fatalf("delete failed: %d %s", w.Code, w.Body.String())
	}

	var connections, secrets, bindings int
	ctx := context.Background()
	if err := testPool.QueryRow(ctx, `SELECT count(*) FROM connection WHERE id = $1`, connectionID).Scan(&connections); err != nil {
		t.Fatalf("count connections: %v", err)
	}
	if err := testPool.QueryRow(ctx, `SELECT count(*) FROM connection_secret WHERE connection_id = $1`, connectionID).Scan(&secrets); err != nil {
		t.Fatalf("count secrets: %v", err)
	}
	if err := testPool.QueryRow(ctx, `SELECT count(*) FROM connection_binding WHERE connection_id = $1`, connectionID).Scan(&bindings); err != nil {
		t.Fatalf("count bindings: %v", err)
	}
	if connections != 0 || secrets != 0 || bindings != 0 {
		t.Fatalf("delete left rows behind: connections=%d secrets=%d bindings=%d", connections, secrets, bindings)
	}
}
