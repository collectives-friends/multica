package service

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/internal/featureflags"
	"github.com/multica-ai/multica/server/internal/util"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
	"github.com/multica-ai/multica/server/pkg/featureflag"
	"github.com/multica-ai/multica/server/pkg/plugincontract"
)

const PluginContributionDisabled = "plugin_contribution_disabled"

func PluginFlagKey(pluginKey, key string) string { return "plugin:" + pluginKey + ":" + key }
func pluginFlagReference(pluginKey, reference string) string {
	if key, ok := strings.CutPrefix(reference, "host:"); ok {
		return key
	}
	return PluginFlagKey(pluginKey, reference)
}

// PluginFlagDefinitions uses the supplied Queries so lifecycle/API transactions
// resolve the very snapshot protected by their workspace advisory lock.
func PluginFlagDefinitions(ctx context.Context, queries *db.Queries, workspaceID string) ([]featureflags.Definition, error) {
	id, err := util.ParseUUID(workspaceID)
	if err != nil {
		return nil, err
	}
	rows, err := queries.ListWorkspacePluginInstallations(ctx, id)
	if err != nil {
		return nil, err
	}
	defs := []featureflags.Definition{}
	for _, installation := range rows {
		if !installation.Enabled {
			continue
		}
		manifest, err := ParseInstallationManifest(installation)
		if err != nil {
			return nil, err
		}
		for _, flag := range manifest.Contributes.Flags {
			parent := featureflags.PluginsV1
			if flag.Parent != "" {
				parent = pluginFlagReference(manifest.Key, flag.Parent)
			}
			defs = append(defs, featureflags.Definition{Key: PluginFlagKey(manifest.Key, flag.Key), Label: flag.Label, Description: flag.Description, Group: "Plugins", Default: flag.Default, ScopePolicy: flag.ScopePolicy, Parent: parent, Public: true, PluginInstallationID: uuidString(installation.ID), PluginName: manifest.Name})
		}
	}
	return defs, nil
}

func (s *PluginService) flagResolver() *featureflags.Resolver {
	if s.FlagResolver != nil {
		return s.FlagResolver
	}
	var source featureflags.Source
	if s.Queries != nil {
		source = featureflags.DBSource{Queries: s.Queries, PluginDefinitions: func(ctx context.Context, w string) ([]featureflags.Definition, error) {
			return PluginFlagDefinitions(ctx, s.Queries, w)
		}}
	}
	return featureflags.NewResolver(featureflags.HostRegistry(), s.FeatureFlags, source)
}

// PluginsEnabled uses a workspace identity even for callers without a session.
func (s *PluginService) PluginsEnabled(ctx context.Context, workspaceID, userID string) bool {
	return s.flagResolver().EnabledFor(ctx, workspaceID, userID, featureflags.PluginsV1)
}

// ContributionActive is the shared runtime gate. An empty userID always means
// workspace resolution, including agents, events, schedules and skill pins.
func (s *PluginService) ContributionActive(ctx context.Context, installation db.PluginInstallation, requiresFlag, userID string) bool {
	if !installation.Enabled {
		return false
	}
	resolver := s.flagResolver()
	workspaceID := uuidString(installation.WorkspaceID)
	if !resolver.EnabledFor(ctx, workspaceID, userID, featureflags.PluginsV1) {
		return false
	}
	if requiresFlag == "" {
		return true
	}
	return resolver.EnabledFor(ctx, workspaceID, userID, pluginFlagReference(installation.PluginKey, requiresFlag))
}

// ContributionFlags exposes own short keys and explicitly referenced public host
// keys. host:plugins_v1 is included as every contribution's implicit parent.
func (s *PluginService) ContributionFlags(ctx context.Context, installation db.PluginInstallation, userID string) map[string]bool {
	out := map[string]bool{}
	manifest, err := ParseInstallationManifest(installation)
	if err != nil {
		return out
	}
	resolver := s.flagResolver()
	workspaceID := uuidString(installation.WorkspaceID)
	refs := map[string]bool{featureflags.PluginsV1: true}
	reference := func(ref string) {
		if key, ok := strings.CutPrefix(ref, "host:"); ok {
			refs[key] = true
		}
	}
	for _, flag := range manifest.Contributes.Flags {
		reference(flag.Parent)
	}
	for _, surface := range manifest.Contributes.Surfaces {
		reference(surface.RequiresFlag)
	}
	for _, hook := range manifest.Contributes.Hooks {
		reference(hook.RequiresFlag)
	}
	for _, resource := range manifest.Contributes.Resources {
		reference(resource.RequiresFlag)
	}
	public := resolver.Registry.PublicKeys()
	for key := range refs {
		if public[key] {
			out["host:"+key] = resolver.EnabledFor(ctx, workspaceID, userID, key)
		}
	}
	for _, flag := range manifest.Contributes.Flags {
		out[flag.Key] = s.ContributionActive(ctx, installation, flag.Key, userID)
	}
	return out
}

func hookUserID(invocation HookInvocation) string {
	if (invocation.Trigger == plugincontract.TriggerUI || invocation.Trigger == plugincontract.TriggerManual) && invocation.Actor.Type == "member" {
		return uuidString(invocation.Actor.ID)
	}
	return ""
}

