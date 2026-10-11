package handler

import (
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/internal/featureflags"
	"github.com/multica-ai/multica/server/internal/service"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
	"github.com/multica-ai/multica/server/pkg/plugincontract"
)

// ── Response shapes ─────────────────────────────────────────────────────────

// ConnectionProviderResponse is the host-rendered provider description. The
// configuration schema is not a secret and is exactly what the settings form
// renders from, so it travels in full.
type ConnectionProviderResponse struct {
	Key          string                      `json:"key"`
	DisplayName  string                      `json:"display_name"`
	Category     string                      `json:"category"`
	AuthKind     string                      `json:"auth_kind"`
	SecretKeys   []string                    `json:"secret_keys"`
	ConfigSchema plugincontract.ConfigSchema `json:"config_schema"`
	NetDomains   []string                    `json:"net_domains"`
	Capabilities []string                    `json:"capabilities"`
	HasHealth    bool                        `json:"has_health"`
}

// ConnectionBindingResponse names one consumer that may use the connection.
type ConnectionBindingResponse struct {
	ID           string `json:"id"`
	ConsumerType string `json:"consumer_type"`
	ConsumerID   string `json:"consumer_id"`
	Enabled      bool   `json:"enabled"`
	CreatedAt    string `json:"created_at"`
}

// ConnectionResponse is the complete non-secret shape of a connection.
//
// Never add a secret value, ciphertext, or reference to this struct: the
// settings UI renders it to administrators, and `configured_secrets` carries
// names only. A reference name is deliberately part of the *reference* mode
// contract and travels separately, because it is a pointer an operator pastes
// into a form, not a credential.
type ConnectionResponse struct {
	ID                string          `json:"id"`
	Level             string          `json:"level"`
	WorkspaceID       *string         `json:"workspace_id,omitempty"`
	ProviderKey       string          `json:"provider_key"`
	Name              string          `json:"name"`
	Environment       string          `json:"environment"`
	Config            json.RawMessage `json:"config"`
	SecretMode        string          `json:"secret_mode"`
	ConfiguredSecrets []string        `json:"configured_secrets"`
	Status            string          `json:"status"`
	StatusDetail      string          `json:"status_detail"`
	LastCheckedAt     *string         `json:"last_checked_at,omitempty"`
	Enabled           bool            `json:"enabled"`
	Revision          int64           `json:"revision"`
	ReadOnly          bool            `json:"read_only"`
	CreatedAt         string          `json:"created_at"`
	UpdatedAt         string          `json:"updated_at"`
}

// ── Helpers ─────────────────────────────────────────────────────────────────

// connectionsDisabledForRequest gates every connections endpoint on the
// deployment flag. An undeclared flag reads as enabled, matching how the other
// plugin surfaces treat a flag an older deployment has not declared yet; an
// operator turns the feature off by declaring connections_v1=false.
func (h *Handler) connectionsDisabledForRequest(w http.ResponseWriter, r *http.Request) bool {
	if decision, ok := h.FeatureFlags.LookupLayer(r.Context(), featureflags.ConnectionsV1, true); !ok || decision.Enabled {
		return false
	}
	writeFeatureDisabled(w, "connections_disabled", "Connections are not enabled on this deployment")
	return true
}

func connectionProviderResponse(provider service.ConnectionProvider) ConnectionProviderResponse {
	secretKeys := provider.SecretKeys
	if len(secretKeys) == 0 {
		secretKeys = []string{"api_key"}
	}
	netDomains := provider.NetDomains
	if netDomains == nil {
		netDomains = []string{}
	}
	capabilities := provider.Capabilities
	if capabilities == nil {
		capabilities = []string{}
	}
	return ConnectionProviderResponse{
		Key:          provider.Key,
		DisplayName:  provider.DisplayName,
		Category:     provider.Category,
		AuthKind:     provider.AuthKind,
		SecretKeys:   secretKeys,
		ConfigSchema: provider.Schema,
		NetDomains:   netDomains,
		Capabilities: capabilities,
		HasHealth:    provider.Health != nil,
	}
}

