package service

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/internal/util/secretbox"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
	"github.com/multica-ai/multica/server/pkg/plugincontract"
	"github.com/multica-ai/multica/server/pkg/remotemcp"
)

// Connection ownership levels. Instance connections are deployment-owned and
// carry no workspace; workspace connections belong to exactly one workspace.
const (
	ConnectionLevelInstance  = "instance"
	ConnectionLevelWorkspace = "workspace"
)

// Secret modes. Host means the deployment stores the encrypted value; reference
// means it stores only a pointer to an operator-owned secret store.
const (
	ConnectionSecretModeHost      = "host"
	ConnectionSecretModeReference = "reference"
)

// Connection consumer types, matching the connection_binding check constraint.
const (
	ConnectionConsumerPluginInstallation = "plugin_installation"
	ConnectionConsumerAgent              = "agent"
	ConnectionConsumerNative             = "native"
)

// Connection status values, matching the connection check constraint.
const (
	ConnectionStatusUnchecked   = "unchecked"
	ConnectionStatusOK          = "ok"
	ConnectionStatusDegraded    = "degraded"
	ConnectionStatusError       = "error"
	ConnectionStatusNeedsReauth = "needs_reauth"
)

// ConnectionErrorKind classifies a failure so the HTTP layer can pick a status
// code without string matching.
type ConnectionErrorKind string

const (
	ConnectionErrorInvalid     ConnectionErrorKind = "invalid"
	ConnectionErrorNotFound    ConnectionErrorKind = "not_found"
	ConnectionErrorConflict    ConnectionErrorKind = "conflict"
	ConnectionErrorForbidden   ConnectionErrorKind = "forbidden"
	ConnectionErrorUnavailable ConnectionErrorKind = "unavailable"
)

type ConnectionError struct {
	Kind    ConnectionErrorKind
	Message string
	Err     error
}

func (e *ConnectionError) Error() string { return e.Message }
func (e *ConnectionError) Unwrap() error { return e.Err }

func connectionErrf(kind ConnectionErrorKind, format string, args ...any) *ConnectionError {
	return &ConnectionError{Kind: kind, Message: fmt.Sprintf(format, args...)}
}

// CredentialInjection says where a resolved credential belongs on an outbound
// request. A provider picks the one shape an API expects; it is never inferred
// from the secret's contents.
type CredentialInjection struct {
	Header string `json:"header,omitempty"`
	Scheme string `json:"scheme,omitempty"`
	Query  string `json:"query,omitempty"`
}

// ConnectionHealthCheck is the read-only request "Test connection" performs.
// The URL is fixed by the provider, never by connection configuration, so a
// tenant cannot point the deployment's credential at an arbitrary host.
type ConnectionHealthCheck struct {
	Method       string `json:"method"`
	URL          string `json:"url"`
	ExpectStatus int    `json:"expect_status"`
}

// ConnectionProvider is a code-defined connector description: what credential
// shape the system uses, which fields the host renders, which hosts it may
// reach, and how a credential is applied to a request.
type ConnectionProvider struct {
	Key          string
	DisplayName  string
	Category     string
	AuthKind     string
	SecretKeys   []string
	Schema       plugincontract.ConfigSchema
	NetDomains   []string
	Injection    CredentialInjection
	Health       *ConnectionHealthCheck
	Capabilities []string
	// BaseURL is the provider's API root. It is fixed by code so a request
	// through the host proxy cannot be pointed at another host; the caller
	// only ever supplies a path. Empty means the provider has no proxyable
	// API surface.
	BaseURL string
}

// BuiltinConnectionProviders is the provider registry this build ships. The
// list is deliberately small: each entry has to be one the deployment's own
// health check and network policy actually support.
func BuiltinConnectionProviders() []ConnectionProvider {
	return []ConnectionProvider{
		{
			Key:         "attio",
			DisplayName: "Attio",
			Category:    "crm",
			AuthKind:    "api_key",
			SecretKeys:  []string{"api_key"},
			NetDomains:  []string{"api.attio.com"},
			Injection:   CredentialInjection{Header: "Authorization", Scheme: "Bearer"},
			Health: &ConnectionHealthCheck{
				Method:       http.MethodGet,
				URL:          "https://api.attio.com/v2/self",
				ExpectStatus: http.StatusOK,
			},
			Capabilities: []string{"attio:read", "attio:write"},
			BaseURL:      "https://api.attio.com",
		},
		{
			Key:         "generic-api-key",
			DisplayName: "Generic API key",
			Category:    "custom",
			AuthKind:    "api_key",
			SecretKeys:  []string{"api_key"},
			// No net: domain and no health check: the host has no endpoint to
			// verify against, so a connection carries metadata and a secret for
			// consumers the deployment trusts, and reports itself unverified.
			Capabilities: []string{"http:invoke"},
		},
	}
}

