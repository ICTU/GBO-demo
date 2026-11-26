// Package democontrol holds the demo's controls over the issuer: batch
// settings that the developer portal changes at runtime. It is not part of
// the issuer itself. The core only knows the issuance.Settings port; a
// real deployment uses issuance.ConfiguredSettings and leaves this package
// out.
package democontrol

import (
	"errors"
	"fmt"
	"sort"
	"sync"

	"gbo-demo/eudi-credential-issuer/internal/issuance"
)

// ErrUnknownProvider and ErrUnknownConfiguration name a provider or
// credential configuration the issuer does not have.
var (
	ErrUnknownProvider      = errors.New("unknown provider")
	ErrUnknownConfiguration = errors.New("unknown credential configuration")
)

// Overrides are settings laid over those of the configuration, in memory:
// a restart brings back the configuration's.
type Overrides struct {
	base      issuance.Settings
	providers []string
	configs   []issuance.CredentialConfiguration

	mu       sync.RWMutex
	batch    map[string]int
	policies map[string]issuance.ReusePolicy
}

// NewOverrides returns overrides over base for the providers and
// credential configurations of cfg, with none set.
func NewOverrides(cfg issuance.Config, base issuance.Settings) *Overrides {
	o := &Overrides{base: base, batch: map[string]int{}, policies: map[string]issuance.ReusePolicy{}}
	for _, p := range cfg.Providers {
		o.providers = append(o.providers, p.Name)
	}
	sort.Strings(o.providers)
	o.configs = append(o.configs, cfg.Configurations...)
	sort.Slice(o.configs, func(i, j int) bool { return o.configs[i].ID < o.configs[j].ID })
	return o
}

// BatchSize implements issuance.Settings.
func (o *Overrides) BatchSize(provider string) int {
	o.mu.RLock()
	n, ok := o.batch[provider]
	o.mu.RUnlock()
	if ok {
		return n
	}
	return o.base.BatchSize(provider)
}

// ReusePolicy implements issuance.Settings.
func (o *Overrides) ReusePolicy(configurationID string) (issuance.ReusePolicy, bool) {
	o.mu.RLock()
	p, ok := o.policies[configurationID]
	o.mu.RUnlock()
	if ok {
		return p, true
	}
	return o.base.ReusePolicy(configurationID)
}

// SetBatchSize sets the batch size of a provider; the issuer decides what
// is valid.
func (o *Overrides) SetBatchSize(provider string, n int) error {
	if !o.hasProvider(provider) {
		return fmt.Errorf("%w: %s", ErrUnknownProvider, provider)
	}
	if err := issuance.ValidateBatchSize(n); err != nil {
		return err
	}
	o.mu.Lock()
	defer o.mu.Unlock()
	o.batch[provider] = n
	return nil
}

// ResetBatchSize gives a provider the batch size of the configuration back.
func (o *Overrides) ResetBatchSize(provider string) error {
	if !o.hasProvider(provider) {
		return fmt.Errorf("%w: %s", ErrUnknownProvider, provider)
	}
	o.mu.Lock()
	defer o.mu.Unlock()
	delete(o.batch, provider)
	return nil
}

// SetReusePolicy sets the reuse policy of a credential configuration; the
// issuer decides what is valid.
func (o *Overrides) SetReusePolicy(configurationID string, p issuance.ReusePolicy) error {
	if _, ok := o.config(configurationID); !ok {
		return fmt.Errorf("%w: %s", ErrUnknownConfiguration, configurationID)
	}
	if err := p.Validate(); err != nil {
		return err
	}
	o.mu.Lock()
	defer o.mu.Unlock()
	o.policies[configurationID] = p
	return nil
}

// ResetReusePolicy gives a credential configuration the reuse policy of the
// configuration back.
func (o *Overrides) ResetReusePolicy(configurationID string) error {
	if _, ok := o.config(configurationID); !ok {
		return fmt.Errorf("%w: %s", ErrUnknownConfiguration, configurationID)
	}
	o.mu.Lock()
	defer o.mu.Unlock()
	delete(o.policies, configurationID)
	return nil
}

// ProviderSettings is the batch size in force for a provider.
type ProviderSettings struct {
	Name      string `json:"name"`
	BatchSize int    `json:"batch_size"`
	// Overridden tells that the batch size is set here, not configured.
	Overridden bool `json:"overridden"`
}

// ConfigurationSettings is the reuse policy in force for a credential
// configuration.
type ConfigurationSettings struct {
	ID       string `json:"id"`
	Provider string `json:"provider"`
	// Name is the first display name of the configuration.
	Name        string       `json:"name"`
	ReusePolicy *reusePolicy `json:"reuse_policy"`
	// BatchSize is the batch size in force for the EUDI wallets: the
	// policy's, or the provider's.
	BatchSize  int  `json:"batch_size"`
	Overridden bool `json:"overridden"`
}

// View is every setting in force.
type View struct {
	Providers      []ProviderSettings      `json:"providers"`
	Configurations []ConfigurationSettings `json:"configurations"`
}

// View returns the settings in force.
func (o *Overrides) View() View {
	o.mu.RLock()
	defer o.mu.RUnlock()
	v := View{Providers: []ProviderSettings{}, Configurations: []ConfigurationSettings{}}
	for _, name := range o.providers {
		n, overridden := o.batch[name]
		if !overridden {
			n = o.base.BatchSize(name)
		}
		v.Providers = append(v.Providers, ProviderSettings{Name: name, BatchSize: n, Overridden: overridden})
	}
	for _, c := range o.configs {
		cs := ConfigurationSettings{ID: c.ID, Provider: c.Provider}
		if len(c.Display) > 0 {
			cs.Name = c.Display[0].Name
		}
		p, overridden := o.policies[c.ID]
		ok := overridden
		if !ok {
			p, ok = o.base.ReusePolicy(c.ID)
		}
		cs.Overridden = overridden
		if ok {
			cs.ReusePolicy = fromPolicy(p)
			cs.BatchSize = p.BatchSize
		} else if n, set := o.batch[c.Provider]; set {
			cs.BatchSize = n
		} else {
			cs.BatchSize = o.base.BatchSize(c.Provider)
		}
		v.Configurations = append(v.Configurations, cs)
	}
	return v
}

func (o *Overrides) hasProvider(name string) bool {
	i := sort.SearchStrings(o.providers, name)
	return i < len(o.providers) && o.providers[i] == name
}

func (o *Overrides) config(id string) (issuance.CredentialConfiguration, bool) {
	for _, c := range o.configs {
		if c.ID == id {
			return c, true
		}
	}
	return issuance.CredentialConfiguration{}, false
}
