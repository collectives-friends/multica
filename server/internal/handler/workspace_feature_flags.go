package handler

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/internal/events"
	"github.com/multica-ai/multica/server/internal/featureflags"
	"github.com/multica-ai/multica/server/internal/util"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
	"github.com/multica-ai/multica/server/pkg/featureflag"
	"github.com/multica-ai/multica/server/pkg/protocol"
)

func (h *Handler) flagResolverFor(q *db.Queries) *featureflags.Resolver {
	registry := featureflags.HostRegistry()
	if h.FlagResolver != nil {
		registry = h.FlagResolver.Registry
	}
	return featureflags.NewResolver(registry, h.FeatureFlags, featureflags.DBSource{Queries: q})
}

func flagError(w http.ResponseWriter, status int, code, message string) {
	writeJSON(w, status, map[string]string{"error": message, "code": code})
}

func flagMember(w http.ResponseWriter, r *http.Request, q *db.Queries, admin bool) (db.Member, bool) {
	workspaceID := chi.URLParam(r, "id")
	userID := requestUserID(r)
	if userID == "" {
		writeError(w, 401, "user not authenticated")
		return db.Member{}, false
	}
	if r.Header.Get("X-Actor-Source") == "task_token" && r.Header.Get("X-Workspace-ID") != workspaceID {
		writeError(w, 403, "task token is bound to a different workspace")
		return db.Member{}, false
	}
	wid, err := util.ParseUUID(workspaceID)
	if err != nil {
		writeError(w, 400, "invalid workspace_id")
		return db.Member{}, false
	}
	uid, err := util.ParseUUID(userID)
	if err != nil {
		writeError(w, 401, "user not authenticated")
		return db.Member{}, false
	}
	member, err := q.GetMemberByUserAndWorkspace(r.Context(), db.GetMemberByUserAndWorkspaceParams{WorkspaceID: wid, UserID: uid})
	if err != nil {
		writeError(w, 404, "workspace not found")
		return db.Member{}, false
	}
	if admin && member.Role != "owner" && member.Role != "admin" {
		writeError(w, 403, "insufficient permissions")
		return db.Member{}, false
	}
	return member, true
}

func (h *Handler) GetFeatureFlags(w http.ResponseWriter, r *http.Request) {
	member, ok := flagMember(w, r, h.Queries, false)
	if !ok {
		return
	}
	resolver := h.flagResolverFor(h.Queries)
	ctx, err := resolver.Prepare(r.Context(), uuidToString(member.WorkspaceID), uuidToString(member.UserID))
	if err != nil {
		writeError(w, 503, "feature flag state unavailable")
		return
	}
	view, err := resolver.View(ctx, uuidToString(member.WorkspaceID), uuidToString(member.UserID))
	if err != nil {
		writeError(w, 503, "feature flag state unavailable")
		return
	}
	writeJSON(w, 200, view)
}
func (h *Handler) SetMemberFeatureFlag(w http.ResponseWriter, r *http.Request) {
	h.writeFeatureFlag(w, r, false, false)
}
func (h *Handler) DeleteMemberFeatureFlag(w http.ResponseWriter, r *http.Request) {
	h.writeFeatureFlag(w, r, false, true)
}
func (h *Handler) SetWorkspaceFeatureFlag(w http.ResponseWriter, r *http.Request) {
	h.writeFeatureFlag(w, r, true, false)
}
func (h *Handler) DeleteWorkspaceFeatureFlag(w http.ResponseWriter, r *http.Request) {
	h.writeFeatureFlag(w, r, true, true)
}