// ConnectionProviderFor returns one built-in provider by key.
func ConnectionProviderFor(key string) (ConnectionProvider, bool) {
	for _, provider := range BuiltinConnectionProviders() {
		if provider.Key == key {
			return provider, true
		}
	}
	return ConnectionProvider{}, false
}

// NewConnectionService constructs the connection service. Secrets are wired
// separately at boot: a deployment without a key still lists connections, but
// host-mode secret writes and probes fail closed.
func NewConnectionService(queries *db.Queries) *ConnectionService {
	return &ConnectionService{Queries: queries}
}

// ConnectionService owns connection CRUD, write-only secrets, bindings and the
// "Test connection" probe.
type ConnectionService struct {
	Queries *db.Queries
	// Secrets encrypts host-mode secret values. Nil disables host-mode secret
	// writes so a deployment without a key fails closed instead of storing
	// plaintext.
	Secrets *secretbox.Box
	// HTTPClient overrides the outbound probe client in tests. Production uses
	// the SSRF-guarded client built per endpoint.
	HTTPClient *http.Client
	// validateEndpoint is the SSRF guard applied before any outbound request
	// (probe or proxy). It is a field so a test can point it at a local
	// server; nil uses the same public-HTTPS validation the hook transport
	// uses.
	validateEndpoint func(ctx context.Context, raw string, allowedHosts []string) (*url.URL, error)
}

// validateOutboundEndpoint applies the configured guard, defaulting to the
// shared public-HTTPS validator. Both the health probe and the host proxy run
// through it, so neither can reach a host the provider did not declare.
func (s *ConnectionService) validateOutboundEndpoint(ctx context.Context, raw string, allowedHosts []string) (*url.URL, error) {
	if s.validateEndpoint != nil {
		return s.validateEndpoint(ctx, raw, allowedHosts)
	}
	return remotemcp.ValidatePublicHTTPSEndpoint(ctx, raw, allowedHosts, nil)
}

// ConnectionCreateInput is everything an administrator supplies when creating a
// connection. Level and ProviderKey are validated against the registry; the
// workspace id is taken from the request context, never the body, for
// workspace-level connections.
type ConnectionCreateInput struct {
	Level       string
	WorkspaceID pgtype.UUID
	ProviderKey string
	Name        string
	Environment string
	Config      map[string]any
	SecretMode  string
	CreatedBy   pgtype.UUID
}

// CreateConnection inserts a connection after validating the provider, the
// config against the provider schema, and the secret mode. The secret itself is
// written separately so a failed secret write cannot leave a connection that
// claims to hold one.
func (s *ConnectionService) CreateConnection(ctx context.Context, input ConnectionCreateInput) (db.Connection, error) {
	provider, ok := ConnectionProviderFor(input.ProviderKey)
	if !ok {
		return db.Connection{}, connectionErrf(ConnectionErrorInvalid, "unknown connection provider %q", input.ProviderKey)
	}
	switch input.Level {
	case ConnectionLevelWorkspace:
		if !input.WorkspaceID.Valid {
			return db.Connection{}, connectionErrf(ConnectionErrorInvalid, "workspace connection requires a workspace")
		}
	case ConnectionLevelInstance:
		return db.Connection{}, connectionErrf(ConnectionErrorForbidden, "instance connections are seeded by the deployment, not created here")
	default:
		return db.Connection{}, connectionErrf(ConnectionErrorInvalid, "unsupported connection level %q", input.Level)
	}
	if strings.TrimSpace(input.Name) == "" || len(input.Name) > 160 {
		return db.Connection{}, connectionErrf(ConnectionErrorInvalid, "name must be between 1 and 160 bytes")
	}
	environment := input.Environment
	if environment == "" {
		environment = "production"
	}
	if environment != "production" && environment != "fixture" {
		return db.Connection{}, connectionErrf(ConnectionErrorInvalid, "environment must be production or fixture")
	}
	secretMode := input.SecretMode
	if secretMode == "" {
		secretMode = ConnectionSecretModeHost
	}
	if secretMode != ConnectionSecretModeHost && secretMode != ConnectionSecretModeReference {
		return db.Connection{}, connectionErrf(ConnectionErrorInvalid, "secret_mode must be host or reference")
	}
	config, err := validateConnectionConfig(provider, input.Config)
	if err != nil {
		return db.Connection{}, err
	}
	return s.Queries.CreateConnection(ctx, db.CreateConnectionParams{
		Level:       input.Level,
		WorkspaceID: input.WorkspaceID,
		ProviderKey: provider.Key,
		Name:        strings.TrimSpace(input.Name),
		Environment: environment,
		Config:      config,
		SecretMode:  secretMode,
		CreatedBy:   input.CreatedBy,
	})
}

