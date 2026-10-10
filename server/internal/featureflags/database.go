package featureflags

import (
	"context"
	"github.com/multica-ai/multica/server/internal/util"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

// DBSource leaves plugin snapshot interpretation with the plugin subsystem.
// The callback must use the same Queries (including its transaction).
type DBSource struct {
	Queries           *db.Queries
	PluginDefinitions func(context.Context, string) ([]Definition, error)
}

func (s DBSource) LoadWorkspace(ctx context.Context, w string) (map[string]Override, []Definition, error) {
	id, err := util.ParseUUID(w)
	if err != nil {
		return nil, nil, err
	}
	rows, err := s.Queries.ListWorkspaceFlagOverrides(ctx, id)
	if err != nil {
		return nil, nil, err
	}
	out := map[string]Override{}
	for _, row := range rows {
		out[row.FlagKey] = Override{row.Enabled, row.Locked}
	}
	var defs []Definition
	if s.PluginDefinitions != nil {
		defs, err = s.PluginDefinitions(ctx, w)
	}
	return out, defs, err
}
func (s DBSource) LoadMember(ctx context.Context, w, u string) (map[string]bool, error) {
	wid, err := util.ParseUUID(w)
	if err != nil {
		return nil, err
	}
	uid, err := util.ParseUUID(u)
	if err != nil {
		return nil, err
	}
	rows, err := s.Queries.ListMemberFlagOverrides(ctx, db.ListMemberFlagOverridesParams{WorkspaceID: wid, ScopeID: uid})
	if err != nil {
		return nil, err
	}
	out := map[string]bool{}
	for _, row := range rows {
		out[row.FlagKey] = row.Enabled
	}
	return out, nil
}
