package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"regexp"
	"slices"

	"gbo-demo/eudi-adapter/internal/gbosimplev1"
)

var (
	languageTagPattern        = regexp.MustCompile(`^[A-Za-z]{2,3}(?:-[A-Za-z0-9]{1,8})*$`)
	summaryPlaceholderPattern = regexp.MustCompile(`\{\{([^{}]*)\}\}`)
)

// reservedClaimNames are set by the issuer or wallet, never by a source.
// nl-wallet rejects Type Metadata that describes any of them as a claim.
var reservedClaimNames = []string{"vct", "vct#integrity", "cnf", "iss", "nbf", "exp", "iat", "sub", "status", "attestation_qualification"}

// sourceClaim defines one credential claim exactly once: the claims key is
// where the value goes, Source is where it comes from, and the remaining
// fields are how the wallet shows it. GBO derives the projection mapping and
// the published Type Metadata from it.
type sourceClaim struct {
	Name        string        `json:"-"`
	Source      mappingRule   `json:"source"`
	Label       localizedText `json:"label"`
	Description localizedText `json:"description,omitempty"`
	SD          string        `json:"sd,omitempty"`
}

// sourceClaims keeps document order, which is the order the wallet shows.
type sourceClaims []sourceClaim

// sourceDisplay is the card-level presentation for one language.
type sourceDisplay struct {
	Lang        string          `json:"-"`
	Name        string          `json:"name"`
	Description string          `json:"description,omitempty"`
	Summary     string          `json:"summary,omitempty"`
	Rendering   json.RawMessage `json:"rendering,omitempty"`
}

type sourceDisplays []sourceDisplay

type localizedString struct {
	Lang string
	Text string
}

// localizedText is a JSON object keyed by language tag, in document order.
type localizedText []localizedString

func (c *sourceClaims) UnmarshalJSON(data []byte) error {
	var claims sourceClaims
	err := decodeOrderedObject(data, func(name string, value json.RawMessage) error {
		var claim sourceClaim
		if err := decodeStrict(value, &claim); err != nil {
			return fmt.Errorf("claim %q: %w", name, err)
		}
		claim.Name = name
		claims = append(claims, claim)
		return nil
	})
	if err != nil {
		return fmt.Errorf("invalid claims: %w", err)
	}
	*c = claims
	return nil
}

func (c sourceClaims) MarshalJSON() ([]byte, error) {
	return encodeOrderedObject(len(c), func(index int) (string, any) {
		type plainClaim sourceClaim
		return c[index].Name, plainClaim(c[index])
	})
}

func (d *sourceDisplays) UnmarshalJSON(data []byte) error {
	var displays sourceDisplays
	err := decodeOrderedObject(data, func(lang string, value json.RawMessage) error {
		var display sourceDisplay
		if err := decodeStrict(value, &display); err != nil {
			return fmt.Errorf("display %q: %w", lang, err)
		}
		display.Lang = lang
		displays = append(displays, display)
		return nil
	})
	if err != nil {
		return fmt.Errorf("invalid display: %w", err)
	}
	*d = displays
	return nil
}

func (d sourceDisplays) MarshalJSON() ([]byte, error) {
	return encodeOrderedObject(len(d), func(index int) (string, any) {
		type plainDisplay sourceDisplay
		return d[index].Lang, plainDisplay(d[index])
	})
}

func (t *localizedText) UnmarshalJSON(data []byte) error {
	var text localizedText
	err := decodeOrderedObject(data, func(lang string, value json.RawMessage) error {
		var entry string
		if err := json.Unmarshal(value, &entry); err != nil {
			return fmt.Errorf("language %q must map to a string", lang)
		}
		text = append(text, localizedString{Lang: lang, Text: entry})
		return nil
	})
	if err != nil {
		return err
	}
	*t = text
	return nil
}

func (t localizedText) MarshalJSON() ([]byte, error) {
	return encodeOrderedObject(len(t), func(index int) (string, any) {
		return t[index].Lang, t[index].Text
	})
}

// decodeOrderedObject visits the members of a JSON object in document order.
// A Go map would lose that order, and the wallet shows claims and languages
// in the order the source wrote them.
func decodeOrderedObject(data []byte, visit func(key string, value json.RawMessage) error) error {
	decoder := json.NewDecoder(bytes.NewReader(data))
	if token, err := decoder.Token(); err != nil || token != json.Delim('{') {
		return fmt.Errorf("must be a JSON object")
	}
	seen := make(map[string]bool)
	for decoder.More() {
		token, err := decoder.Token()
		if err != nil {
			return err
		}
		key := token.(string)
		if seen[key] {
			return fmt.Errorf("duplicate key %q", key)
		}
		seen[key] = true
		var value json.RawMessage
		if err := decoder.Decode(&value); err != nil {
			return err
		}
		if err := visit(key, value); err != nil {
			return err
		}
	}
	if _, err := decoder.Token(); err != nil {
		return err
	}
	return nil
}

