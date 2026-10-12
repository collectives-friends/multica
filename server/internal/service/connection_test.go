package service

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgtype"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
	"github.com/multica-ai/multica/server/pkg/plugincontract"
)

func TestBuiltinConnectionProviders_AttioIsProbeable(t *testing.T) {
	provider, ok := ConnectionProviderFor("attio")
	if !ok {
		t.Fatal("attio provider is not registered")
	}
	if provider.AuthKind != "api_key" {
		t.Fatalf("attio auth kind = %q, want api_key", provider.AuthKind)
	}
	if provider.Health == nil {
		t.Fatal("attio has no health check, so a stored credential could never be verified")
	}
	if len(provider.NetDomains) != 1 || provider.NetDomains[0] != "api.attio.com" {
		t.Fatalf("attio net domains = %v, want [api.attio.com]", provider.NetDomains)
	}
	if provider.Injection.Header != "Authorization" || provider.Injection.Scheme != "Bearer" {
		t.Fatalf("attio injection = %+v, want Authorization: Bearer", provider.Injection)
	}
}

func TestConnectionProviderFor_UnknownKeyIsRejected(t *testing.T) {
	if _, ok := ConnectionProviderFor("not-a-provider"); ok {
		t.Fatal("an unregistered provider key was accepted")
	}
}

func TestValidateConnectionConfig(t *testing.T) {
	schema := plugincontract.ConfigSchema{}
	raw := []byte(`{"tenant":{"type":"string","label":"Tenant","required":true},"region":{"type":"enum","label":"Region","options":["eu","us"]},"retries":{"type":"number","label":"Retries"},"active":{"type":"bool","label":"Active"},"token":{"type":"secret","label":"Token"}}`)
	if err := schema.UnmarshalJSON(raw); err != nil {
		t.Fatalf("parse test schema: %v", err)
	}
	provider := ConnectionProvider{Key: "test", Schema: schema}

	cases := []struct {
		name    string
		config  map[string]any
		wantErr bool
	}{
		{"valid", map[string]any{"tenant": "acme", "region": "eu", "retries": float64(3), "active": true}, false},
		{"required missing", map[string]any{"region": "eu"}, true},
		{"unknown key", map[string]any{"tenant": "acme", "nope": "x"}, true},
		{"wrong type", map[string]any{"tenant": 7}, true},
		{"unknown enum option", map[string]any{"tenant": "acme", "region": "mars"}, true},
		{"secret must not be config", map[string]any{"tenant": "acme", "token": "sk-live"}, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := validateConnectionConfig(provider, tc.config)
			if tc.wantErr && err == nil {
				t.Fatal("expected an error, got none")
			}
			if !tc.wantErr && err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
		})
	}
}

func TestValidateConnectionConfig_EmptySchemaRejectsFields(t *testing.T) {
	_, err := validateConnectionConfig(ConnectionProvider{Key: "attio"}, map[string]any{"anything": "x"})
	if err == nil {
		t.Fatal("a provider with no schema accepted a configuration field")
	}
}

