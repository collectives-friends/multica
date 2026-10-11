package handler

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/url"

	"github.com/go-chi/chi/v5"
	"github.com/multica-ai/multica/server/internal/featureflags"
	"github.com/multica-ai/multica/server/internal/service"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
	"github.com/multica-ai/multica/server/pkg/plugincontract"
	publicapiv1 "github.com/multica-ai/multica/server/pkg/publicapi/v1"
)

// Connection access from the Plugin Action API.
//
// A plugin never receives a credential. `GET /v1/connections` tells it which
// connections it is bound to (metadata only), and
// `POST /v1/connections/{connection_id}/request` asks the host to perform one
// provider-relative call with the stored credential attached. The binding the
// admin created in Settings is the grant for both.

// pluginConnectionDTO is the non-secret shape a plugin may see. Never add a
// secret, ciphertext or reference here.
type pluginConnectionDTO struct {
	ID           string   `json:"id"`
	ProviderKey  string   `json:"provider_key"`
	Name         string   `json:"name"`
	Environment  string   `json:"environment"`
	Status       string   `json:"status"`
	StatusDetail string   `json:"status_detail"`
	Enabled      bool     `json:"enabled"`
	Capabilities []string `json:"capabilities"`
	// HasProxy is false for a provider with no API base URL, so a plugin can
	// hide an action it could never complete.
	HasProxy  bool `json:"has_proxy"`
	HasHealth bool `json:"has_health"`
}

func (h *Handler) pluginConnectionDTO(connection db.Connection) pluginConnectionDTO {
	provider, _ := service.ConnectionProviderFor(connection.ProviderKey)
	return pluginConnectionDTO{
		ID:           uuidToString(connection.ID),
		ProviderKey:  connection.ProviderKey,
		Name:         connection.Name,
		Environment:  connection.Environment,
		Status:       connection.Status,
		StatusDetail: connection.StatusDetail,
		Enabled:      connection.Enabled,
		Capabilities: provider.Capabilities,
		HasProxy:     provider.BaseURL != "",
		HasHealth:    provider.Health != nil,
	}
}

// requireConnectionsV1 gates the plugin-facing connection endpoints on the same
// deployment flag as the settings surface. It writes the Public API problem
// shape, not the App API error shape.
func (h *Handler) requireConnectionsV1(w http.ResponseWriter, r *http.Request) bool {
	if decision, ok := h.FeatureFlags.LookupLayer(r.Context(), featureflags.ConnectionsV1, true); !ok || decision.Enabled {
		return true
	}
	publicapiv1.WriteProblem(w, r, http.StatusForbidden, "connections_disabled", "Connections are not enabled on this deployment")
	return false
}

func writePluginConnectionError(w http.ResponseWriter, r *http.Request, err error) {
	var connectionErr *service.ConnectionError
	if !errors.As(err, &connectionErr) {
		publicapiv1.WriteProblem(w, r, http.StatusInternalServerError, "internal_error", "connection request failed")
		return
	}
	switch connectionErr.Kind {
	case service.ConnectionErrorInvalid:
		publicapiv1.WriteProblem(w, r, http.StatusBadRequest, "invalid_request", connectionErr.Message)
	case service.ConnectionErrorNotFound:
		publicapiv1.WriteProblem(w, r, http.StatusNotFound, "not_found", connectionErr.Message)
	case service.ConnectionErrorConflict:
		publicapiv1.WriteProblem(w, r, http.StatusConflict, "conflict", connectionErr.Message)
	case service.ConnectionErrorForbidden:
		publicapiv1.WriteProblem(w, r, http.StatusForbidden, "forbidden", connectionErr.Message)
	case service.ConnectionErrorUnavailable:
		publicapiv1.WriteProblem(w, r, http.StatusServiceUnavailable, "unavailable", connectionErr.Message)
	default:
		publicapiv1.WriteProblem(w, r, http.StatusInternalServerError, "internal_error", "connection request failed")
	}
}