func (h *Handler) connectionToResponse(r *http.Request, connection db.Connection, canManage bool) ConnectionResponse {
	configured := []string{}
	if keys, err := h.ConnectionService.SecretKeys(r.Context(), connection); err == nil {
		for _, row := range keys {
			configured = append(configured, row.Key)
		}
	}
	config := connection.Config
	if len(config) == 0 {
		config = []byte("{}")
	}
	out := ConnectionResponse{
		ID:                uuidToString(connection.ID),
		Level:             connection.Level,
		ProviderKey:       connection.ProviderKey,
		Name:              connection.Name,
		Environment:       connection.Environment,
		Config:            json.RawMessage(config),
		SecretMode:        connection.SecretMode,
		ConfiguredSecrets: configured,
		Status:            connection.Status,
		StatusDetail:      connection.StatusDetail,
		Enabled:           connection.Enabled,
		Revision:          connection.Revision,
		ReadOnly:          connection.Level == service.ConnectionLevelInstance || !canManage,
		CreatedAt:         timestampToString(connection.CreatedAt),
		UpdatedAt:         timestampToString(connection.UpdatedAt),
	}
	if connection.WorkspaceID.Valid {
		workspaceID := uuidToString(connection.WorkspaceID)
		out.WorkspaceID = &workspaceID
	}
	if connection.LastCheckedAt.Valid {
		lastChecked := timestampToString(connection.LastCheckedAt)
		out.LastCheckedAt = &lastChecked
	}
	return out
}

func writeConnectionError(w http.ResponseWriter, err error) {
	var connErr *service.ConnectionError
	if !errors.As(err, &connErr) {
		slog.Error("connection operation failed", "error", err)
		writeError(w, http.StatusInternalServerError, "connection operation failed")
		return
	}
	switch connErr.Kind {
	case service.ConnectionErrorInvalid:
		writeError(w, http.StatusBadRequest, connErr.Message)
	case service.ConnectionErrorNotFound:
		writeError(w, http.StatusNotFound, connErr.Message)
	case service.ConnectionErrorConflict:
		writeError(w, http.StatusConflict, connErr.Message)
	case service.ConnectionErrorForbidden:
		writeError(w, http.StatusForbidden, connErr.Message)
	case service.ConnectionErrorUnavailable:
		writeError(w, http.StatusServiceUnavailable, connErr.Message)
	default:
		slog.Error("connection operation failed", "error", err)
		writeError(w, http.StatusInternalServerError, "connection operation failed")
	}
}

// loadWorkspaceConnection parses the workspace and connection ids, checks
// membership, and returns the connection together with the caller's manage
// right. It writes the error response itself when anything fails.
func (h *Handler) loadWorkspaceConnection(w http.ResponseWriter, r *http.Request) (db.Connection, db.Member, pgtype.UUID, bool) {
	workspaceID := workspaceIDFromURL(r, "id")
	wsUUID, ok := parseUUIDOrBadRequest(w, workspaceID, "workspace id")
	if !ok {
		return db.Connection{}, db.Member{}, pgtype.UUID{}, false
	}
	member, ok := h.workspaceMember(w, r, workspaceID)
	if !ok {
		return db.Connection{}, db.Member{}, pgtype.UUID{}, false
	}
	connectionID, ok := parseUUIDOrBadRequest(w, chi.URLParam(r, "connectionId"), "connection id")
	if !ok {
		return db.Connection{}, db.Member{}, pgtype.UUID{}, false
	}
	connection, err := h.ConnectionService.GetWorkspaceConnection(r.Context(), wsUUID, connectionID)
	if err != nil {
		writeConnectionError(w, err)
		return db.Connection{}, db.Member{}, pgtype.UUID{}, false
	}
	return connection, member, wsUUID, true
}

// ── Handlers ────────────────────────────────────────────────────────────────