// probeHealth is the part of CheckHealth that talks to the network; the SSRF
// guard runs before it on the real path, so a local server here cannot be
// reached by an attacker through the service.
func TestProbeHealth_MapsStatusAndInjectsCredential(t *testing.T) {
	const secret = "sk-live-connection-probe"
	var seenAuth string
	var seenAPIKey string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seenAuth = r.Header.Get("Authorization")
		seenAPIKey = r.Header.Get("x-api-key")
		switch r.URL.Path {
		case "/ok":
			w.WriteHeader(http.StatusOK)
		case "/denied":
			w.WriteHeader(http.StatusUnauthorized)
		case "/broken":
			w.WriteHeader(http.StatusInternalServerError)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer server.Close()
	endpointFor := func(path string) *url.URL {
		parsed, err := url.Parse(server.URL + path)
		if err != nil {
			t.Fatalf("parse test server URL: %v", err)
		}
		return parsed
	}

	svc := &ConnectionService{HTTPClient: server.Client()}
	health := ConnectionHealthCheck{Method: http.MethodGet, URL: server.URL + "/ok", ExpectStatus: http.StatusOK}

	status, detail := svc.probeHealth(context.Background(), endpointFor("/ok"), health, CredentialInjection{Header: "Authorization", Scheme: "Bearer"}, secret)
	if status != ConnectionStatusOK || detail != "" {
		t.Fatalf("ok probe = %q/%q, want ok/empty", status, detail)
	}
	if seenAuth != "Bearer "+secret {
		t.Fatalf("Authorization header = %q, want the injected credential", seenAuth)
	}
	if seenAPIKey != "" {
		t.Fatalf("unexpected x-api-key header %q", seenAPIKey)
	}

	health.URL = server.URL + "/denied"
	status, _ = svc.probeHealth(context.Background(), endpointFor("/denied"), health, CredentialInjection{Header: "Authorization", Scheme: "Bearer"}, secret)
	if status != ConnectionStatusNeedsReauth {
		t.Fatalf("401 probe = %q, want needs_reauth", status)
	}

	health.URL = server.URL + "/broken"
	status, detail = svc.probeHealth(context.Background(), endpointFor("/broken"), health, CredentialInjection{Header: "Authorization", Scheme: "Bearer"}, secret)
	if status != ConnectionStatusError || detail == "" {
		t.Fatalf("500 probe = %q/%q, want error with detail", status, detail)
	}
}

func TestProbeHealth_CustomHeaderInjection(t *testing.T) {
	const secret = "sk-custom-header"
	var seen string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen = r.Header.Get("x-api-key")
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()
	serverURL, _ := url.Parse(server.URL)

	svc := &ConnectionService{HTTPClient: server.Client()}
	status, _ := svc.probeHealth(context.Background(), serverURL,
		ConnectionHealthCheck{Method: http.MethodGet, URL: server.URL, ExpectStatus: http.StatusOK},
		CredentialInjection{Header: "x-api-key"}, secret)
	if status != ConnectionStatusOK {
		t.Fatalf("probe status = %q, want ok", status)
	}
	if seen != secret {
		t.Fatalf("x-api-key header = %q, want the credential without a scheme prefix", seen)
	}
}

func TestApplyConnectionCredential_QueryInjection(t *testing.T) {
	request, err := http.NewRequest(http.MethodGet, "https://example.test/path?keep=1", nil)
	if err != nil {
		t.Fatalf("build request: %v", err)
	}
	applyConnectionCredential(request, CredentialInjection{Query: "api_key"}, "sk-query")
	if got := request.URL.Query().Get("api_key"); got != "sk-query" {
		t.Fatalf("query credential = %q, want sk-query", got)
	}
	if got := request.URL.Query().Get("keep"); got != "1" {
		t.Fatalf("existing query value was dropped: %q", got)
	}
	if request.Header.Get("Authorization") != "" {
		t.Fatal("query injection also set an Authorization header")
	}
}

func TestCheckHealth_RefusesReferenceModeAndUnprobeableProviders(t *testing.T) {
	svc := &ConnectionService{}
	_, err := svc.CheckHealth(context.Background(), connectionFixture("attio", ConnectionSecretModeReference))
	if err == nil {
		t.Fatal("reference-mode health check was accepted; the host cannot read an external reference")
	}

	_, err = svc.CheckHealth(context.Background(), connectionFixture("generic-api-key", ConnectionSecretModeHost))
	if err == nil {
		t.Fatal("a provider with no health check was probed")
	}

	_, err = svc.CheckHealth(context.Background(), connectionFixture("not-a-provider", ConnectionSecretModeHost))
	if err == nil {
		t.Fatal("an unknown provider was probed")
	}
}

func connectionFixture(providerKey, secretMode string) db.Connection {
	return db.Connection{
		ID:          pgtype.UUID{Bytes: [16]byte{1}, Valid: true},
		ProviderKey: providerKey,
		SecretMode:  secretMode,
		Enabled:     true,
	}
}

// The proxy is the only way a plugin uses a stored credential. These tests pin
// the two properties that matter: the credential is attached by the host and
// never returned, and the caller cannot steer the request outside the
// provider's declared host or method set.
func TestProxyWithCredential_AttachesCredentialAndReturnsResponse(t *testing.T) {
	const secret = "sk-live-attio-proxy-secret"
	var seenAuth string
	var seenPath string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seenAuth = r.Header.Get("Authorization")
		seenPath = r.URL.Path + "?" + r.URL.RawQuery
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	defer server.Close()

	svc := &ConnectionService{
		HTTPClient: server.Client(),
		validateEndpoint: func(_ context.Context, raw string, _ []string) (*url.URL, error) {
			return url.Parse(raw)
		},
	}
	provider := ConnectionProvider{
		Key:        "test",
		BaseURL:    server.URL,
		NetDomains: []string{"api.example.test"},
		Injection:  CredentialInjection{Header: "Authorization", Scheme: "Bearer"},
	}
	response, err := svc.proxyWithCredential(context.Background(), provider, secret,
		ConnectionProxyRequest{Method: http.MethodPost, Path: "/v2/records", Query: url.Values{"limit": {"10"}}, Body: []byte(`{"a":1}`)})
	if err != nil {
		t.Fatalf("proxy: %v", err)
	}
	if response.Status != http.StatusCreated || string(response.Body) != `{"ok":true}` {
		t.Fatalf("response = %d %q", response.Status, string(response.Body))
	}
	if response.ContentType != "application/json" {
		t.Fatalf("content type = %q", response.ContentType)
	}
	if seenAuth != "Bearer "+secret {
		t.Fatalf("provider saw Authorization %q, want the injected credential", seenAuth)
	}
	if seenPath != "/v2/records?limit=10" {
		t.Fatalf("provider saw path %q", seenPath)
	}
}

// The shared endpoint validator refuses any query string by contract, while a
// provider list call routinely carries one. The proxy must validate the
// query-less target and attach the query afterwards; this test fails if the
// query is ever handed to the validator again.
func TestProxyWithCredential_ValidatesWithoutQueryAndForwardsBothForms(t *testing.T) {
	var validated []string
	var seenPath string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seenPath = r.URL.Path + "?" + r.URL.RawQuery
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("{}"))
	}))
	defer server.Close()

	svc := &ConnectionService{
		HTTPClient: server.Client(),
		validateEndpoint: func(_ context.Context, raw string, _ []string) (*url.URL, error) {
			validated = append(validated, raw)
			parsed, err := url.Parse(raw)
			if err != nil {
				return nil, err
			}
			if parsed.RawQuery != "" || parsed.Fragment != "" {
				return nil, fmt.Errorf("endpoint must be a public HTTPS URL without userinfo, query, or fragment")
			}
			return parsed, nil
		},
	}
	provider := ConnectionProvider{Key: "test", BaseURL: server.URL}

	if _, err := svc.proxyWithCredential(context.Background(), provider, "sk-test",
		ConnectionProxyRequest{Method: http.MethodGet, Path: "/v2/objects/x/attributes?show_archived=true"}); err != nil {
		t.Fatalf("path-embedded query: %v", err)
	}
	if seenPath != "/v2/objects/x/attributes?show_archived=true" {
		t.Fatalf("provider saw %q, want the path-embedded query", seenPath)
	}

	if _, err := svc.proxyWithCredential(context.Background(), provider, "sk-test",
		ConnectionProxyRequest{Method: http.MethodGet, Path: "/v2/records", Query: url.Values{"limit": {"10"}}}); err != nil {
		t.Fatalf("structured query: %v", err)
	}
	if seenPath != "/v2/records?limit=10" {
		t.Fatalf("provider saw %q, want the structured query", seenPath)
	}

	if _, err := svc.proxyWithCredential(context.Background(), provider, "sk-test",
		ConnectionProxyRequest{Method: http.MethodGet, Path: "/v2/records?offset=5", Query: url.Values{"limit": {"10"}}}); err != nil {
		t.Fatalf("combined query: %v", err)
	}
	if seenPath != "/v2/records?offset=5&limit=10" {
		t.Fatalf("provider saw %q, want both query forms", seenPath)
	}

	for _, raw := range validated {
		if strings.Contains(raw, "?") {
			t.Fatalf("validator received a query: %q", raw)
		}
	}
}