// validateConnectionConfig serializes the non-secret configuration and checks
// it against the provider's schema. Unknown keys and type mismatches are
// rejected rather than stored: the form is host-rendered from the same schema,
// so a mismatch means the caller bypassed the form.
func validateConnectionConfig(provider ConnectionProvider, config map[string]any) ([]byte, error) {
	if config == nil {
		config = map[string]any{}
	}
	if provider.Schema.Len() == 0 {
		if len(config) > 0 {
			return nil, connectionErrf(ConnectionErrorInvalid, "provider %q accepts no configuration fields", provider.Key)
		}
		return []byte("{}"), nil
	}
	for key, value := range config {
		field, ok := provider.Schema.Field(key)
		if !ok {
			return nil, connectionErrf(ConnectionErrorInvalid, "unknown configuration field %q", key)
		}
		switch field.Type {
		case plugincontract.ConfigString:
			if _, ok := value.(string); !ok {
				return nil, connectionErrf(ConnectionErrorInvalid, "configuration field %q must be a string", key)
			}
		case plugincontract.ConfigNumber:
			if _, ok := value.(float64); !ok {
				return nil, connectionErrf(ConnectionErrorInvalid, "configuration field %q must be a number", key)
			}
		case plugincontract.ConfigBool:
			if _, ok := value.(bool); !ok {
				return nil, connectionErrf(ConnectionErrorInvalid, "configuration field %q must be a boolean", key)
			}
		case plugincontract.ConfigEnum:
			text, ok := value.(string)
			if !ok {
				return nil, connectionErrf(ConnectionErrorInvalid, "configuration field %q must be a string", key)
			}
			allowed := false
			for _, option := range field.Options {
				if option == text {
					allowed = true
					break
				}
			}
			if !allowed {
				return nil, connectionErrf(ConnectionErrorInvalid, "configuration field %q has an unknown option", key)
			}
		case plugincontract.ConfigSecret:
			return nil, connectionErrf(ConnectionErrorInvalid, "configuration field %q must be stored as a secret", key)
		}
	}
	for _, field := range provider.Schema.Fields {
		if !field.Required {
			continue
		}
		if _, ok := config[field.Key]; !ok {
			return nil, connectionErrf(ConnectionErrorInvalid, "configuration field %q is required", field.Key)
		}
	}
	encoded, err := json.Marshal(config)
	if err != nil {
		return nil, connectionErrf(ConnectionErrorInvalid, "configuration could not be encoded")
	}
	return encoded, nil
}