func (h *Handler) writeFeatureFlag(w http.ResponseWriter, r *http.Request, workspace, clear bool) {
	member, ok := flagMember(w, r, h.Queries, workspace)
	if !ok {
		return
	}
	key := chi.URLParam(r, "key")
	var body struct {
		Enabled *bool   `json:"enabled"`
		Locked  *bool   `json:"locked"`
		Reason  *string `json:"reason"`
	}
	if !clear {
		decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096))
		decoder.DisallowUnknownFields()
		if err := decoder.Decode(&body); err != nil || body.Enabled == nil {
			writeError(w, 400, "enabled is required")
			return
		}
		if !workspace && (body.Locked != nil || body.Reason != nil) {
			writeError(w, 400, "personal overrides accept only enabled")
			return
		}
		if body.Reason != nil && utf8.RuneCountInString(*body.Reason) > 500 {
			writeError(w, 400, "reason must be at most 500 characters")
			return
		}
	}
	tx, err := h.TxStarter.Begin(r.Context())
	if err != nil {
		writeError(w, 500, "failed to start feature flag update")
		return
	}
	defer tx.Rollback(r.Context())
	q := h.Queries.WithTx(tx)
	wid := uuidToString(member.WorkspaceID)
	uid := uuidToString(member.UserID)
	if err = q.LockWorkspaceFlagWrites(r.Context(), wid); err != nil {
		writeError(w, 500, "failed to lock feature flags")
		return
	}
	// Membership and definitions are read again under the lifecycle lock. An
	// uninstall or member removal cannot leave a newly inserted orphan behind.
	member, ok = flagMember(w, r, q, workspace)
	if !ok {
		return
	}
	resolver := h.flagResolverFor(q)
	ctx, err := resolver.Prepare(r.Context(), wid, uid)
	if err != nil {
		writeError(w, 503, "feature flag state unavailable")
		return
	}
	view, err := resolver.View(ctx, wid, uid)
	if err != nil {
		writeError(w, 503, "feature flag state unavailable")
		return
	}
	var definition *featureflags.Definition
	for i := range view.Definitions {
		if view.Definitions[i].Key == key {
			definition = &view.Definitions[i]
			break
		}
	}
	if definition == nil {
		flagError(w, 404, "flag_unknown", "Unknown feature flag")
		return
	}
	if definition.Frozen != nil {
		flagError(w, 409, "flag_frozen", "This feature flag is fixed by the host")
		return
	}
	if !workspace && definition.ScopePolicy != featureflags.MemberOverridable {
		flagError(w, 409, "flag_workspace_only", "This flag can only be configured for a workspace")
		return
	}
	scope := "member"
	scopeID := member.UserID
	if workspace {
		scope = "workspace"
		scopeID = member.WorkspaceID
	}
	old, err := q.GetFlagOverride(ctx, db.GetFlagOverrideParams{WorkspaceID: member.WorkspaceID, ScopeType: scope, ScopeID: scopeID, FlagKey: key})
	exists := err == nil
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		writeError(w, 500, "failed to read feature flag override")
		return
	}
	actorType, actorID := h.resolveActor(r, uid, wid)
	actorUUID, err := util.ParseUUID(actorID)
	if err != nil {
		writeError(w, 403, "invalid actor")
		return
	}
	audit := db.InsertFlagAuditParams{WorkspaceID: member.WorkspaceID, FlagKey: key, ScopeType: scope, ScopeID: scopeID, Action: "set", ActorType: actorType, ActorID: actorUUID}
	if exists {
		audit.OldEnabled = pgtype.Bool{Bool: old.Enabled, Valid: true}
		audit.OldLocked = pgtype.Bool{Bool: old.Locked, Valid: true}
	}
	if body.Reason != nil {
		audit.Reason = pgtype.Text{String: *body.Reason, Valid: true}
	}
	var enabled, locked *bool
	if clear {
		audit.Action = "clear"
		err = q.DeleteFlagOverride(ctx, db.DeleteFlagOverrideParams{WorkspaceID: member.WorkspaceID, ScopeType: scope, ScopeID: scopeID, FlagKey: key})
	} else {
		lock := body.Locked != nil && *body.Locked
		enabled = body.Enabled
		locked = &lock
		audit.NewEnabled = pgtype.Bool{Bool: *enabled, Valid: true}
		audit.NewLocked = pgtype.Bool{Bool: lock, Valid: true}
		_, err = q.UpsertFlagOverride(ctx, db.UpsertFlagOverrideParams{WorkspaceID: member.WorkspaceID, ScopeType: scope, ScopeID: scopeID, FlagKey: key, Enabled: *enabled, Locked: lock, UpdatedByType: actorType, UpdatedByID: actorUUID})
	}
	if err != nil {
		writeError(w, 500, "failed to write feature flag override")
		return
	}
	if _, err = q.InsertFlagAudit(ctx, audit); err != nil {
		writeError(w, 500, "failed to audit feature flag update")
		return
	}
	if err = tx.Commit(ctx); err != nil {
		writeError(w, 500, "failed to commit feature flag update")
		return
	}
	if h.FlagResolver != nil {
		h.FlagResolver.Invalidate(wid)
	}
	event := events.Event{Type: protocol.EventFeatureFlagChanged, WorkspaceID: wid, ActorType: actorType, ActorID: actorID, Payload: map[string]any{"workspace_id": wid, "key": key, "scope_type": scope, "enabled": enabled, "locked": locked, "action": audit.Action}}
	if !workspace {
		event.RecipientUserID = uid
	}
	if h.Bus != nil {
		h.Bus.Publish(event)
	}
	if clear {
		w.WriteHeader(204)
		return
	}
	writeJSON(w, 200, map[string]any{"enabled": enabled, "locked": locked})
}