// RequireContributionFresh bypasses cached decisions before an outbound effect.
func (s *PluginService) RequireContributionFresh(ctx context.Context, installation db.PluginInstallation, requiresFlag, userID string) (context.Context, error) {
	prepared, err := s.flagResolver().Prepare(ctx, uuidString(installation.WorkspaceID), userID)
	if err != nil {
		return ctx, &PluginError{Kind: PluginErrorUnavailable, Message: "load plugin feature flags", Err: err}
	}
	if !s.ContributionActive(prepared, installation, requiresFlag, userID) {
		return prepared, pluginErrf(PluginErrorContributionDisabled, PluginContributionDisabled)
	}
	return prepared, nil
}

// SurfaceActive and ResourceActive also resolve the declared contribution so an
// unknown key cannot bypass a gate by supplying an empty requires_flag value.
func (s *PluginService) SurfaceActive(ctx context.Context, installation db.PluginInstallation, key, userID string) bool {
	manifest, err := ParseInstallationManifest(installation)
	if err != nil {
		return false
	}
	for _, surface := range manifest.Contributes.Surfaces {
		if surface.Key == key {
			return s.ContributionActive(ctx, installation, surface.RequiresFlag, userID)
		}
	}
	return false
}
func (s *PluginService) ResourceActive(ctx context.Context, installation db.PluginInstallation, key string) bool {
	manifest, err := ParseInstallationManifest(installation)
	if err != nil {
		return false
	}
	for _, resource := range manifest.Contributes.Resources {
		if resource.Key == key {
			return s.ContributionActive(ctx, installation, resource.RequiresFlag, "")
		}
	}
	return false
}

// SkillActive is a fail-closed convenience gate. Task/claim assembly uses
// ResolveSkillActive so infrastructure failures cannot yield a partial skillset.
func (s *PluginService) SkillActive(ctx context.Context, skill db.Skill) bool {
	active, err := s.ResolveSkillActive(ctx, skill)
	return err == nil && active
}

// ResolveSkillActive distinguishes disabled/missing contributions from failures
// loading the installation or workspace flag snapshot. User-owned skills pass.
func (s *PluginService) ResolveSkillActive(ctx context.Context, skill db.Skill) (bool, error) {
	if !skill.PluginInstallationID.Valid {
		return true, nil
	}
	installation, err := s.Queries.GetPluginInstallation(ctx, skill.PluginInstallationID)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("load skill plugin installation: %w", err)
	}
	if !installation.Enabled {
		return false, nil
	}
	manifest, err := ParseInstallationManifest(installation)
	if err != nil {
		return false, err
	}
	for _, resource := range manifest.Contributes.Resources {
		if resource.Type != "skill" || resource.Key != skill.Name {
			continue
		}
		prepared, err := s.flagResolver().Prepare(ctx, uuidString(installation.WorkspaceID), "")
		if err != nil {
			return false, fmt.Errorf("load skill plugin feature flags: %w", err)
		}
		return s.ContributionActive(prepared, installation, resource.RequiresFlag, ""), nil
	}
	return false, nil
}

func validatePluginHostFlags(manifest plugincontract.Manifest) error {
	return manifest.ValidateFlagReferences(featureflags.HostRegistry().PublicKeys())
}

func (s *PluginService) invalidatePluginFlags(workspaceID pgtype.UUID) {
	if s.FlagResolver != nil {
		s.FlagResolver.Invalidate(uuidString(workspaceID))
	}
}

// clearPluginFlagOverrides retains the append-only audit while removing both
// workspace and member rows. Remaining definitions retain their own overrides.
func clearPluginFlagOverrides(ctx context.Context, queries *db.Queries, installation db.PluginInstallation, keep map[string]bool, reason string) error {
	rows, err := queries.ListFlagOverridesByPrefix(ctx, db.ListFlagOverridesByPrefixParams{WorkspaceID: installation.WorkspaceID, Prefix: PluginFlagKey(installation.PluginKey, "")})
	if err != nil {
		return err
	}
	for _, row := range rows {
		if keep[row.FlagKey] {
			continue
		}
		err = queries.DeleteFlagOverride(ctx, db.DeleteFlagOverrideParams{WorkspaceID: row.WorkspaceID, ScopeType: row.ScopeType, ScopeID: row.ScopeID, FlagKey: row.FlagKey})
		if err != nil {
			return err
		}
		_, err = queries.InsertFlagAudit(ctx, db.InsertFlagAuditParams{WorkspaceID: row.WorkspaceID, FlagKey: row.FlagKey, ScopeType: row.ScopeType, ScopeID: row.ScopeID, Action: "clear", OldEnabled: pgtype.Bool{Bool: row.Enabled, Valid: true}, OldLocked: pgtype.Bool{Bool: row.Locked, Valid: true}, ActorType: "system", Reason: pgtype.Text{String: reason, Valid: true}})
		if err != nil {
			return fmt.Errorf("clear plugin flag overrides: %w", err)
		}
	}
	return nil
}

func pluginUserFromContext(ctx context.Context) string {
	return featureflag.EvalContextFrom(ctx).UserID
}