// GetWorkspaceConnection loads one connection scoped to its workspace.
func (s *ConnectionService) GetWorkspaceConnection(ctx context.Context, workspaceID, connectionID pgtype.UUID) (db.Connection, error) {
	connection, err := s.Queries.GetWorkspaceConnection(ctx, db.GetWorkspaceConnectionParams{
		WorkspaceID: workspaceID,
		ID:          connectionID,
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return db.Connection{}, connectionErrf(ConnectionErrorNotFound, "connection not found")
	}
	if err != nil {
		return db.Connection{}, err
	}
	return connection, nil
}

// ListWorkspaceConnections returns the workspace's own connections plus the
// deployment's instance connections, which every workspace may use read-only.
func (s *ConnectionService) ListWorkspaceConnections(ctx context.Context, workspaceID pgtype.UUID) ([]db.Connection, error) {
	workspace, err := s.Queries.ListWorkspaceConnections(ctx, workspaceID)
	if err != nil {
		return nil, err
	}
	instance, err := s.Queries.ListInstanceConnections(ctx)
	if err != nil {
		return nil, err
	}
	return append(workspace, instance...), nil
}

// UpdateConnectionInput is the mutable half of a connection. The provider is
// fixed at creation: changing it would silently reinterpret the stored secret.
type UpdateConnectionInput struct {
	Name             string
	Environment      string
	Config           map[string]any
	SecretMode       string
	ExpectedRevision int64
	UpdatedBy        pgtype.UUID
}

// UpdateConnection applies a revision-checked update. A stale revision returns
// Conflict so the caller re-reads instead of overwriting a newer edit.
func (s *ConnectionService) UpdateConnection(ctx context.Context, connection db.Connection, input UpdateConnectionInput) (db.Connection, error) {
	if connection.Level != ConnectionLevelWorkspace {
		return db.Connection{}, connectionErrf(ConnectionErrorForbidden, "instance connections are read-only")
	}
	provider, ok := ConnectionProviderFor(connection.ProviderKey)
	if !ok {
		return db.Connection{}, connectionErrf(ConnectionErrorUnavailable, "provider %q is not available", connection.ProviderKey)
	}
	if strings.TrimSpace(input.Name) == "" || len(input.Name) > 160 {
		return db.Connection{}, connectionErrf(ConnectionErrorInvalid, "name must be between 1 and 160 bytes")
	}
	environment := input.Environment
	if environment == "" {
		environment = connection.Environment
	}
	if environment != "production" && environment != "fixture" {
		return db.Connection{}, connectionErrf(ConnectionErrorInvalid, "environment must be production or fixture")
	}
	secretMode := input.SecretMode
	if secretMode == "" {
		secretMode = connection.SecretMode
	}
	if secretMode != ConnectionSecretModeHost && secretMode != ConnectionSecretModeReference {
		return db.Connection{}, connectionErrf(ConnectionErrorInvalid, "secret_mode must be host or reference")
	}
	if input.ExpectedRevision <= 0 {
		return db.Connection{}, connectionErrf(ConnectionErrorInvalid, "expected_revision is required")
	}
	config, err := validateConnectionConfig(provider, input.Config)
	if err != nil {
		return db.Connection{}, err
	}
	updated, err := s.Queries.UpdateConnection(ctx, db.UpdateConnectionParams{
		ID:          connection.ID,
		Revision:    input.ExpectedRevision,
		Name:        strings.TrimSpace(input.Name),
		Environment: environment,
		Config:      config,
		SecretMode:  secretMode,
		UpdatedBy:   input.UpdatedBy,
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return db.Connection{}, connectionErrf(ConnectionErrorConflict, "connection changed since it was read")
	}
	if err != nil {
		return db.Connection{}, err
	}
	return updated, nil
}

// SetEnabled flips a workspace connection on or off.
func (s *ConnectionService) SetEnabled(ctx context.Context, connection db.Connection, enabled bool, updatedBy pgtype.UUID) (db.Connection, error) {
	if connection.Level != ConnectionLevelWorkspace {
		return db.Connection{}, connectionErrf(ConnectionErrorForbidden, "instance connections are read-only")
	}
	return s.Queries.SetConnectionEnabled(ctx, db.SetConnectionEnabledParams{
		ID:        connection.ID,
		Enabled:   enabled,
		UpdatedBy: updatedBy,
	})
}

// DeleteConnection removes a workspace connection together with its secrets and
// bindings. Consumers holding it lose access on the next read.
func (s *ConnectionService) DeleteConnection(ctx context.Context, connection db.Connection) error {
	if connection.Level != ConnectionLevelWorkspace {
		return connectionErrf(ConnectionErrorForbidden, "instance connections are read-only")
	}
	if err := s.Queries.DeleteConnectionBindings(ctx, connection.ID); err != nil {
		return err
	}
	if err := s.Queries.DeleteConnectionSecrets(ctx, connection.ID); err != nil {
		return err
	}
	return s.Queries.DeleteConnection(ctx, connection.ID)
}

// SetSecret writes one secret value. Host mode encrypts it; reference mode
// stores only the operator's pointer. A reference value is a name, never a
// secret, which is what makes it readable back to administrators.
func (s *ConnectionService) SetSecret(ctx context.Context, connection db.Connection, key, value string) error {
	provider, ok := ConnectionProviderFor(connection.ProviderKey)
	if !ok {
		return connectionErrf(ConnectionErrorUnavailable, "provider %q is not available", connection.ProviderKey)
	}
	if !connectionSecretKeyAllowed(provider, key) {
		return connectionErrf(ConnectionErrorInvalid, "provider %q has no secret field %q", provider.Key, key)
	}
	if strings.TrimSpace(value) == "" {
		return connectionErrf(ConnectionErrorInvalid, "secret value must not be empty")
	}
	switch connection.SecretMode {
	case ConnectionSecretModeHost:
		if s.Secrets == nil {
			return connectionErrf(ConnectionErrorUnavailable, "connection secrets are disabled: no connection secret key is configured")
		}
		ciphertext, err := s.Secrets.Seal([]byte(value))
		if err != nil {
			return err
		}
		_, err = s.Queries.UpsertConnectionSecret(ctx, db.UpsertConnectionSecretParams{
			ConnectionID: connection.ID,
			Key:          key,
			Ciphertext:   ciphertext,
			Reference:    "",
		})
		return err
	case ConnectionSecretModeReference:
		if len(value) > 1024 {
			return connectionErrf(ConnectionErrorInvalid, "secret reference must be at most 1024 bytes")
		}
		_, err := s.Queries.UpsertConnectionSecret(ctx, db.UpsertConnectionSecretParams{
			ConnectionID: connection.ID,
			Key:          key,
			Ciphertext:   nil,
			Reference:    value,
		})
		return err
	default:
		return connectionErrf(ConnectionErrorInvalid, "unsupported secret mode %q", connection.SecretMode)
	}
}

// DeleteSecret removes one stored secret or reference.
func (s *ConnectionService) DeleteSecret(ctx context.Context, connection db.Connection, key string) error {
	return s.Queries.DeleteConnectionSecret(ctx, db.DeleteConnectionSecretParams{
		ConnectionID: connection.ID,
		Key:          key,
	})
}

// SecretKeys lists the configured secret field names with rotation timestamps.
// Values and ciphertext are never part of the result.
func (s *ConnectionService) SecretKeys(ctx context.Context, connection db.Connection) ([]db.ListConnectionSecretKeysRow, error) {
	return s.Queries.ListConnectionSecretKeys(ctx, connection.ID)
}

func connectionSecretKeyAllowed(provider ConnectionProvider, key string) bool {
	if len(provider.SecretKeys) == 0 {
		return key == "api_key"
	}
	for _, allowed := range provider.SecretKeys {
		if allowed == key {
			return true
		}
	}
	return false
}

// CheckHealth performs the provider's read-only probe with the stored
// credential and records the outcome. Only host-mode connections can be
// probed: a reference points at a store the host deliberately cannot read.
func (s *ConnectionService) CheckHealth(ctx context.Context, connection db.Connection) (db.Connection, error) {
	provider, ok := ConnectionProviderFor(connection.ProviderKey)
	if !ok {
		return db.Connection{}, connectionErrf(ConnectionErrorUnavailable, "provider %q is not available", connection.ProviderKey)
	}
	if provider.Health == nil {
		return db.Connection{}, connectionErrf(ConnectionErrorInvalid, "provider %q has no health check", provider.Key)
	}
	if connection.SecretMode != ConnectionSecretModeHost {
		return db.Connection{}, connectionErrf(ConnectionErrorInvalid, "reference connections are verified by their secret owner")
	}
	if s.Secrets == nil {
		return db.Connection{}, connectionErrf(ConnectionErrorUnavailable, "connection secrets are disabled: no connection secret key is configured")
	}
	credential, err := s.hostSecretValue(ctx, connection, provider.SecretKeys[0])
	if err != nil {
		return db.Connection{}, err
	}

	endpoint, err := s.validateOutboundEndpoint(ctx, provider.Health.URL, provider.NetDomains)
	if err != nil {
		return db.Connection{}, connectionErrf(ConnectionErrorUnavailable, "health endpoint is not allowed: %v", err)
	}
	status, detail := s.probeHealth(ctx, endpoint, *provider.Health, provider.Injection, credential)
	return s.recordStatus(ctx, connection, status, detail)
}

// probeHealth performs the probe and maps the outcome onto a status. It is
// separate from CheckHealth so the mapping and credential injection are
// testable against a local server without weakening the SSRF guard on the real
// path.
func (s *ConnectionService) probeHealth(ctx context.Context, endpoint *url.URL, health ConnectionHealthCheck, injection CredentialInjection, credential string) (string, string) {
	client := s.HTTPClient
	if client == nil {
		client = remotemcp.NewSecureHTTPClient(endpoint)
	}
	probeCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	request, err := http.NewRequestWithContext(probeCtx, health.Method, endpoint.String(), nil)
	if err != nil {
		return ConnectionStatusError, "health request could not be built"
	}
	applyConnectionCredential(request, injection, credential)

	response, err := client.Do(request)
	if err != nil {
		// The transport error is reported without the URL or credential: a
		// misconfigured hostname must not become a way to read the secret back
		// out of the status detail.
		return ConnectionStatusError, "connection attempt failed"
	}
	defer response.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, 4096))
	if health.ExpectStatus != 0 && response.StatusCode != health.ExpectStatus {
		if response.StatusCode == http.StatusUnauthorized || response.StatusCode == http.StatusForbidden {
			return ConnectionStatusNeedsReauth, fmt.Sprintf("provider rejected the credential (status %d)", response.StatusCode)
		}
		return ConnectionStatusError, fmt.Sprintf("unexpected status %d", response.StatusCode)
	}
	return ConnectionStatusOK, ""
}

