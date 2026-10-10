package featureflags

import (
	"context"
	"log/slog"
	"sync"
	"time"

	"github.com/multica-ai/multica/server/pkg/featureflag"
)

type Override struct {
	Enabled bool `json:"enabled"`
	Locked  bool `json:"locked"`
}
type Decision struct {
	Value  bool   `json:"value"`
	Source string `json:"source"`
	Locked bool   `json:"locked"`
}
type Overrides struct {
	Workspace map[string]Override `json:"workspace"`
	Member    map[string]bool     `json:"member"`
}
type View struct {
	Definitions []Definition        `json:"definitions"`
	Resolved    map[string]Decision `json:"resolved"`
	Overrides   Overrides           `json:"overrides"`
}

// Source returns only workspace overrides and this actor's own member rows.
type Source interface {
	LoadWorkspace(context.Context, string) (map[string]Override, []Definition, error)
	LoadMember(context.Context, string, string) (map[string]bool, error)
}
type state struct {
	workspaceID, userID string
	registry            *Registry
	overrides           Overrides
}
type stateKey struct{}
type cachedState struct {
	state   *state
	expires time.Time
}

// Resolver owns resolution for requests and background work. No client resolves
// parents or override precedence independently.
type Resolver struct {
	Registry   *Registry
	Deployment *featureflag.Service
	source     Source
	mu         sync.Mutex
	cache      map[[2]string]cachedState
	generation uint64
	unknown    map[string]bool
}

func NewResolver(registry *Registry, deployment *featureflag.Service, source Source) *Resolver {
	return &Resolver{Registry: registry, Deployment: deployment, source: source, cache: map[[2]string]cachedState{}, unknown: map[string]bool{}}
}
func (r *Resolver) Invalidate(workspaceID string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.generation++
	for key := range r.cache {
		if key[0] == workspaceID {
			delete(r.cache, key)
		}
	}
}
func (r *Resolver) load(ctx context.Context, w, u string) (*state, error) {
	workspace := map[string]Override{}
	member := map[string]bool{}
	var plugins []Definition
	var err error
	if r.source != nil && w != "" {
		workspace, plugins, err = r.source.LoadWorkspace(ctx, w)
		if err != nil {
			return nil, err
		}
		if u != "" {
			member, err = r.source.LoadMember(ctx, w, u)
			if err != nil {
				return nil, err
			}
		}
	}
	registry := r.Registry
	if len(plugins) > 0 {
		registry, err = NewRegistry(append(r.Registry.Definitions(), plugins...))
		if err != nil {
			return nil, err
		}
	}
	if workspace == nil {
		workspace = map[string]Override{}
	}
	if member == nil {
		member = map[string]bool{}
	}
	return &state{w, u, registry, Overrides{workspace, member}}, nil
}
func (r *Resolver) get(ctx context.Context, w, u string) (*state, error) {
	if s, ok := ctx.Value(stateKey{}).(*state); ok && s.workspaceID == w && (s.userID == u || u == "") {
		return s, nil
	}
	key := [2]string{w, u}
	r.mu.Lock()
	cached, ok := r.cache[key]
	generation := r.generation
	r.mu.Unlock()
	if ok && time.Now().Before(cached.expires) {
		return cached.state, nil
	}
	s, err := r.load(ctx, w, u)
	if err != nil {
		return nil, err
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if generation == r.generation {
		if len(r.cache) >= 1024 {
			clear(r.cache)
		}
		r.cache[key] = cachedState{s, time.Now().Add(2 * time.Second)}
	}
	return s, nil
}

// Prepare bypasses the background cache; each authenticated request gets one
// fresh pair of override queries, reused by all evaluations in that request.
func (r *Resolver) Prepare(ctx context.Context, w, u string) (context.Context, error) {
	s, err := r.load(ctx, w, u)
	if err != nil {
		return ctx, err
	}
	ctx = featureflag.WithEvalContext(ctx, featureflag.EvalContext{WorkspaceID: w, UserID: u})
	return context.WithValue(ctx, stateKey{}, s), nil
}
func (r *Resolver) Enabled(ctx context.Context, key string) bool {
	ec := featureflag.EvalContextFrom(ctx)
	return r.EnabledFor(ctx, ec.WorkspaceID, ec.UserID, key)
}
func (r *Resolver) EnabledFor(ctx context.Context, w, u, key string) bool {
	return r.Explain(ctx, w, u, key).Value
}
func (r *Resolver) ResolveWorkspace(ctx context.Context, w, key string) Decision {
	return r.Explain(ctx, w, "", key)
}
func (r *Resolver) Explain(ctx context.Context, w, u, key string, fallback ...bool) Decision {
	def := len(fallback) > 0 && fallback[0]
	if r == nil {
		return Decision{def, "default", false}
	}
	if definition, ok := r.Registry.Definition(key); ok && definition.Frozen != nil {
		return Decision{*definition.Frozen, "frozen", true}
	}
	s, err := r.get(ctx, w, u)
	if err != nil {
		slog.ErrorContext(ctx, "feature flag state unavailable", "workspace_id", w)
		return Decision{false, "error", true}
	}
	return r.resolve(ctx, s, u, key, def)
}
func (r *Resolver) resolve(ctx context.Context, s *state, u, key string, fallback bool) Decision {
	d, known := s.registry.Definition(key)
	if !known {
		r.mu.Lock()
		logged := r.unknown[key]
		if len(r.unknown) < 1024 {
			r.unknown[key] = true
		}
		r.mu.Unlock()
		if !logged {
			slog.DebugContext(ctx, "unknown feature flag", "key", key)
		}
		return Decision{fallback, "default", false}
	}
	if d.ScopePolicy == WorkspaceOnly {
		u = ""
	}
	if d.Frozen != nil {
		return Decision{*d.Frozen, "frozen", true}
	}
	if d.Parent != "" && !r.resolve(ctx, s, u, d.Parent, false).Value {
		return Decision{false, "parent:" + d.Parent, true}
	}
	ctx = featureflag.WithEvalContext(ctx, featureflag.EvalContext{WorkspaceID: s.workspaceID, UserID: u})
	if value, ok := r.Deployment.LookupLayer(ctx, key, true); ok {
		return Decision{value.Enabled, "deployment", true}
	}
	if o, ok := s.overrides.Workspace[key]; ok && o.Locked {
		return Decision{o.Enabled, "workspace_locked", true}
	}
	if u != "" && d.ScopePolicy == MemberOverridable {
		if value, ok := s.overrides.Member[key]; ok {
			return Decision{value, "member", false}
		}
	}
	if o, ok := s.overrides.Workspace[key]; ok {
		return Decision{o.Enabled, "workspace", false}
	}
	if value, ok := r.Deployment.LookupLayer(ctx, key, false); ok {
		return Decision{value.Enabled, "rule", false}
	}
	return Decision{d.Default, "default", false}
}
func (r *Resolver) Snapshot(ctx context.Context, w, u string) map[string]Decision {
	view, err := r.View(ctx, w, u)
	if err != nil {
		return map[string]Decision{}
	}
	return view.Resolved
}
func (r *Resolver) View(ctx context.Context, w, u string) (View, error) {
	s, err := r.get(ctx, w, u)
	if err != nil {
		return View{}, err
	}
	view := View{Definitions: s.registry.Definitions(), Resolved: map[string]Decision{}, Overrides: s.overrides}
	for _, d := range view.Definitions {
		view.Resolved[d.Key] = r.resolve(ctx, s, u, d.Key, false)
	}
	return view, nil
}
