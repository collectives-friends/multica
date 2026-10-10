package featureflag

import "context"

// LookupLayer separates operator overrides from targeting rules without
// changing IsEnabled or Variant. Workspace resolution inserts its overrides
// between these layers. Unknown custom providers retain deployment precedence.
func (s *Service) LookupLayer(ctx context.Context, key string, deployment bool) (Decision, bool) {
	if s == nil {
		return Decision{}, false
	}
	return lookupLayer(ctx, s.provider, key, deployment)
}
func lookupLayer(ctx context.Context, p Provider, key string, deployment bool) (Decision, bool) {
	if p == nil {
		return Decision{}, false
	}
	if chain, ok := p.(*ChainProvider); ok {
		for _, child := range chain.Providers() {
			if d, found := lookupLayer(ctx, child, key, deployment); found {
				return d, true
			}
		}
		return Decision{}, false
	}
	targeted := false
	if static, ok := p.(*StaticProvider); ok {
		static.mu.RLock()
		rule, exists := static.rules[key]
		static.mu.RUnlock()
		if !exists {
			return Decision{}, false
		}
		targeted = len(rule.Allow) > 0 || len(rule.Deny) > 0 || rule.Percent != nil
		if !targeted && rule.DefaultUnset && rule.Variant == "" {
			return Decision{}, false
		}
		if deployment == targeted {
			return Decision{}, false
		}
		// Classify and evaluate the same snapshot across concurrent reloads.
		return evaluateRule(key, rule, EvalContextFrom(ctx)), true
	}
	if deployment == targeted {
		return Decision{}, false
	}
	return p.Lookup(ctx, key)
}
