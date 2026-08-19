package onboarding

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"os"
)

const maximumConfigurationSize = 1 << 20

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
	return configuration, nil
}
