package sources

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"sync"

	"github.com/multica-ai/multica/server/internal/personalagent/core"
	"github.com/multica-ai/multica/server/internal/personalagent/policy"
)

// Registry holds the installed Source kinds and implements core.SourceScope for the Engine (SA SourceRegistry). It
// claims each kind's health entries and mediates channels, links, search and thread context readers.
type Registry struct {
	settings func() policy.Settings
	board    *HealthBoard
	mu       sync.RWMutex
	kinds    map[string]*Kind
	health   map[string]*HealthEntry
}

// NewRegistry returns a Registry reading settings; a nil board gets a private one.
func NewRegistry(settings func() policy.Settings, board *HealthBoard) *Registry {
	if board == nil {
		board = NewHealthBoard()
	}
	return &Registry{settings: settings, board: board, kinds: map[string]*Kind{}, health: map[string]*HealthEntry{}}
}

// Register installs a kind, claiming its health entries (`sync:` entries belong to Settings), and returns its disposer.
func (r *Registry) Register(kind Kind) (func(), error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, ok := r.kinds[kind.Kind]; ok {
		return nil, fmt.Errorf("Source kind already registered: %s", kind.Kind)
	}
	refused := func(name string) error {
		return fmt.Errorf("Source kind %s declares the health entry %s, which another part reports", kind.Kind, name)
	}
	for _, name := range kind.Health {
		if strings.HasPrefix(name, "sync:") {
			return nil, refused(name)
		}
	}
	entries := map[string]*HealthEntry{}
	release := func() {
		for name, entry := range entries {
			entry.Release()
			if r.health[name] == entry {
				delete(r.health, name)
			}
		}
	}
	for _, name := range kind.Health {
		entry, err := r.board.Claim(name, Health{State: "starting"})
		if err != nil {
			release()
			return nil, refused(name)
		}
		entries[name] = entry
	}
	for name, entry := range entries {
		r.health[name] = entry
	}
	k := kind
	r.kinds[kind.Kind] = &k
	return func() {
		r.mu.Lock()
		defer r.mu.Unlock()
		if r.kinds[kind.Kind] == &k {
			delete(r.kinds, kind.Kind)
		}
		release()
	}, nil
}

// HealthEntries returns the active entries for names; a context built from them ignores reports after unregister.
func (r *Registry) HealthEntries(names []string) map[string]*HealthEntry {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := map[string]*HealthEntry{}
	for _, name := range names {
		if e := r.health[name]; e != nil {
			out[name] = e
		}
	}
	return out
}

// Kind returns an installed kind.
func (r *Registry) Kind(id string) *Kind {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.kinds[id]
}

func (r *Registry) sorted() []*Kind {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]*Kind, 0, len(r.kinds))
	for _, k := range r.kinds {
		out = append(out, k)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Kind < out[j].Kind })
	return out
}

// Instances lists every installed kind's instance in ID order, then stored instances whose kind is not installed.
func (r *Registry) Instances() []Instance {
	s := r.settings()
	out := []Instance{}
	for _, k := range r.sorted() {
		inst := r.resolve(k, s)
		inst.Installed = true
		out = append(out, inst)
	}
	orphans := []string{}
	for id := range s.Sources {
		if r.Kind(id) == nil {
			orphans = append(orphans, id)
		}
	}
	sort.Strings(orphans)
	for _, id := range orphans {
		stored := s.Sources[id]
		out = append(out, Instance{ID: id, Kind: id, Enabled: stored.Enabled, Config: stored.Config})
	}
	return out
}

// Instance returns an installed kind's resolved instance, or nil.
func (r *Registry) Instance(id string) *Instance {
	k := r.Kind(id)
	if k == nil {
		return nil
	}
	inst := r.resolve(k, r.settings())
	return &inst
}

// Validate checks a submitted instance against its kind's schema.
func (r *Registry) Validate(id string, enabled bool, config map[string]any) (bool, Config, error) {
	k := r.Kind(id)
	if k == nil {
		return false, nil, fmt.Errorf("Source kind not installed: %s", id)
	}
	cfg, err := parse(k.Schema, config)
	return enabled, cfg, err
}

// ValidatePolicy validates each Source's part of a submitted Policy with its kind; parts of uninstalled kinds are
// kept as stored. `admission` is validated by the Policy's own table schema for every kind.
func (r *Registry) ValidatePolicy(parts map[string]map[string]any) (map[string]map[string]any, error) {
	out := map[string]map[string]any{}
	for id, part := range parts {
		k := r.Kind(id)
		if k == nil || k.Policy == nil {
			out[id] = part
			continue
		}
		rest := map[string]any{}
		for key, v := range part {
			if key != "admission" {
				rest[key] = v
			}
		}
		parsed, err := parse(k.Policy.Schema, rest)
		if err != nil {
			return nil, fmt.Errorf("%s policy: %w", id, err)
		}
		table, err := policy.AdmissionTable(part["admission"])
		if err != nil {
			return nil, fmt.Errorf("%s policy: %w", id, err)
		}
		parsed["admission"] = tableMap(table)
		out[id] = parsed
	}
	return out, nil
}