// ListConnectionProviders (GET /workspaces/{id}/connections/providers) returns
// the built-in provider registry. Member-visible: the schema is host-rendered
// and contains no secret.
func (h *Handler) ListConnectionProviders(w http.ResponseWriter, r *http.Request) {
	if h.connectionsDisabledForRequest(w, r) {
		return
	}
	providers := service.BuiltinConnectionProviders()
	out := make([]ConnectionProviderResponse, 0, len(providers))
	for _, provider := range providers {
		out = append(out, connectionProviderResponse(provider))
	}
	writeJSON(w, http.StatusOK, map[string]any{"providers": out})
}

// ListConnections (GET /workspaces/{id}/connections) is member-visible: it
// carries configuration and status but no credential material.
func (h *Handler) ListConnections(w http.ResponseWriter, r *http.Request) {
	if h.connectionsDisabledForRequest(w, r) {
		return
	}
	workspaceID := workspaceIDFromURL(r, "id")
	wsUUID, ok := parseUUIDOrBadRequest(w, workspaceID, "workspace id")
	if !ok {
		return
	}
	member, ok := h.workspaceMember(w, r, workspaceID)
	if !ok {
		return
	}
	canManage := roleAllowed(member.Role, "owner", "admin")
	connections, err := h.ConnectionService.ListWorkspaceConnections(r.Context(), wsUUID)
	if err != nil {
		slog.Error("list connections failed", "error", err)
		writeError(w, http.StatusInternalServerError, "failed to list connections")
		return
	}
	out := make([]ConnectionResponse, 0, len(connections))
	for _, connection := range connections {
		out = append(out, h.connectionToResponse(r, connection, canManage))
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"connections": out,
		"can_manage":  canManage,
	})
}

type createConnectionRequest struct {
	ProviderKey string         `json:"provider_key"`
	Name        string         `json:"name"`
	Environment string         `json:"environment"`
	Config      map[string]any `json:"config"`
	SecretMode  string         `json:"secret_mode"`
}

// CreateConnection (POST /workspaces/{id}/connections) is admin-gated by the
// router. The secret is written by a separate call so a connection can exist in
// an unverified state while the administrator pastes the credential.
func (h *Handler) CreateConnection(w http.ResponseWriter, r *http.Request) {
	if h.connectionsDisabledForRequest(w, r) {
		return
	}
	workspaceID := workspaceIDFromURL(r, "id")
	wsUUID, ok := parseUUIDOrBadRequest(w, workspaceID, "workspace id")
	if !ok {
		return
	}
	member, ok := h.workspaceMember(w, r, workspaceID)
	if !ok {
		return
	}
	var req createConnectionRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	connection, err := h.ConnectionService.CreateConnection(r.Context(), service.ConnectionCreateInput{
		Level:       service.ConnectionLevelWorkspace,
		WorkspaceID: wsUUID,
		ProviderKey: req.ProviderKey,
		Name:        req.Name,
		Environment: req.Environment,
		Config:      req.Config,
		SecretMode:  req.SecretMode,
		CreatedBy:   member.UserID,
	})
	if err != nil {
		writeConnectionError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{
		"connection": h.connectionToResponse(r, connection, true),
	})
}

// GetConnection (GET /workspaces/{id}/connections/{connectionId}) is
// member-visible.
func (h *Handler) GetConnection(w http.ResponseWriter, r *http.Request) {
	if h.connectionsDisabledForRequest(w, r) {
		return
	}
	connection, member, _, ok := h.loadWorkspaceConnection(w, r)
	if !ok {
		return
	}
	canManage := roleAllowed(member.Role, "owner", "admin")
	writeJSON(w, http.StatusOK, map[string]any{
		"connection": h.connectionToResponse(r, connection, canManage),
	})
}

type updateConnectionRequest struct {
	Name             string         `json:"name"`
	Environment      string         `json:"environment"`
	Config           map[string]any `json:"config"`
	SecretMode       string         `json:"secret_mode"`
	ExpectedRevision int64          `json:"expected_revision"`
}