func encodeOrderedObject(length int, member func(index int) (string, any)) ([]byte, error) {
	var buffer bytes.Buffer
	buffer.WriteByte('{')
	for index := range length {
		key, value := member(index)
		encodedKey, err := json.Marshal(key)
		if err != nil {
			return nil, err
		}
		encodedValue, err := json.Marshal(value)
		if err != nil {
			return nil, err
		}
		if index > 0 {
			buffer.WriteByte(',')
		}
		buffer.Write(encodedKey)
		buffer.WriteByte(':')
		buffer.Write(encodedValue)
	}
	buffer.WriteByte('}')
	return buffer.Bytes(), nil
}

func decodeStrict(data []byte, target any) error {
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return err
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return fmt.Errorf("trailing JSON data")
	}
	return nil
}

// mapping is the gbo-simple-v1 projection contract the claims imply.
func (d sourceAttestationDefinition) mapping() gbosimplev1.Mapping {
	mapping := make(gbosimplev1.Mapping, len(d.Claims))
	for _, claim := range d.Claims {
		mapping[claim.Name] = claim.Source
	}
	return mapping
}

func validateSourceClaims(definition sourceAttestationDefinition) error {
	if len(definition.Claims) == 0 {
		return fmt.Errorf("claims must contain at least one claim")
	}
	if err := gbosimplev1.Validate(definition.mapping()); err != nil {
		return err
	}
	for _, claim := range definition.Claims {
		if slices.Contains(reservedClaimNames, claim.Name) {
			return fmt.Errorf("claim %q is reserved for the issuer", claim.Name)
		}
		if claim.Source.Datatype == "number" {
			return fmt.Errorf("claim %q uses number, which nl-wallet v0.5 cannot represent; use integer or string", claim.Name)
		}
		if len(claim.Label) == 0 {
			return fmt.Errorf("claim %q needs a label in at least one language", claim.Name)
		}
		if err := validateLocalizedText(claim.Label, true); err != nil {
			return fmt.Errorf("claim %q label: %w", claim.Name, err)
		}
		if err := validateLocalizedText(claim.Description, false); err != nil {
			return fmt.Errorf("claim %q description: %w", claim.Name, err)
		}
		for _, description := range claim.Description {
			if !slices.ContainsFunc(claim.Label, func(label localizedString) bool { return label.Lang == description.Lang }) {
				return fmt.Errorf("claim %q has a description in %q but no label in that language", claim.Name, description.Lang)
			}
		}
		switch claim.SD {
		case "", "always", "allowed", "never":
		default:
			return fmt.Errorf("claim %q has unsupported sd %q", claim.Name, claim.SD)
		}
	}
	return nil
}

func validateLocalizedText(text localizedText, requireText bool) error {
	for _, entry := range text {
		if !languageTagPattern.MatchString(entry.Lang) {
			return fmt.Errorf("invalid language tag %q", entry.Lang)
		}
		if requireText && entry.Text == "" {
			return fmt.Errorf("language %q is empty", entry.Lang)
		}
	}
	return nil
}

func validateSourceDisplay(definition sourceAttestationDefinition) error {
	if len(definition.Display) == 0 {
		return fmt.Errorf("display must contain at least one language")
	}
	for _, display := range definition.Display {
		if !languageTagPattern.MatchString(display.Lang) {
			return fmt.Errorf("display has an invalid language tag %q", display.Lang)
		}
		if display.Name == "" {
			return fmt.Errorf("display %q needs a name", display.Lang)
		}
		if len(display.Rendering) > 0 {
			var rendering map[string]any
			if err := json.Unmarshal(display.Rendering, &rendering); err != nil || rendering == nil {
				return fmt.Errorf("display %q rendering must be a JSON object", display.Lang)
			}
		}
		// Every claim is published with svg_id equal to its name, so a
		// placeholder resolves exactly when it names a claim.
		for _, match := range summaryPlaceholderPattern.FindAllStringSubmatch(display.Summary, -1) {
			if !slices.ContainsFunc(definition.Claims, func(claim sourceClaim) bool { return claim.Name == match[1] }) {
				return fmt.Errorf("display %q summary placeholder {{%s}} is not a claim", display.Lang, match[1])
			}
		}
	}
	return nil
}