// PolicyPart returns a kind's resolved part of the stored Policy, or nil for a kind without one.
func (r *Registry) PolicyPart(id string) map[string]any {
	k := r.Kind(id)
	if k == nil || k.Policy == nil {
		return nil
	}
	stored := r.settings().Policy.Sources[id]
	raw := map[string]any{}
	if stored.Allowed != nil {
		raw["allowed"] = toAny(stored.Allowed)
	}
	if stored.Bots != nil {
		raw["bots"] = toAny(stored.Bots)
	}
	parsed, err := parse(k.Policy.Schema, raw)
	if err != nil {
		parsed, _ = parse(k.Policy.Schema, map[string]any{})
	}
	parsed["admission"] = tableMap(stored.Admission)
	return parsed
}

// ChannelAllowed reports whether a reply may still go to a Channel: its kind is installed, enabled and allows the address.
func (r *Registry) ChannelAllowed(source string, address map[string]string) bool {
	k := r.Kind(source)
	if k == nil {
		return false
	}
	inst := r.resolve(k, r.settings())
	if !inst.Enabled {
		return false
	}
	if k.Channel == nil || k.Channel.Allowed == nil {
		return true
	}
	return k.Channel.Allowed(address, inst.Config)
}

// Send delivers a message through its Channel's kind: Waiting while the kind is not installed, Gone for a kind
// without a channel.
func (r *Registry) Send(ctx context.Context, message core.OutboundMessage) (string, error) {
	k := r.Kind(message.Channel.Source)
	if k == nil {
		return Waiting, nil
	}
	if k.Channel == nil {
		return Gone, nil
	}
	return k.Channel.Send(ctx, message)
}

// Link returns the panel link for a channel; an uninstalled kind's has no page.
func (r *Registry) Link(channel core.ChannelRef, links ChannelLinks) ChannelLink {
	if k := r.Kind(channel.Source); k != nil && k.Channel != nil && k.Channel.Link != nil {
		return k.Channel.Link(channel.Address, links)
	}
	return ChannelLink{Label: Localized{Zh: channel.Source, En: channel.Source}}
}

// Find merges what every kind finds in a search query.
func (r *Registry) Find(query string) Found {
	found := Found{Threads: []string{}, Terms: []string{}}
	seenT, seenS := map[string]bool{}, map[string]bool{}
	for _, k := range r.sorted() {
		if k.Find == nil {
			continue
		}
		f := k.Find(query)
		for _, t := range f.Threads {
			if !seenT[t] {
				seenT[t] = true
				found.Threads = append(found.Threads, t)
			}
		}
		for _, t := range f.Terms {
			if !seenS[t] {
				seenS[t] = true
				found.Terms = append(found.Terms, t)
			}
		}
	}
	return found
}

// RepliesTo names the Trigger whose message a Trigger answers, where its kind can tell without a read.
func (r *Registry) RepliesTo(trigger core.Trigger) string {
	if k := r.Kind(trigger.Source); k != nil && k.RepliesTo != nil {
		return k.RepliesTo(trigger)
	}
	return ""
}

// ThreadContext binds a Trigger to its kind's context reader, or returns nil for a kind without one.
func (r *Registry) ThreadContext(trigger core.Trigger) *TriggerContext {
	k := r.Kind(trigger.Source)
	if k == nil || k.ThreadContext == nil {
		return nil
	}
	config := r.resolve(k, r.settings()).Config
	tc := k.ThreadContext
	return &TriggerContext{
		MessageID: tc.MessageID(trigger),
		Read: func(ctx context.Context, part, cursor string) (ContextPart, error) {
			return tc.Read(ctx, trigger, part, cursor, config)
		},
	}
}

// Scope implements core.SourceScope. A configured Source whose kind is missing listens to nothing.
func (r *Registry) Scope(source string, meta map[string]any) core.ScopeResult {
	s := r.settings()
	k := r.Kind(source)
	if k == nil {
		if _, stored := s.Sources[source]; stored {
			return core.ScopeResult{Reason: "source_not_installed"}
		}
		return core.ScopeResult{Listened: true}
	}
	inst := r.resolve(k, s)
	if !inst.Enabled {
		return core.ScopeResult{Reason: "source_disabled"}
	}
	if k.Scope == nil {
		return core.ScopeResult{Listened: true}
	}
	return k.Scope(meta, inst.Config)
}

// resolve falls back to schema defaults and disables the instance when the stored configuration is invalid.
func (r *Registry) resolve(k *Kind, s policy.Settings) Instance {
	stored, ok := s.Sources[k.Kind]
	raw := map[string]any{}
	if ok && stored.Config != nil {
		raw = stored.Config
	}
	cfg, err := parse(k.Schema, raw)
	enabled := err == nil && (!ok || stored.Enabled)
	if err != nil {
		cfg, _ = parse(k.Schema, map[string]any{})
	}
	return Instance{ID: k.Kind, Kind: k.Kind, Enabled: enabled, Config: cfg}
}

func parse(schema Schema, raw map[string]any) (Config, error) {
	if schema == nil {
		return Config{}, nil
	}
	return schema(raw)
}

func tableMap(table map[string]policy.AdmissionRow) map[string]any {
	out := map[string]any{}
	for row, cells := range table {
		out[row] = toAny(cells)
	}
	return out
}

func toAny(v any) any {
	data, _ := json.Marshal(v)
	var out any
	_ = json.Unmarshal(data, &out)
	return out
}

type senderKey struct{}

// WithSender carries the agent a message is sent as (its Router agent) to a Channel's Send.
func WithSender(ctx context.Context, agentID string) context.Context {
	return context.WithValue(ctx, senderKey{}, agentID)
}

// SenderOf is the agent WithSender carried, or "".
func SenderOf(ctx context.Context) string {
	s, _ := ctx.Value(senderKey{}).(string)
	return s
}