func (s *ConnectionService) recordStatus(ctx context.Context, connection db.Connection, status, detail string) (db.Connection, error) {
	return s.Queries.SetConnectionStatus(ctx, db.SetConnectionStatusParams{
		ID:            connection.ID,
		Status:        status,
		StatusDetail:  detail,
		LastCheckedAt: pgtype.Timestamptz{Time: time.Now(), Valid: true},
	})
}

// hostSecretValue decrypts one host-mode secret. It is the only path that
// turns stored ciphertext back into a value, and it never leaves the server:
// callers are the health probe and, once the host proxy lands, the outbound
// request builder. Nothing serializes its result.
func (s *ConnectionService) hostSecretValue(ctx context.Context, connection db.Connection, key string) (string, error) {
	if s.Secrets == nil {
		return "", connectionErrf(ConnectionErrorUnavailable, "connection secrets are disabled")
	}
	row, err := s.Queries.GetConnectionSecret(ctx, db.GetConnectionSecretParams{
		ConnectionID: connection.ID,
		Key:          key,
	})
	if errors.Is(err, pgx.ErrNoRows) || len(row.Ciphertext) == 0 {
		return "", connectionErrf(ConnectionErrorInvalid, "connection has no stored credential")
	}
	if err != nil {
		return "", err
	}
	plaintext, err := s.Secrets.Open(row.Ciphertext)
	if err != nil {
		return "", connectionErrf(ConnectionErrorUnavailable, "stored credential could not be opened")
	}
	return string(plaintext), nil
}