func TestProxyWithCredential_RefusesUnsafeRequestsBeforeDialing(t *testing.T) {
	var dialed bool
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		dialed = true
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	svc := &ConnectionService{
		HTTPClient: server.Client(),
		validateEndpoint: func(_ context.Context, raw string, _ []string) (*url.URL, error) {
			return url.Parse(raw)
		},
	}
	provider := ConnectionProvider{Key: "test", BaseURL: server.URL, Injection: CredentialInjection{Header: "Authorization", Scheme: "Bearer"}}

	cases := []struct {
		name   string
		method string
		path   string
		body   []byte
	}{
		{"absolute URL in path", http.MethodGet, "https://evil.example/steal", nil},
		{"protocol-relative path", http.MethodGet, "//evil.example/steal", nil},
		{"traversal", http.MethodGet, "/v2/../admin", nil},
		{"relative path without slash", http.MethodGet, "v2/records", nil},
		{"method not allowed", http.MethodTrace, "/v2/records", nil},
		{"oversized body", http.MethodPost, "/v2/records", make([]byte, MaxConnectionProxyRequestBytes+1)},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := svc.proxyWithCredential(context.Background(), provider, "sk-test",
				ConnectionProxyRequest{Method: tc.method, Path: tc.path, Body: tc.body})
			if err == nil {
				t.Fatal("unsafe proxy request was accepted")
			}
			if dialed {
				t.Fatal("the host dialed out before validating the request")
			}
		})
	}
}

func TestProxyWithCredential_RejectsOversizedProviderResponse(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(make([]byte, MaxConnectionProxyResponseBytes+1))
	}))
	defer server.Close()

	svc := &ConnectionService{
		HTTPClient: server.Client(),
		validateEndpoint: func(_ context.Context, raw string, _ []string) (*url.URL, error) {
			return url.Parse(raw)
		},
	}
	provider := ConnectionProvider{Key: "test", BaseURL: server.URL}
	_, err := svc.proxyWithCredential(context.Background(), provider, "sk-test", ConnectionProxyRequest{Method: http.MethodGet, Path: "/big"})
	if err == nil {
		t.Fatal("an oversized provider response was accepted instead of failing closed")
	}
}

func TestProxyRequest_RefusesReferenceModeAndProvidersWithoutBaseURL(t *testing.T) {
	svc := &ConnectionService{}
	_, err := svc.ProxyRequest(context.Background(), connectionFixture("attio", ConnectionSecretModeReference),
		ConnectionProxyRequest{Method: http.MethodGet, Path: "/v2/self"})
	if err == nil {
		t.Fatal("reference-mode connection was proxied; the host cannot read an operator-owned value")
	}
	_, err = svc.ProxyRequest(context.Background(), connectionFixture("generic-api-key", ConnectionSecretModeHost),
		ConnectionProxyRequest{Method: http.MethodGet, Path: "/anything"})
	if err == nil {
		t.Fatal("a provider without a base URL was proxied")
	}
}