// UpdateConnection (PATCH /workspaces/{id}/connections/{connectionId}) is
// admin-gated. The expected revision makes an overwrite a 409.
func (h *Handler) UpdateConnection(w http.ResponseWriter, r *http.Request) {
	if h.connectionsDisabledForRequest(w, r) {
		return
	}
	connection, member, _, ok := h.loadWorkspaceConnection(w, r)
	if !ok {
		return
	}
	var req updateConnectionRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	updated, err := h.ConnectionService.UpdateConnection(r.Context(), connection, service.UpdateConnectionInput{
		Name:             req.Name,
		Environment:      req.Environment,
		Config:           req.Config,
		SecretMode:       req.SecretMode,
		ExpectedRevision: req.ExpectedRevision,
		UpdatedBy:        member.UserID,
	})
	if err != nil {
		writeConnectionError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"connection": h.connectionToResponse(r, updated, true),
	})
}

// DeleteConnection (DELETE /workspaces/{id}/connections/{connectionId}) is
// admin-gated and removes the connection with its secrets and bindings.
func (h *Handler) DeleteConnection(w http.ResponseWriter, r *http.Request) {
	if h.connectionsDisabledForRequest(w, r) {
		return
	}
	connection, _, _, ok := h.loadWorkspaceConnection(w, r)
	if !ok {
		return
	}
	if err := h.ConnectionService.DeleteConnection(r.Context(), connection); err != nil {
		writeConnectionError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

type setConnectionSecretRequest struct {
	Key   string `json:"key"`
	Value string `json:"value"`
}

// SetConnectionSecret (PUT /workspaces/{id}/connections/{connectionId}/secret)
// is admin-gated. Host mode encrypts the value; reference mode stores the
// operator's pointer. The value is never echoed back.
func (h *Handler) SetConnectionSecret(w http.ResponseWriter, r *http.Request) {
	if h.connectionsDisabledForRequest(w, r) {
		return
	}
	connection, _, _, ok := h.loadWorkspaceConnection(w, r)
	if !ok {
		return
	}
	var req setConnectionSecretRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if strings.TrimSpace(req.Key) == "" {
		writeError(w, http.StatusBadRequest, "secret key is required")
		return
	}
	if err := h.ConnectionService.SetSecret(r.Context(), connection, req.Key, req.Value); err != nil {
		writeConnectionError(w, err)
		return
	}
	updated, err := h.ConnectionService.GetWorkspaceConnection(r.Context(), connection.WorkspaceID, connection.ID)
	if err != nil {
		writeConnectionError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"connection": h.connectionToResponse(r, updated, true),
	})
}

// DeleteConnectionSecret (DELETE /workspaces/{id}/connections/{connectionId}/secret/{key})
// is admin-gated.
func (h *Handler) DeleteConnectionSecret(w http.ResponseWriter, r *http.Request) {
	if h.connectionsDisabledForRequest(w, r) {
		return
	}
	connection, _, _, ok := h.loadWorkspaceConnection(w, r)
	if !ok {
		return
	}
	key := chi.URLParam(r, "key")
	if strings.TrimSpace(key) == "" {
		writeError(w, http.StatusBadRequest, "secret key is required")
		return
	}
	if err := h.ConnectionService.DeleteSecret(r.Context(), connection, key); err != nil {
		writeConnectionError(w, err)
		return
	}
	updated, err := h.ConnectionService.GetWorkspaceConnection(r.Context(), connection.WorkspaceID, connection.ID)
	if err != nil {
		writeConnectionError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"connection": h.connectionToResponse(r, updated, true),
	})
}

// TestConnection (POST /workspaces/{id}/connections/{connectionId}/test) is
// admin-gated and performs the provider's read-only probe.
func (h *Handler) TestConnection(w http.ResponseWriter, r *http.Request) {
	if h.connectionsDisabledForRequest(w, r) {
		return
	}
	connection, _, _, ok := h.loadWorkspaceConnection(w, r)
	if !ok {
		return
	}
	checked, err := h.ConnectionService.CheckHealth(r.Context(), connection)
	if err != nil {
		writeConnectionError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"connection": h.connectionToResponse(r, checked, true),
	})
}

// EnableConnection (POST /workspaces/{id}/connections/{connectionId}/enable) is
// admin-gated.
func (h *Handler) EnableConnection(w http.ResponseWriter, r *http.Request) {
	h.setConnectionEnabled(w, r, true)
}

