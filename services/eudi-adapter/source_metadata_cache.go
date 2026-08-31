package main

import (
	"fmt"
	"strconv"
	"strings"
	"time"
)

var defaultSourceMetadataCachePolicy = sourceMetadataCachePolicy{
	MinimumValidity:  time.Hour,
	MaximumFreshness: 15 * time.Minute,
	StaleGrace:       time.Hour,
}

const activationReloadRetryInterval = 5 * time.Second

type sourceMetadataCachePolicy struct {
	MinimumValidity  time.Duration
	MaximumFreshness time.Duration
	StaleGrace       time.Duration
}

func validateSourceMetadataEnvelope(document sourceMetadataDocument, definition sourceAttestationDefinition, now time.Time, policy sourceMetadataCachePolicy) (time.Time, error) {
	issuedAt, err := time.Parse(time.RFC3339, document.IssuedAt)
	if err != nil {
		return time.Time{}, fmt.Errorf("source metadata issued_at is invalid: %w", err)
	}
	expiresAt, err := time.Parse(time.RFC3339, document.ExpiresAt)
	if err != nil {
		return time.Time{}, fmt.Errorf("source metadata expires_at is invalid: %w", err)
	}
	if issuedAt.After(now.Add(5 * time.Minute)) {
		return time.Time{}, fmt.Errorf("source metadata issued_at is in the future")
	}
	if !expiresAt.After(issuedAt) {
		return time.Time{}, fmt.Errorf("source metadata expires_at must be after issued_at")
	}
	if expiresAt.Sub(now) < policy.MinimumValidity {
		return time.Time{}, fmt.Errorf("source metadata validity is shorter than the GBO minimum")
	}
	if _, err := compareNumericVersion(document.Version, "0"); err != nil {
		return time.Time{}, fmt.Errorf("source metadata version: %w", err)
	}
	if _, err := compareNumericVersion(definition.TypeVersion, "0"); err != nil {
		return time.Time{}, fmt.Errorf("type metadata version: %w", err)
	}
	return expiresAt, nil
}

func compareNumericVersion(left, right string) (int, error) {
	parse := func(value string) ([]int, error) {
		value = strings.TrimPrefix(value, "v")
		parts := strings.Split(value, ".")
		if len(parts) == 0 {
			return nil, fmt.Errorf("empty version")
		}
		result := make([]int, len(parts))
		for i, part := range parts {
			parsed, err := strconv.Atoi(part)
			if err != nil || parsed < 0 {
				return nil, fmt.Errorf("version %q is not numeric dotted notation", value)
			}
			result[i] = parsed
		}
		return result, nil
	}
	a, err := parse(left)
	if err != nil {
		return 0, err
	}
	b, err := parse(right)
	if err != nil {
		return 0, err
	}
	length := max(len(a), len(b))
	for i := 0; i < length; i++ {
		var av, bv int
		if i < len(a) {
			av = a[i]
		}
		if i < len(b) {
			bv = b[i]
		}
		if av < bv {
			return -1, nil
		}
		if av > bv {
			return 1, nil
		}
	}
	return 0, nil
}

func minTime(left, right time.Time) time.Time {
	if left.Before(right) {
		return left
	}
	return right
}
