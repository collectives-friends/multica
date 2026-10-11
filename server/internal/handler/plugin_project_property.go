package handler

import (
	"encoding/json"
	"errors"
	"net/http"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/internal/middleware"
	"github.com/multica-ai/multica/server/internal/service"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
	"github.com/multica-ai/multica/server/pkg/plugincontract"
	publicapiv1 "github.com/multica-ai/multica/server/pkg/publicapi/v1"
)

// UpsertPluginProjectPropertyRequest is the integration-owned project property
// definition the plugin capability creates or reconciles (TCO-293). Options
// carry optional stable UUIDs: on first creation the host assigns them and the
// plugin stores them, so later upserts reconcile by ID (soft drift: rename and
// color updates, additions, and removal only of options no project references).
type UpsertPluginProjectPropertyRequest struct {
	Name        string          `json:"name"`
	Type        string          `json:"type"`
	Description string          `json:"description"`
	Icon        string          `json:"icon"`
	Config      *PropertyConfig `json:"config"`
}

// pluginPropertyAdminCaller requires the admin attestation only a callback
// token carries: schema creation is a human-confirmed action, never a
// standing-installation write.
func (h *Handler) pluginPropertyAdminCaller(w http.ResponseWriter, r *http.Request) (service.PluginActionCaller, bool) {
	token := middleware.BearerToken(r)
	if !strings.HasPrefix(token, "mpc_") {
		publicapiv1.WriteProblem(w, r, http.StatusForbidden, "callback_required", "property definitions require a callback credential attesting an administrator")
		return service.PluginActionCaller{}, false
	}
	c, actor, ok := h.pluginCaller(w, r, plugincontract.ScopeProjectsWrite)
	if !ok {
		return service.PluginActionCaller{}, false
	}
	if !actor.isMember() || (actor.Member.Role != "admin" && actor.Member.Role != "owner") {
		publicapiv1.WriteProblem(w, r, http.StatusForbidden, "admin_required", "property definitions require a workspace administrator")
		return service.PluginActionCaller{}, false
	}
	return c, true
}

// UpsertPluginProjectProperty idempotently creates or reconciles the
// installation's project-type property definition named req.Name and returns
// its definition with stable option IDs.
func (h *Handler) UpsertPluginProjectProperty(w http.ResponseWriter, r *http.Request) {
	c, ok := h.pluginPropertyAdminCaller(w, r)
	if !ok {
		return
	}
	if !h.workspaceFlagEnabled(r.Context(), uuidToString(c.WorkspaceID), ProjectPropertiesFlag) {
		publicapiv1.WriteProblem(w, r, http.StatusForbidden, "capability_disabled", "project properties are not enabled for this workspace")
		return
	}
	invalid := func(message string) {
		publicapiv1.WriteProblem(w, r, http.StatusBadRequest, "invalid_request", message)
	}
	var req UpsertPluginProjectPropertyRequest
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1024*1024))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&req); err != nil {
		invalid("invalid property definition")
		return
	}
	name, err := validatePropertyName(req.Name)
	if err != nil {
		invalid(err.Error())
		return
	}
	if err := validatePropertyType(req.Type); err != nil {
		invalid(err.Error())
		return
	}
	icon, err := validatePropertyIcon(req.Icon)
	if err != nil {
		invalid(err.Error())
		return
	}
	configJSON, err := validatePropertyConfig(req.Type, req.Config)
	if err != nil {
		invalid(err.Error())
		return
	}

	workspaceID := uuidToString(c.WorkspaceID)
	wsUUID := c.WorkspaceID
	installationID := c.Installation.ID

	var property db.IssueProperty
	created := false
	var httpStatus int
	var httpMsg string
	fail := func(status int, msg string) error {
		httpStatus, httpMsg = status, msg
		return errClientRejected
	}
	err = h.withPropertyLock(r, []string{"props:" + workspaceID, "install:" + uuidToString(installationID)}, func(q *db.Queries) error {
		existing, err := q.GetOwnedProjectPropertyByName(r.Context(), db.GetOwnedProjectPropertyByNameParams{
			WorkspaceID:         wsUUID,
			OwnerInstallationID: installationID,
			Name:                name,
		})
		if err != nil && !errors.Is(err, pgx.ErrNoRows) {
			return err
		}
		if errors.Is(err, pgx.ErrNoRows) {
			active, err := q.CountActiveIssueProperties(r.Context(), db.CountActiveIssuePropertiesParams{
				WorkspaceID:  wsUUID,
				ResourceType: propertyResourceTypeProject,
			})
			if err != nil {
				return err
			}
			if active >= maxActivePropertiesPerWorkspace {
				return fail(http.StatusBadRequest, "a workspace cannot have more than 20 active properties; archive unused ones first")
			}
			property, err = q.CreateIssueProperty(r.Context(), db.CreateIssuePropertyParams{
				WorkspaceID:         wsUUID,
				Name:                name,
				Type:                req.Type,
				Description:         sanitizeNullBytes(strings.TrimSpace(req.Description)),
				Icon:                icon,
				Config:              configJSON,
				ResourceType:        propertyResourceTypeProject,
				OwnerInstallationID: installationID,
			})
			if err != nil {
				if isUniqueViolation(err) {
					return fail(http.StatusConflict, "a property with that name already exists")
				}
				return err
			}
			created = true
			return nil
		}
		if existing.Type != req.Type {
			return fail(http.StatusConflict, "property type cannot change")
		}
		merged, err := reconcileOwnedOptions(r, q, existing, req.Config, wsUUID)
		if err != nil {
			return err
		}
		property, err = q.UpdateIssueProperty(r.Context(), db.UpdateIssuePropertyParams{
			ID:          existing.ID,
			WorkspaceID: wsUUID,
			Name:        pgtype.Text{String: name, Valid: true},
			Description: pgtype.Text{String: sanitizeNullBytes(strings.TrimSpace(req.Description)), Valid: true},
			Icon:        pgtype.Text{String: icon, Valid: true},
			Config:      merged,
		})
		if err != nil {
			if isUniqueViolation(err) {
				return fail(http.StatusConflict, "a property with that name already exists")
			}
			return err
		}
		return nil
	})
	if err != nil {
		if errors.Is(err, errClientRejected) {
			problemType := "invalid_request"
			if httpStatus == http.StatusConflict {
				problemType = "conflict"
			}
			publicapiv1.WriteProblem(w, r, httpStatus, problemType, httpMsg)
			return
		}
		publicapiv1.WriteProblem(w, r, http.StatusInternalServerError, "internal_error", "property definition could not be saved")
		return
	}
	status := http.StatusOK
	if created {
		status = http.StatusCreated
	}
	writeJSON(w, status, propertyToResponse(property, 0))
}