// ListPluginConnections — GET /v1/connections
//
// The connections bound to the calling installation, with status but no
// credential material. A plugin uses this to show whether the workspace has
// wired it up before offering an action.
func (h *Handler) ListPluginConnections(w http.ResponseWriter, r *http.Request) {
	if !h.requireConnectionsV1(w, r) {
		return
	}
	caller, _, ok := h.pluginCaller(w, r, plugincontract.ScopeConnectionsRead)
	if !ok {
		return
	}
	connections, err := h.ConnectionService.ListConnectionsForConsumer(
		r.Context(),
		service.ConnectionConsumerPluginInstallation,
		caller.Installation.ID,
		caller.WorkspaceID,
	)
	if err != nil {
		publicapiv1.WriteProblem(w, r, http.StatusInternalServerError, "internal_error", "failed to list connections")
		return
	}
	out := make([]pluginConnectionDTO, 0, len(connections))
	for _, connection := range connections {
		out = append(out, h.pluginConnectionDTO(connection))
	}
	writeJSON(w, http.StatusOK, map[string]any{"connections": out})
}

type pluginConnectionProxyRequest struct {
	Method      string            `json:"method"`
	Path        string            `json:"path"`
	Query       map[string]string `json:"query,omitempty"`
	Body        json.RawMessage   `json:"body,omitempty"`
	ContentType string            `json:"content_type,omitempty"`
}

type pluginConnectionProxyResponse struct {
	Status      int    `json:"status"`
	ContentType string `json:"content_type"`
	// Body is a string, not nested JSON: the provider's response is opaque
	// here and may not be JSON at all. The caller parses it if it can.
	Body string `json:"body"`
}

// InvokePluginConnection — POST /v1/connections/{connection_id}/request
//
// The host performs one provider-relative HTTP call with the stored credential
// attached and returns the provider's status, content type and body. The
// credential itself never crosses this boundary.
func (h *Handler) InvokePluginConnection(w http.ResponseWriter, r *http.Request) {
	if !h.requireConnectionsV1(w, r) {
		return
	}
	caller, _, ok := h.pluginCaller(w, r, plugincontract.ScopeConnectionsInvoke)
	if !ok {
		return
	}
	connectionID, ok := parseUUIDOrBadRequest(w, chi.URLParam(r, "connection_id"), "connection id")
	if !ok {
		return
	}
	// AuthorizeConsumer is the binding check: an installation may only use a
	// connection an administrator bound to it, and only while both are enabled.
	connection, err := h.ConnectionService.AuthorizeConsumer(
		r.Context(),
		connectionID,
		service.ConnectionConsumerPluginInstallation,
		caller.Installation.ID,
	)
	if err != nil {
		writePluginConnectionError(w, r, err)
		return
	}
	// A workspace connection must be in the caller's workspace; an instance
	// connection is deployment-wide by construction.
	if connection.WorkspaceID.Valid && connection.WorkspaceID != caller.WorkspaceID {
		publicapiv1.WriteProblem(w, r, http.StatusForbidden, "forbidden", "connection is not granted to this consumer")
		return
	}

	var request pluginConnectionProxyRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, service.MaxConnectionProxyRequestBytes+4096)).Decode(&request); err != nil {
		publicapiv1.WriteProblem(w, r, http.StatusBadRequest, "invalid_request", "request body must be a JSON object")
		return
	}
	query := url.Values{}
	for key, value := range request.Query {
		query.Set(key, value)
	}
	response, err := h.ConnectionService.ProxyRequest(r.Context(), connection, service.ConnectionProxyRequest{
		Method:      request.Method,
		Path:        request.Path,
		Query:       query,
		Body:        []byte(request.Body),
		ContentType: request.ContentType,
	})
	if err != nil {
		writePluginConnectionError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, pluginConnectionProxyResponse{
		Status:      response.Status,
		ContentType: response.ContentType,
		Body:        string(response.Body),
	})
}