// applyConnectionCredential places the credential on an outbound request per
// the provider's declared injection. Keeping it in one function means every
// caller (health probe now, host proxy later) applies the same shape.
func applyConnectionCredential(request *http.Request, injection CredentialInjection, credential string) {
	if injection.Query != "" {
		query := request.URL.Query()
		query.Set(injection.Query, credential)
		request.URL.RawQuery = query.Encode()
		return
	}
	header := injection.Header
	if header == "" {
		header = "Authorization"
	}
	if injection.Scheme != "" {
		credential = injection.Scheme + " " + credential
	}
	request.Header.Set(header, credential)
}

// ── Host proxy ──────────────────────────────────────────────────────────────
//
// The proxy is how a plugin uses a credential without ever holding one: the
// plugin names a provider-relative path, the host attaches the stored
// credential and performs the call. Both directions are bounded because this
// is an API relay, not a file channel.

const (
	// MaxConnectionProxyRequestBytes bounds the request body the host will
	// forward on a plugin's behalf.
	MaxConnectionProxyRequestBytes = 256 << 10
	// MaxConnectionProxyResponseBytes bounds what the host will read back. A
	// larger response is an error rather than a silent truncation: a truncated
	// JSON document would parse as corrupt data at the caller.
	MaxConnectionProxyResponseBytes = 1 << 20
	// ConnectionProxyTimeout bounds one proxied call end to end. It is kept
	// below the plugin SDK's 15s bridge timeout so the host always answers the
	// surface with a real error instead of letting the SDK give up first while
	// the outbound call continues in the background.
	ConnectionProxyTimeout = 10 * time.Second
)

var connectionProxyMethods = map[string]bool{
	http.MethodGet:    true,
	http.MethodPost:   true,
	http.MethodPatch:  true,
	http.MethodPut:    true,
	http.MethodDelete: true,
}

// ConnectionProxyRequest is one provider-relative call. The caller supplies a
// path, never a host: the destination is provider.BaseURL plus this path, and
// the SSRF guard still checks the result against the provider's net domains.
type ConnectionProxyRequest struct {
	Method      string
	Path        string
	Query       url.Values
	Body        []byte
	ContentType string
}

// ConnectionProxyResponse is the provider's reply, already bounded.
type ConnectionProxyResponse struct {
	Status      int
	ContentType string
	Body        []byte
}

// ProxyRequest performs one outbound call with the connection's credential. It
// never returns the credential. Reference-mode connections are refused: the
// host deliberately cannot read a value its operator owns, so there is nothing
// to attach.
func (s *ConnectionService) ProxyRequest(ctx context.Context, connection db.Connection, request ConnectionProxyRequest) (ConnectionProxyResponse, error) {
	provider, ok := ConnectionProviderFor(connection.ProviderKey)
	if !ok {
		return ConnectionProxyResponse{}, connectionErrf(ConnectionErrorUnavailable, "provider %q is not available", connection.ProviderKey)
	}
	if provider.BaseURL == "" {
		return ConnectionProxyResponse{}, connectionErrf(ConnectionErrorInvalid, "provider %q has no API base URL", provider.Key)
	}
	if connection.SecretMode != ConnectionSecretModeHost {
		return ConnectionProxyResponse{}, connectionErrf(ConnectionErrorForbidden, "reference connections are resolved by their owner, not the host")
	}
	credential, err := s.hostSecretValue(ctx, connection, provider.SecretKeys[0])
	if err != nil {
		return ConnectionProxyResponse{}, err
	}
	return s.proxyWithCredential(ctx, provider, credential, request)
}

