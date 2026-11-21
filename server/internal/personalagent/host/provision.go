package host

import (
	"context"

	"github.com/multica-ai/multica/server/internal/personalagent/policy"
	"github.com/multica-ai/multica/server/internal/personalagent/sources/multica"
)

// Provision brings an agent in line with its owner: the owner is on the multica Source's allowed list, an unset
// Router is filled from the Router template, and the rules follow the template's rule set.
func (h *Host) Provision(ctx context.Context, a *Agent) error {
	name := ""
	if h.options.OwnerName != nil {
		name = h.options.OwnerName(ctx, a.UserID)
	}
	router := ""
	if h.options.PersonalRouter != nil && a.RouterAgentID == "" {
		router = h.options.PersonalRouter(ctx, a.WorkspaceID, a.UserID)
	}
	var rules []policy.Rule
	var origins map[string]policy.RuleOrigin
	syncRules := false
	if h.options.TemplateRules != nil {
		if version, template, ok := h.options.TemplateRules(ctx); ok {
			rules, origins, syncRules = policy.SyncTemplateRules(a.Settings.Policy.Rules, a.Store.RuleOrigins(), version, template)
		}
	}
	a.Store.Atomic(func() {
		if syncRules {
			a.Store.SyncRules(rules, origins)
		}
		a.Store.EnsureAllowed(multica.Source, a.UserID, name)
		a.Store.FillRouter(router)
	})
	return nil
}

// RefreshLabels reloads the names a Source's rows choose from; no remaining Source has such a list.
func (h *Host) RefreshLabels(ctx context.Context, a *Agent, source string) ([]string, error) {
	return nil, ErrNoLabels
}

// ErrNoLabels refuses a refresh of a Source whose rows have nothing to choose from.
var ErrNoLabels = errNoLabels{}

type errNoLabels struct{}

func (errNoLabels) Error() string { return "This Source has no list to refresh" }
