package handler

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/internal/middleware"
	"github.com/multica-ai/multica/server/internal/service"
	"github.com/multica-ai/multica/server/internal/util"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
	"github.com/multica-ai/multica/server/pkg/plugincontract"
	"github.com/multica-ai/multica/server/pkg/protocol"
	publicapiv1 "github.com/multica-ai/multica/server/pkg/publicapi/v1"
)

// Authorization is a point-in-time read, not a transferable lease. Backends
// must recheck before every dispatch and drain/read back already sent writes.
// Only callback credentials can attest a person and the exact invoked input.
func (h *Handler) GetPluginInstallationAuthorization(w http.ResponseWriter, r *http.Request) {
	caller, actor, ok := h.pluginCaller(w, r, plugincontract.ScopeInstallationRead)
	if !ok {
		return
	}
	ctx, err := h.PluginService.RequireContributionFresh(r.Context(), caller.Installation, "", uuidToString(caller.UserID))
	if err != nil {
		writePluginActionError(w, r, err, "authorization unavailable")
		return
	}
	result := map[string]any{"installation_id": uuidToString(caller.Installation.ID), "workspace_id": uuidToString(caller.WorkspaceID), "enabled": true, "credential_kind": "standing", "flags": h.PluginService.ContributionFlags(ctx, caller.Installation, uuidToString(caller.UserID)), "actor": map[string]any{"type": "plugin", "id": uuidToString(caller.Installation.ID), "admin": false}}
	token := middleware.BearerToken(r)
	if strings.HasPrefix(token, "mpc_") {
		grant, err := h.PluginService.Callbacks.Resolve(token)
		if err != nil {
			writePluginActionError(w, r, err, "callback unavailable")
			return
		}
		result["credential_kind"] = "callback"
		result["hook_key"] = grant.HookKey
		result["input"] = grant.Input
		result["actor"] = map[string]any{"type": actor.Type, "id": uuidToString(actor.Member.UserID), "admin": actor.isMember() && (actor.Member.Role == "admin" || actor.Member.Role == "owner")}
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, http.StatusOK, result)
}

type pluginProject struct {
	ID          string          `json:"id"`
	WorkspaceID string          `json:"workspace_id"`
	Title       string          `json:"title"`
	Description *string         `json:"description"`
	Status      string          `json:"status"`
	Revision    int64           `json:"revision"`
	Properties  json.RawMessage `json:"properties"`
}

func pluginProjectDTO(p db.Project) pluginProject {
	return pluginProject{uuidToString(p.ID), uuidToString(p.WorkspaceID), p.Title, textToPtr(p.Description), p.Status, p.Revision, json.RawMessage(projectPropertiesJSON(p.Properties))}
}

func (h *Handler) pluginProjectCaller(w http.ResponseWriter, r *http.Request, scope string) (service.PluginActionCaller, bool) {
	c, _, ok := h.pluginCaller(w, r, scope)
	if ok && strings.HasPrefix(middleware.BearerToken(r), "mpc_") {
		publicapiv1.WriteProblem(w, r, http.StatusForbidden, "standing_credential_required", "project APIs require a session or standing installation credential")
		return c, false
	}
	return c, ok
}
func (h *Handler) ListPluginProjects(w http.ResponseWriter, r *http.Request) {
	c, ok := h.pluginProjectCaller(w, r, plugincontract.ScopeProjectsRead)
	if !ok {
		return
	}
	projects, err := h.Queries.ListProjects(r.Context(), db.ListProjectsParams{WorkspaceID: c.WorkspaceID})
	if err != nil {
		publicapiv1.WriteProblem(w, r, 500, "internal_error", "failed to list projects")
		return
	}
	out := make([]pluginProject, 0, len(projects))
	for _, p := range projects {
		out = append(out, pluginProjectDTO(p))
	}
	writeJSON(w, 200, map[string]any{"projects": out})
}
func (h *Handler) GetPluginProject(w http.ResponseWriter, r *http.Request) {
	c, ok := h.pluginProjectCaller(w, r, plugincontract.ScopeProjectsRead)
	if !ok {
		return
	}
	id, err := util.ParseUUID(chi.URLParam(r, "project_id"))
	if err != nil {
		publicapiv1.WriteProblem(w, r, 404, "not_found", "project not found")
		return
	}
	p, err := h.Queries.GetProjectInWorkspace(r.Context(), db.GetProjectInWorkspaceParams{ID: id, WorkspaceID: c.WorkspaceID})
	if err != nil {
		publicapiv1.WriteProblem(w, r, 404, "not_found", "project not found")
		return
	}
	writeJSON(w, 200, pluginProjectDTO(p))
}