// proxyWithCredential validates one provider-relative request and performs it
// with the credential attached. Separated from ProxyRequest so the validation,
// credential injection and response bounds are testable without a database row
// backing the credential — every caller still passes through the same checks.
func (s *ConnectionService) proxyWithCredential(ctx context.Context, provider ConnectionProvider, credential string, request ConnectionProxyRequest) (ConnectionProxyResponse, error) {
	method := strings.ToUpper(strings.TrimSpace(request.Method))
	if !connectionProxyMethods[method] {
		return ConnectionProxyResponse{}, connectionErrf(ConnectionErrorInvalid, "method %q is not allowed", request.Method)
	}
	if len(request.Body) > MaxConnectionProxyRequestBytes {
		return ConnectionProxyResponse{}, connectionErrf(ConnectionErrorInvalid, "request body exceeds %d bytes", MaxConnectionProxyRequestBytes)
	}
	path := request.Path
	if path == "" {
		path = "/"
	}
	if !strings.HasPrefix(path, "/") || strings.HasPrefix(path, "//") || strings.Contains(path, "://") || strings.ContainsAny(path, "\r\n") {
		return ConnectionProxyResponse{}, connectionErrf(ConnectionErrorInvalid, "path must be relative to the provider base URL")
	}
	for _, segment := range strings.Split(path, "/") {
		if segment == ".." {
			return ConnectionProxyResponse{}, connectionErrf(ConnectionErrorInvalid, "path must not contain traversal")
		}
	}

	target := strings.TrimRight(provider.BaseURL, "/") + path
	if len(request.Query) > 0 {
		target += "?" + request.Query.Encode()
	}
	endpoint, err := s.validateOutboundEndpoint(ctx, target, provider.NetDomains)
	if err != nil {
		return ConnectionProxyResponse{}, connectionErrf(ConnectionErrorForbidden, "request target is not allowed")
	}
	client := s.HTTPClient
	if client == nil {
		client = remotemcp.NewSecureHTTPClient(endpoint)
	}
	proxyCtx, cancel := context.WithTimeout(ctx, ConnectionProxyTimeout)
	defer cancel()
	var body io.Reader
	if len(request.Body) > 0 {
		body = bytes.NewReader(request.Body)
	}
	outbound, err := http.NewRequestWithContext(proxyCtx, method, endpoint.String(), body)
	if err != nil {
		return ConnectionProxyResponse{}, connectionErrf(ConnectionErrorUnavailable, "provider request could not be built")
	}
	if len(request.Body) > 0 {
		contentType := request.ContentType
		if contentType == "" {
			contentType = "application/json"
		}
		outbound.Header.Set("Content-Type", contentType)
	}
	outbound.Header.Set("Accept", "application/json")
	applyConnectionCredential(outbound, provider.Injection, credential)

	response, err := client.Do(outbound)
	if err != nil {
		// Without the URL or credential: the provider's own error is not a
		// channel for reading deployment details back out.
		return ConnectionProxyResponse{}, connectionErrf(ConnectionErrorUnavailable, "provider request failed")
	}
	defer response.Body.Close()
	payload, err := io.ReadAll(io.LimitReader(response.Body, MaxConnectionProxyResponseBytes+1))
	if err != nil {
		return ConnectionProxyResponse{}, connectionErrf(ConnectionErrorUnavailable, "provider response could not be read")
	}
	if len(payload) > MaxConnectionProxyResponseBytes {
		return ConnectionProxyResponse{}, connectionErrf(ConnectionErrorUnavailable, "provider response exceeds %d bytes", MaxConnectionProxyResponseBytes)
	}
	return ConnectionProxyResponse{
		Status:      response.StatusCode,
		ContentType: response.Header.Get("Content-Type"),
		Body:        payload,
	}, nil
}

// ListConnectionsForConsumer returns the enabled bindings' connections in one
// workspace, for a consumer that needs to know what it may use. It carries no
// credential material.
func (s *ConnectionService) ListConnectionsForConsumer(ctx context.Context, consumerType string, consumerID pgtype.UUID, workspaceID pgtype.UUID) ([]db.Connection, error) {
	bindings, err := s.Queries.ListConsumerConnectionBindings(ctx, db.ListConsumerConnectionBindingsParams{
		ConsumerType: consumerType,
		ConsumerID:   consumerID,
	})
	if err != nil {
		return nil, err
	}
	out := make([]db.Connection, 0, len(bindings))
	for _, binding := range bindings {
		connection, err := s.Queries.GetConnection(ctx, binding.ConnectionID)
		if errors.Is(err, pgx.ErrNoRows) {
			continue
		}
		if err != nil {
			return nil, err
		}
		// Instance connections serve every workspace; a workspace connection
		// must actually be in the consumer's workspace.
		if connection.WorkspaceID.Valid && connection.WorkspaceID != workspaceID {
			continue
		}
		out = append(out, connection)
	}
	return out, nil
}

// ── Bindings ────────────────────────────────────────────────────────────────