func (h *Handler) GetFeatureFlagAudit(w http.ResponseWriter, r *http.Request) {
	member, ok := flagMember(w, r, h.Queries, true)
	if !ok {
		return
	}
	limit := 50
	if raw := r.URL.Query().Get("limit"); raw != "" {
		value, err := strconv.Atoi(raw)
		if err != nil || value < 1 || value > 100 {
			writeError(w, 400, "limit must be between 1 and 100")
			return
		}
		limit = value
	}
	args := db.ListFlagAuditParams{WorkspaceID: member.WorkspaceID, FlagKey: r.URL.Query().Get("key"), RowLimit: int32(limit + 1)}
	if raw := r.URL.Query().Get("before"); raw != "" {
		decoded, err := base64.RawURLEncoding.DecodeString(raw)
		parts := strings.Split(string(decoded), "|")
		if err != nil || len(parts) != 2 {
			writeError(w, 400, "invalid audit cursor")
			return
		}
		date, err := time.Parse(time.RFC3339Nano, parts[0])
		if err != nil {
			writeError(w, 400, "invalid audit cursor")
			return
		}
		id, err := util.ParseUUID(parts[1])
		if err != nil {
			writeError(w, 400, "invalid audit cursor")
			return
		}
		args.BeforeTime = pgtype.Timestamptz{Time: date, Valid: true}
		args.BeforeID = id
	}
	rows, err := h.Queries.ListFlagAudit(r.Context(), args)
	if err != nil {
		writeError(w, 500, "failed to read feature flag history")
		return
	}
	cursor := ""
	if len(rows) > limit {
		rows = rows[:limit]
		last := rows[len(rows)-1]
		cursor = base64.RawURLEncoding.EncodeToString([]byte(last.CreatedAt.Time.Format(time.RFC3339Nano) + "|" + uuidToString(last.ID)))
	}
	writeJSON(w, 200, struct {
		Entries    []db.WorkspaceFeatureFlagAudit `json:"entries"`
		NextBefore string                         `json:"next_before,omitempty"`
	}{rows, cursor})
}

// PrepareFeatureFlags runs after workspace membership resolution.
func (h *Handler) PrepareFeatureFlags(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Entity-scoped paths set EvalContext when their loader resolves membership.
		// Those paths may use explicit EnabledFor without preloading this request.
		if strings.Contains(r.URL.Path, "/feature-flags") {
			next.ServeHTTP(w, r)
			return
		}
		ec := featureflag.EvalContextFrom(r.Context())
		if ec.WorkspaceID == "" || h.FlagResolver == nil {
			next.ServeHTTP(w, r)
			return
		}
		ctx, err := h.FlagResolver.Prepare(r.Context(), ec.WorkspaceID, ec.UserID)
		if err != nil {
			writeError(w, 503, "feature flag state unavailable")
			return
		}
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

func (h *Handler) workspaceFlagEnabled(ctx context.Context, workspaceID, key string) bool {
	resolver := h.FlagResolver
	if resolver == nil {
		resolver = h.flagResolverFor(h.Queries)
	}
	return resolver.ResolveWorkspace(ctx, workspaceID, key).Value
}
