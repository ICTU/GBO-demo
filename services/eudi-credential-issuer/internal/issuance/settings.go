package issuance

import (
	"errors"
	"fmt"
)

// ReuseOnceOnly is the once_only method of the ARF Annex II reuse policy
// (ETSI TS 119 472-3 clause 4.2.4.2, ARF ISSU_43 to ISSU_47): the wallet
// presents each credential once, and asks for a new batch when fewer than
// ReissueTriggerUnused are left unused.
const ReuseOnceOnly = "once_only"

// reusePolicyARFAnnexII is the id of the reuse policy of ARF Annex II.
const reusePolicyARFAnnexII = "arf_annex_ii"

// ReusePolicy is the reuse policy of a credential configuration, published
// as credential_metadata.credential_reuse_policy (ETSI TS 119 472-3 clause
// 4.2.4). Only the once_only method is supported.
type ReusePolicy struct {
	Method string
	// BatchSize is the maximum number of keys, and so of credentials, per
	// credential request.
	BatchSize int
	// ReissueTriggerUnused is the number of unused credentials at which the
	// wallet asks for a new batch (once_only).
	ReissueTriggerUnused int
}

// Validate checks p against ETSI TS 119 472-3 clauses 4.2.4.2-07 to
// 4.2.4.2-11.
func (p ReusePolicy) Validate() error {
	if p.Method != ReuseOnceOnly {
		return fmt.Errorf("reuse method %q is not supported; only %s is", p.Method, ReuseOnceOnly)
	}
	if p.BatchSize < 2 {
		return errors.New("the batch size of a reuse policy must be 2 or more")
	}
	if p.ReissueTriggerUnused < 0 || p.ReissueTriggerUnused >= p.BatchSize {
		return errors.New("the reissue trigger must be 0 or more and lower than the batch size")
	}
	return nil
}

// metadata is p as credential_reuse_policy.
func (p ReusePolicy) metadata() map[string]any {
	return map[string]any{
		"id": reusePolicyARFAnnexII,
		"options": []map[string]any{{
			"details":                []string{p.Method},
			"batch_size":             p.BatchSize,
			"reissue_trigger_unused": p.ReissueTriggerUnused,
		}},
	}
}

// ValidateBatchSize checks the batch size of a provider: 1 or more, where 1
// means no batch issuance.
func ValidateBatchSize(n int) error {
	if n < 1 {
		return errors.New("the batch size must be 1 or more")
	}
	return nil
}

// Settings supplies the batch settings in force. The issuer asks on every
// metadata and credential request, so a change applies from the next
// request on; credentials already issued are not affected.
type Settings interface {
	// BatchSize is the batch size of a provider, published as
	// batch_credential_issuance when it is 2 or more.
	BatchSize(provider string) int
	// ReusePolicy returns the reuse policy of a credential configuration,
	// if it has one.
	ReusePolicy(configurationID string) (ReusePolicy, bool)
}

// ConfiguredSettings are the settings of cfg: its batch size, or the
// default, for every provider, and no reuse policies.
func ConfiguredSettings(cfg Config) Settings {
	if cfg.BatchSize == 0 {
		cfg.BatchSize = DefaultBatchSize
	}
	return configuredSettings{batchSize: cfg.BatchSize}
}

type configuredSettings struct{ batchSize int }

func (s configuredSettings) BatchSize(string) int { return s.batchSize }

func (configuredSettings) ReusePolicy(string) (ReusePolicy, bool) { return ReusePolicy{}, false }

// reusePolicy returns the reuse policy of c a wallet on p is told about:
// only the profiles that serve the EUDI wallets publish it, and on a
// combined profile the NL Wallet does not read it.
func (s *Service) reusePolicy(p Profile, c CredentialConfiguration, toNLWallet bool) (ReusePolicy, bool) {
	if !p.servesEUDI() || toNLWallet {
		return ReusePolicy{}, false
	}
	return s.cfg.Settings.ReusePolicy(c.ID)
}

// batchSize is the batch size in force for c on p, and where it comes
// from: its reuse policy, if the wallet is told about one (ETSI TS 119
// 472-3 clause 4.2.4.2-14), the provider otherwise.
func (s *Service) batchSize(p Profile, c CredentialConfiguration, toNLWallet bool) (int, string) {
	if policy, ok := s.reusePolicy(p, c, toNLWallet); ok {
		return policy.BatchSize, BatchSizeFromReusePolicy
	}
	return s.cfg.Settings.BatchSize(c.Provider), BatchSizeFromProvider
}