// DisableConnection (POST /workspaces/{id}/connections/{connectionId}/disable)
// is admin-gated.
func (h *Handler) DisableConnection(w http.ResponseWriter, r *http.Request) {
	h.setConnectionEnabled(w, r, false)
}

func (h *Handler) setConnectionEnabled(w http.ResponseWriter, r *http.Request, enabled bool) {
	if h.connectionsDisabledForRequest(w, r) {
		return
	}
	connection, member, _, ok := h.loadWorkspaceConnection(w, r)
	if !ok {
		return
	}
	updated, err := h.ConnectionService.SetEnabled(r.Context(), connection, enabled, member.UserID)
	if err != nil {
		writeConnectionError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"connection": h.connectionToResponse(r, updated, true),
	})
}

// ListConnectionBindings (GET /workspaces/{id}/connections/{connectionId}/bindings)
// is member-visible; it lists consumers, never credentials.
func (h *Handler) ListConnectionBindings(w http.ResponseWriter, r *http.Request) {
	if h.connectionsDisabledForRequest(w, r) {
		return
	}
	connection, _, _, ok := h.loadWorkspaceConnection(w, r)
	if !ok {
		return
	}
	bindings, err := h.ConnectionService.ListBindings(r.Context(), connection)
	if err != nil {
		slog.Error("list connection bindings failed", "error", err)
		writeError(w, http.StatusInternalServerError, "failed to list connection bindings")
		return
	}
	out := make([]ConnectionBindingResponse, 0, len(bindings))
	for _, binding := range bindings {
		out = append(out, ConnectionBindingResponse{
			ID:           uuidToString(binding.ID),
			ConsumerType: binding.ConsumerType,
			ConsumerID:   uuidToString(binding.ConsumerID),
			Enabled:      binding.Enabled,
			CreatedAt:    timestampToString(binding.CreatedAt),
		})
	}
	writeJSON(w, http.StatusOK, map[string]any{"bindings": out})
}

type bindConnectionRequest struct {
	ConsumerType string `json:"consumer_type"`
	ConsumerID   string `json:"consumer_id"`
}

// BindConnection (POST /workspaces/{id}/connections/{connectionId}/bindings) is
// admin-gated. The grant is revocable by unbinding; the connection survives.
func (h *Handler) BindConnection(w http.ResponseWriter, r *http.Request) {
	if h.connectionsDisabledForRequest(w, r) {
		return
	}
	connection, member, _, ok := h.loadWorkspaceConnection(w, r)
	if !ok {
		return
	}
	var req bindConnectionRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	consumerID, ok := parseUUIDOrBadRequest(w, req.ConsumerID, "consumer_id")
	if !ok {
		return
	}
	binding, err := h.ConnectionService.BindConsumer(r.Context(), connection, req.ConsumerType, consumerID, member.UserID)
	if err != nil {
		writeConnectionError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{
		"binding": ConnectionBindingResponse{
			ID:           uuidToString(binding.ID),
			ConsumerType: binding.ConsumerType,
			ConsumerID:   uuidToString(binding.ConsumerID),
			Enabled:      binding.Enabled,
			CreatedAt:    timestampToString(binding.CreatedAt),
		},
	})
}

// UnbindConnection (DELETE /workspaces/{id}/connections/{connectionId}/bindings/{consumerType}/{consumerId})
// is admin-gated.
func (h *Handler) UnbindConnection(w http.ResponseWriter, r *http.Request) {
	if h.connectionsDisabledForRequest(w, r) {
		return
	}
	connection, _, _, ok := h.loadWorkspaceConnection(w, r)
	if !ok {
		return
	}
	consumerID, ok := parseUUIDOrBadRequest(w, chi.URLParam(r, "consumerId"), "consumer id")
	if !ok {
		return
	}
	if err := h.ConnectionService.UnbindConsumer(r.Context(), connection, chi.URLParam(r, "consumerType"), consumerID); err != nil {
		writeConnectionError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