// reconcileOwnedOptions applies soft option drift: incoming options match
// existing ones by ID, or by name when the caller has no ID yet (first
// publish). Options the incoming list no longer carries are removed only when
// no project references them; a referenced option stays resolvable at the end
// of the list. Order follows the incoming list.
func reconcileOwnedOptions(r *http.Request, q *db.Queries, existing db.IssueProperty, incoming *PropertyConfig, wsUUID pgtype.UUID) ([]byte, error) {
	existingCfg := parsePropertyConfig(existing.Config)
	byID := make(map[string]PropertyOption, len(existingCfg.Options))
	byName := make(map[string]PropertyOption, len(existingCfg.Options))
	for _, opt := range existingCfg.Options {
		byID[opt.ID] = opt
		byName[strings.ToLower(opt.Name)] = opt
	}
	used := make(map[string]bool)
	merged := make([]PropertyOption, 0, len(existingCfg.Options))
	for _, opt := range incoming.Options {
		var base PropertyOption
		var ok bool
		if opt.ID != "" {
			base, ok = byID[opt.ID]
		} else {
			base, ok = byName[strings.ToLower(opt.Name)]
		}
		if ok && !used[base.ID] {
			merged = append(merged, PropertyOption{ID: base.ID, Name: opt.Name, Color: opt.Color})
			used[base.ID] = true
			continue
		}
		merged = append(merged, opt)
		if opt.ID != "" {
			used[opt.ID] = true
		}
	}
	for _, opt := range existingCfg.Options {
		if used[opt.ID] {
			continue
		}
		rows, err := q.CountProjectsUsingPropertyOptions(r.Context(), db.CountProjectsUsingPropertyOptionsParams{
			OptionIds:   []string{opt.ID},
			WorkspaceID: wsUUID,
			PropertyKey: uuidToString(existing.ID),
		})
		if err != nil {
			return nil, err
		}
		if len(rows) > 0 {
			merged = append(merged, opt)
			used[opt.ID] = true
		}
	}
	return validatePropertyConfig(existing.Type, &PropertyConfig{Options: merged})
}