// BindConsumer grants one consumer the use of a connection. Binding a consumer
// that already has a binding re-enables it rather than creating a second row.
func (s *ConnectionService) BindConsumer(ctx context.Context, connection db.Connection, consumerType string, consumerID, createdBy pgtype.UUID) (db.ConnectionBinding, error) {
	if !validConnectionConsumerType(consumerType) {
		return db.ConnectionBinding{}, connectionErrf(ConnectionErrorInvalid, "unsupported consumer type %q", consumerType)
	}
	if !consumerID.Valid {
		return db.ConnectionBinding{}, connectionErrf(ConnectionErrorInvalid, "consumer id is required")
	}
	return s.Queries.CreateConnectionBinding(ctx, db.CreateConnectionBindingParams{
		ConnectionID: connection.ID,
		ConsumerType: consumerType,
		ConsumerID:   consumerID,
		CreatedBy:    createdBy,
	})
}

// UnbindConsumer revokes the grant. The connection and its secret stay.
func (s *ConnectionService) UnbindConsumer(ctx context.Context, connection db.Connection, consumerType string, consumerID pgtype.UUID) error {
	return s.Queries.DeleteConnectionBinding(ctx, db.DeleteConnectionBindingParams{
		ConnectionID: connection.ID,
		ConsumerType: consumerType,
		ConsumerID:   consumerID,
	})
}

// ListBindings returns who may currently use the connection.
func (s *ConnectionService) ListBindings(ctx context.Context, connection db.Connection) ([]db.ConnectionBinding, error) {
	return s.Queries.ListConnectionBindings(ctx, connection.ID)
}

// AuthorizeConsumer resolves the grant a consumer presents: the connection must
// exist, be enabled, and carry an enabled binding for exactly this consumer.
// It returns the connection so the caller can proceed to a provider-specific
// use without a second read.
func (s *ConnectionService) AuthorizeConsumer(ctx context.Context, connectionID pgtype.UUID, consumerType string, consumerID pgtype.UUID) (db.Connection, error) {
	if !validConnectionConsumerType(consumerType) {
		return db.Connection{}, connectionErrf(ConnectionErrorInvalid, "unsupported consumer type %q", consumerType)
	}
	connection, err := s.Queries.GetConnection(ctx, connectionID)
	if errors.Is(err, pgx.ErrNoRows) {
		return db.Connection{}, connectionErrf(ConnectionErrorNotFound, "connection not found")
	}
	if err != nil {
		return db.Connection{}, err
	}
	if !connection.Enabled {
		return db.Connection{}, connectionErrf(ConnectionErrorForbidden, "connection is disabled")
	}
	bindings, err := s.Queries.ListConsumerConnectionBindings(ctx, db.ListConsumerConnectionBindingsParams{
		ConsumerType: consumerType,
		ConsumerID:   consumerID,
	})
	if err != nil {
		return db.Connection{}, err
	}
	for _, binding := range bindings {
		if binding.ConnectionID == connection.ID {
			return connection, nil
		}
	}
	return db.Connection{}, connectionErrf(ConnectionErrorForbidden, "connection is not granted to this consumer")
}

// ResolveReference returns the operator-owned secret pointer for a reference
// connection. Only the name travels; the value never enters this process. It is
// how an external worker with its own vault access learns which vault entry the
// workspace approved, instead of hardcoding one.
func (s *ConnectionService) ResolveReference(ctx context.Context, connection db.Connection, key string) (string, error) {
	if connection.SecretMode != ConnectionSecretModeReference {
		return "", connectionErrf(ConnectionErrorForbidden, "connection does not use an external secret reference")
	}
	row, err := s.Queries.GetConnectionSecret(ctx, db.GetConnectionSecretParams{
		ConnectionID: connection.ID,
		Key:          key,
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return "", connectionErrf(ConnectionErrorNotFound, "connection has no secret reference")
	}
	if err != nil {
		return "", err
	}
	if row.Reference == "" {
		return "", connectionErrf(ConnectionErrorNotFound, "connection has no secret reference")
	}
	return row.Reference, nil
}

// HostSecretForConsumer authorizes a consumer, then decrypts one host-mode
// secret for server-side use. The plaintext is for outbound calls; it must not
// be logged or serialized into a response.
func (s *ConnectionService) HostSecretForConsumer(ctx context.Context, connectionID pgtype.UUID, consumerType string, consumerID pgtype.UUID, key string) (string, error) {
	connection, err := s.AuthorizeConsumer(ctx, connectionID, consumerType, consumerID)
	if err != nil {
		return "", err
	}
	return s.hostSecretValue(ctx, connection, key)
}

func validConnectionConsumerType(consumerType string) bool {
	switch consumerType {
	case ConnectionConsumerPluginInstallation, ConnectionConsumerAgent, ConnectionConsumerNative:
		return true
	default:
		return false
	}
}