type pluginProjectMutation struct {
	OperationID      string          `json:"operation_id"`
	ExpectedRevision int64           `json:"expected_revision,omitempty"`
	Title            *string         `json:"title,omitempty"`
	Description      json.RawMessage `json:"description,omitempty"`
	Status           *string         `json:"status,omitempty"`
	Properties       json.RawMessage `json:"properties,omitempty"`
}

func (h *Handler) CreatePluginProject(w http.ResponseWriter, r *http.Request) {
	h.mutatePluginProject(w, r, true)
}
func (h *Handler) PatchPluginProject(w http.ResponseWriter, r *http.Request) {
	h.mutatePluginProject(w, r, false)
}
func (h *Handler) mutatePluginProject(w http.ResponseWriter, r *http.Request, create bool) {
	c, ok := h.pluginProjectCaller(w, r, plugincontract.ScopeProjectsWrite)
	if !ok {
		return
	}
	invalid := func(message string) { publicapiv1.WriteProblem(w, r, 400, "invalid_request", message) }
	var req pluginProjectMutation
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1024*1024))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&req); err != nil {
		invalid("invalid project mutation")
		return
	}
	if err := decoder.Decode(new(any)); err != io.EOF {
		invalid("unexpected trailing data")
		return
	}
	operationID, err := util.ParseUUID(req.OperationID)
	if err != nil {
		invalid("operation_id must be a UUID")
		return
	}
	if create && req.Title == nil {
		invalid("title is required")
		return
	}
	if req.Title != nil && (strings.TrimSpace(*req.Title) == "" || len(*req.Title) > 1000 || strings.ContainsRune(*req.Title, 0)) {
		invalid("title must contain 1 to 1000 characters without null bytes")
		return
	}
	if !create && req.ExpectedRevision < 1 {
		invalid("expected_revision is required")
		return
	}
	if !create && req.Title == nil && req.Status == nil && req.Description == nil && req.Properties == nil {
		invalid("at least one field is required")
		return
	}
	if req.Status != nil {
		switch *req.Status {
		case "planned", "in_progress", "paused", "completed", "cancelled":
		default:
			invalid("invalid project status")
			return
		}
	}
	var description *string
	if req.Description != nil && json.Unmarshal(req.Description, &description) != nil {
		invalid("description must be string or null")
		return
	}
	if description != nil && strings.ContainsRune(*description, 0) {
		invalid("description cannot contain null bytes")
		return
	}
	// Property values reference integration-owned project definitions only.
	// Each value is validated against its definition before the write, and the
	// bag merges per key so other owners' properties are never clobbered.
	var properties []byte
	if req.Properties != nil {
		var bag map[string]json.RawMessage
		if err := json.Unmarshal(req.Properties, &bag); err != nil {
			invalid("properties must be an object keyed by definition UUID")
			return
		}
		if len(bag) > 0 && !h.workspaceFlagEnabled(r.Context(), uuidToString(c.WorkspaceID), ProjectPropertiesFlag) {
			publicapiv1.WriteProblem(w, r, http.StatusForbidden, "capability_disabled", "project properties are not enabled for this workspace")
			return
		}
		canonical := make(map[string]json.RawMessage, len(bag))
		for key, raw := range bag {
			defUUID, err := util.ParseUUID(key)
			if err != nil {
				invalid("properties keys must be definition UUIDs")
				return
			}
			def, err := h.Queries.GetIssueProperty(r.Context(), db.GetIssuePropertyParams{ID: defUUID, WorkspaceID: c.WorkspaceID})
			if err != nil {
				if errors.Is(err, pgx.ErrNoRows) {
					invalid("properties reference an unknown definition")
					return
				}
				publicapiv1.WriteProblem(w, r, http.StatusServiceUnavailable, "unavailable", "property definitions unavailable")
				return
			}
			if def.ResourceType != propertyResourceTypeProject || !def.OwnerInstallationID.Valid || def.OwnerInstallationID != c.Installation.ID {
				publicapiv1.WriteProblem(w, r, http.StatusForbidden, "forbidden", "property values may only be written for definitions owned by this installation")
				return
			}
			if def.ArchivedAt.Valid {
				invalid("properties reference an archived definition")
				return
			}
			value, err := validatePropertyValue(def, raw)
			if err != nil {
				invalid("invalid value for property " + def.Name + ": " + err.Error())
				return
			}
			canonical[uuidToString(defUUID)] = json.RawMessage(value)
		}
		properties, err = json.Marshal(canonical)
		if err != nil {
			invalid("properties are invalid")
			return
		}
	} else {
		properties = json.RawMessage(`{}`)
	}
	var projectID pgtype.UUID
	if !create {
		projectID, err = util.ParseUUID(chi.URLParam(r, "project_id"))
		if err != nil {
			invalid("invalid project id")
			return
		}
	}
	// One receipt covers path, revision and payload; same key with a different
	// request is always a conflict, even if the resulting fields happen to match.
	requestBytes, _ := json.Marshal(struct {
		Project  string                `json:"project"`
		Create   bool                  `json:"create"`
		Mutation pluginProjectMutation `json:"mutation"`
	}{chi.URLParam(r, "project_id"), create, req})
	tx, err := h.TxStarter.Begin(r.Context())
	if err != nil {
		publicapiv1.WriteProblem(w, r, 503, "unavailable", "transaction unavailable")
		return
	}
	defer tx.Rollback(r.Context())
	q := h.Queries.WithTx(tx)
	// Shares the lifecycle/flag write domain. Row lock also serializes revoke.
	if err = q.LockWorkspaceFlagWrites(r.Context(), uuidToString(c.WorkspaceID)); err != nil {
		publicapiv1.WriteProblem(w, r, 503, "unavailable", "authorization lock unavailable")
		return
	}
	if _, err = q.LockPluginInstallationForProject(r.Context(), c.Installation.ID); err != nil {
		publicapiv1.WriteProblem(w, r, 403, "forbidden", "installation unavailable")
		return
	}
	c, ok = h.pluginProjectCaller(w, r, plugincontract.ScopeProjectsWrite)
	if !ok {
		return
	}
	if _, err = h.PluginService.RequireContributionFresh(r.Context(), c.Installation, "", uuidToString(c.UserID)); err != nil {
		writePluginActionError(w, r, err, "authorization unavailable")
		return
	}
	receipt, err := q.GetPluginProjectOperation(r.Context(), db.GetPluginProjectOperationParams{InstallationID: c.Installation.ID, OperationID: operationID})
	if err == nil {
		var old, now any
		_ = json.Unmarshal(receipt.Request, &old)
		_ = json.Unmarshal(requestBytes, &now)
		oldJSON, _ := json.Marshal(old)
		nowJSON, _ := json.Marshal(now)
		if !bytes.Equal(oldJSON, nowJSON) {
			publicapiv1.WriteProblem(w, r, 409, "operation_conflict", "operation_id was already used for a different request")
			return
		}
		writeJSON(w, 200, json.RawMessage(receipt.Response))
		return
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		publicapiv1.WriteProblem(w, r, 503, "unavailable", "operation receipt unavailable")
		return
	}
	var project db.Project
	if create {
		status := "planned"
		if req.Status != nil {
			status = *req.Status
		}
		project, err = q.CreatePluginProject(r.Context(), db.CreatePluginProjectParams{ID: operationID, WorkspaceID: c.WorkspaceID, Title: *req.Title, Description: ptrToText(description), Status: status, Properties: properties})
	} else {
		project, err = q.PatchPluginProject(r.Context(), db.PatchPluginProjectParams{ID: projectID, WorkspaceID: c.WorkspaceID, ExpectedRevision: req.ExpectedRevision, Title: ptrToText(req.Title), Description: ptrToText(description), SetDescription: req.Description != nil, Status: ptrToText(req.Status), SetProperties: req.Properties != nil, Properties: properties})
	}
	var constraint *pgconn.PgError
	if errors.As(err, &constraint) && constraint.Code == "23505" {
		publicapiv1.WriteProblem(w, r, 409, "operation_conflict", "project identity already exists")
		return
	}
	if errors.Is(err, pgx.ErrNoRows) {
		publicapiv1.WriteProblem(w, r, 409, "revision_conflict", "project is unavailable or changed since it was read")
		return
	}
	if err != nil {
		publicapiv1.WriteProblem(w, r, 500, "internal_error", "project mutation failed")
		return
	}
	response, _ := json.Marshal(pluginProjectDTO(project))
	err = q.InsertPluginProjectOperation(r.Context(), db.InsertPluginProjectOperationParams{InstallationID: c.Installation.ID, OperationID: operationID, Request: requestBytes, Response: response})
	if err != nil {
		publicapiv1.WriteProblem(w, r, 503, "unavailable", "operation receipt could not be saved")
		return
	}
	if err = tx.Commit(r.Context()); err != nil {
		publicapiv1.WriteProblem(w, r, 503, "unknown_outcome", "read back using the same operation_id before retrying")
		return
	}
	actorType, actorID := "plugin", uuidToString(c.Installation.ID)
	if c.UserID.Valid {
		actorType, actorID = "member", uuidToString(c.UserID)
	}
	event := protocol.EventProjectUpdated
	if create {
		event = protocol.EventProjectCreated
	}
	app := projectToResponse(project)
	app.IssueCount, app.DoneCount = h.loadProjectIssueStats(r.Context(), c.WorkspaceID, project.ID)
	app.ResourceCount = h.loadProjectResourceCount(r.Context(), project.ID)
	h.publish(event, uuidToString(c.WorkspaceID), actorType, actorID, map[string]any{"project": app})
	status := 200
	if create {
		status = 201
	}
	writeJSON(w, status, json.RawMessage(response))
}
