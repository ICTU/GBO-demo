package onboarding

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"regexp"
)

const maximumConfigurationSize = 1 << 20

var environmentReference = regexp.MustCompile(`\$\{([A-Za-z_][A-Za-z0-9_]*)\}`)

func LoadConfiguration(path string) (Configuration, error) {
	file, err := os.Open(path)
	if err != nil {
		return Configuration{}, fmt.Errorf("open onboarding configuration: %w", err)
	}
	defer file.Close()
	body, err := io.ReadAll(io.LimitReader(file, maximumConfigurationSize+1))
	if err != nil {
		return Configuration{}, fmt.Errorf("read onboarding configuration: %w", err)
	}
	if len(body) > maximumConfigurationSize {
		return Configuration{}, fmt.Errorf("read onboarding configuration: file exceeds %d bytes", maximumConfigurationSize)
	}
	var configuration Configuration
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&configuration); err != nil {
		return Configuration{}, fmt.Errorf("decode onboarding configuration: %w", err)
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		return Configuration{}, fmt.Errorf("decode onboarding configuration: trailing JSON data")
	}
	if err := expandReturnLocations(configuration.ServiceProviders, os.LookupEnv); err != nil {
		return Configuration{}, err
	}
	return configuration, nil
}

// expandReturnLocations replaces ${VAR} in return locations, so that a
// deployment sets its own host names. An unset or empty variable is an error.
func expandReturnLocations(providers []ServiceProvider, lookup func(string) (string, bool)) error {
	for _, provider := range providers {
		for index, location := range provider.ReturnLocations {
			var missing string
			expanded := environmentReference.ReplaceAllStringFunc(location, func(reference string) string {
				name := environmentReference.FindStringSubmatch(reference)[1]
				value, ok := lookup(name)
				if (!ok || value == "") && missing == "" {
					missing = name
				}
				return value
			})
			if missing != "" {
				return fmt.Errorf("service provider %q: return location %q uses unset environment variable %s", provider.PeerID, location, missing)
			}
			provider.ReturnLocations[index] = expanded
		}
	}
	return nil
}
