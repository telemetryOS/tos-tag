package modelrouter

import (
	"context"
	"fmt"
	"sort"
	"sync"

	"github.com/telemetryos/tos-tag/types"
)

type Snapshot struct {
	PolicyRevision    string               `json:"policy_revision"`
	DeploymentDefault string               `json:"deployment_default"`
	Profiles          []types.ModelProfile `json:"profiles"`
	Rules             []Rule               `json:"rules"`
}
type Registry struct {
	mu                   sync.RWMutex
	profiles             map[string]types.ModelProfile
	rules                map[string]Rule
	organizationDefaults map[string]string
	deploymentDefault    string
	policyRevision       string
	router               *Router
	store                Store
}

type Store interface {
	Load(context.Context) ([]types.ModelProfile, []Rule, error)
	PutProfile(context.Context, types.ModelProfile) error
	PutRule(context.Context, Rule) error
}

func NewRegistry(profiles []types.ModelProfile, rules []Rule, defaults map[string]string, deploymentDefault, revision string) (*Registry, error) {
	r := &Registry{profiles: make(map[string]types.ModelProfile), rules: make(map[string]Rule), organizationDefaults: make(map[string]string), deploymentDefault: deploymentDefault, policyRevision: revision}
	for _, p := range profiles {
		r.profiles[p.ID] = p
	}
	for _, rule := range rules {
		r.rules[rule.ID] = rule
	}
	for k, v := range defaults {
		r.organizationDefaults[k] = v
	}
	if err := r.rebuild(); err != nil {
		return nil, err
	}
	return r, nil
}
func (r *Registry) Resolve(ctx context.Context, route types.ModelRouteContext, constraints Constraints) (types.ResolvedModel, types.DecisionTrace, error) {
	r.mu.RLock()
	router := r.router
	r.mu.RUnlock()
	return router.Resolve(ctx, route, constraints)
}

func (r *Registry) AttachStore(store Store) { r.mu.Lock(); r.store = store; r.mu.Unlock() }

func (r *Registry) Load(ctx context.Context) error {
	r.mu.RLock()
	store := r.store
	r.mu.RUnlock()
	if store == nil {
		return nil
	}
	profiles, rules, err := store.Load(ctx)
	if err != nil {
		return err
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, profile := range profiles {
		r.profiles[profile.ID] = profile
	}
	for _, rule := range rules {
		r.rules[rule.ID] = rule
	}
	return r.rebuildLocked()
}
func (r *Registry) Allowed(snapshot types.ResolvedModel) bool {
	r.mu.RLock()
	defer r.mu.RUnlock()
	profile, ok := r.profiles[snapshot.ProfileID]
	return ok && profile.Enabled && profile.ProviderID == snapshot.ProviderID && profile.ModelID == snapshot.ModelID && profile.Variant == snapshot.Variant
}
func (r *Registry) PutProfile(profile types.ModelProfile) error {
	return r.PutProfileContext(context.Background(), profile)
}

func (r *Registry) PutProfileContext(ctx context.Context, profile types.ModelProfile) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	previous, exists := r.profiles[profile.ID]
	r.profiles[profile.ID] = profile
	if err := r.rebuildLocked(); err != nil {
		if exists {
			r.profiles[profile.ID] = previous
		} else {
			delete(r.profiles, profile.ID)
		}
		_ = r.rebuildLocked()
		return err
	}
	if r.store != nil {
		if err := r.store.PutProfile(ctx, profile); err != nil {
			if exists {
				r.profiles[profile.ID] = previous
			} else {
				delete(r.profiles, profile.ID)
			}
			_ = r.rebuildLocked()
			return err
		}
	}
	return nil
}
func (r *Registry) PutRule(rule Rule) error {
	return r.PutRuleContext(context.Background(), rule)
}

func (r *Registry) PutRuleContext(ctx context.Context, rule Rule) error {
	if rule.ID == "" || rule.ProfileID == "" {
		return fmt.Errorf("rule ID and profile are required")
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	previous, exists := r.rules[rule.ID]
	r.rules[rule.ID] = rule
	if err := r.rebuildLocked(); err != nil {
		if exists {
			r.rules[rule.ID] = previous
		} else {
			delete(r.rules, rule.ID)
		}
		_ = r.rebuildLocked()
		return err
	}
	if r.store != nil {
		if err := r.store.PutRule(ctx, rule); err != nil {
			if exists {
				r.rules[rule.ID] = previous
			} else {
				delete(r.rules, rule.ID)
			}
			_ = r.rebuildLocked()
			return err
		}
	}
	return nil
}
func (r *Registry) SetOrganizationDefault(org, profile string) error {
	if org == "" || profile == "" {
		return fmt.Errorf("organization and profile are required")
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	old, exists := r.organizationDefaults[org]
	r.organizationDefaults[org] = profile
	if err := r.rebuildLocked(); err != nil {
		if exists {
			r.organizationDefaults[org] = old
		} else {
			delete(r.organizationDefaults, org)
		}
		_ = r.rebuildLocked()
		return err
	}
	return nil
}
func (r *Registry) Snapshot() Snapshot {
	r.mu.RLock()
	defer r.mu.RUnlock()
	snapshot := Snapshot{PolicyRevision: r.policyRevision, DeploymentDefault: r.deploymentDefault}
	for _, p := range r.profiles {
		snapshot.Profiles = append(snapshot.Profiles, p)
	}
	for _, rule := range r.rules {
		snapshot.Rules = append(snapshot.Rules, rule)
	}
	sort.Slice(snapshot.Profiles, func(i, j int) bool { return snapshot.Profiles[i].ID < snapshot.Profiles[j].ID })
	sort.Slice(snapshot.Rules, func(i, j int) bool { return snapshot.Rules[i].ID < snapshot.Rules[j].ID })
	return snapshot
}
func (r *Registry) rebuild() error { r.mu.Lock(); defer r.mu.Unlock(); return r.rebuildLocked() }
func (r *Registry) rebuildLocked() error {
	profiles := make([]types.ModelProfile, 0, len(r.profiles))
	for _, p := range r.profiles {
		profiles = append(profiles, p)
	}
	rules := make([]Rule, 0, len(r.rules))
	for _, rule := range r.rules {
		if _, ok := r.profiles[rule.ProfileID]; !ok {
			return fmt.Errorf("rule %s references missing profile %s", rule.ID, rule.ProfileID)
		}
		rules = append(rules, rule)
	}
	router, err := New(profiles, rules, r.organizationDefaults, r.deploymentDefault, r.policyRevision)
	if err != nil {
		return err
	}
	r.router = router
	return nil
}
