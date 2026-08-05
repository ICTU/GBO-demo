// Package gbosimplev1 implements the deliberately small, fail-closed
// projection profile used by source-provided GBO attestation metadata.
package gbosimplev1

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"math/big"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
)

const (
	MaxClaims        = 128
	MaxClaimLength   = 64
	MaxPointerLength = 512
	MaxPointerDepth  = 32
	MaxStringLength  = 16 * 1024
	MaxNumberLength  = 128
	MaxMoneyScale    = 6
)

type ErrorCode string

const (
	CodeMappingInvalid        ErrorCode = "GBO_SIMPLE_MAPPING_INVALID"
	CodePathMissing           ErrorCode = "GBO_SIMPLE_PATH_MISSING"
	CodeTypeMismatch          ErrorCode = "GBO_SIMPLE_TYPE_MISMATCH"
	CodeConversionUnsupported ErrorCode = "GBO_SIMPLE_CONVERSION_UNSUPPORTED"
	CodeCurrencyMismatch      ErrorCode = "GBO_SIMPLE_CURRENCY_MISMATCH"
	CodeMoneyInexact          ErrorCode = "GBO_SIMPLE_MONEY_INEXACT"
	CodeResultType            ErrorCode = "GBO_SIMPLE_RESULT_TYPE"
	CodeCardinalityAmbiguous  ErrorCode = "GBO_SIMPLE_CARDINALITY_AMBIGUOUS"
)

type ProfileError struct {
	Code   ErrorCode
	Claim  string
	Detail string
}

func (e *ProfileError) Error() string {
	if e.Claim != "" {
		return fmt.Sprintf("%s: claim %q: %s", e.Code, e.Claim, e.Detail)
	}
	return fmt.Sprintf("%s: %s", e.Code, e.Detail)
}

func ErrorCodeOf(err error) ErrorCode {
	var profileErr *ProfileError
	if errors.As(err, &profileErr) {
		return profileErr.Code
	}
	return ""
}

type Outcome string

const (
	OutcomeCredential Outcome = "credential"
	OutcomeNoData     Outcome = "no_data"
)

type Projection struct {
	Outcome Outcome
	Claims  map[string]any
}

type Mapping map[string]Rule

type Rule struct {
	Pointer   string     `json:"pointer"`
	Datatype  string     `json:"datatype"`
	Transform *Transform `json:"transform,omitempty"`
}

type Transform struct {
	Operator        string `json:"operator"`
	CurrencyPointer string `json:"currency_pointer"`
	Currency        string `json:"currency"`
	SourceScale     int    `json:"source_scale"`
	TargetScale     int    `json:"target_scale"`
}

func (r *Rule) UnmarshalJSON(data []byte) error {
	type plainRule Rule
	var decoded plainRule
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&decoded); err != nil {
		return profileError(CodeMappingInvalid, "", "invalid rule: %v", err)
	}
	if err := ensureJSONEOF(decoder); err != nil {
		return profileError(CodeMappingInvalid, "", "invalid rule: %v", err)
	}
	*r = Rule(decoded)
	return nil
}

func (t *Transform) UnmarshalJSON(data []byte) error {
	type transformFields struct {
		Operator        *string `json:"operator"`
		CurrencyPointer *string `json:"currency_pointer"`
		Currency        *string `json:"currency"`
		SourceScale     *int    `json:"source_scale"`
		TargetScale     *int    `json:"target_scale"`
	}
	var decoded transformFields
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&decoded); err != nil {
		return profileError(CodeMappingInvalid, "", "invalid transform: %v", err)
	}
	if err := ensureJSONEOF(decoder); err != nil {
		return profileError(CodeMappingInvalid, "", "invalid transform: %v", err)
	}
	if decoded.Operator == nil || decoded.CurrencyPointer == nil || decoded.Currency == nil || decoded.SourceScale == nil || decoded.TargetScale == nil {
		return profileError(CodeMappingInvalid, "", "all money_scale fields are required")
	}
	*t = Transform{
		Operator:        *decoded.Operator,
		CurrencyPointer: *decoded.CurrencyPointer,
		Currency:        *decoded.Currency,
		SourceScale:     *decoded.SourceScale,
		TargetScale:     *decoded.TargetScale,
	}
	return nil
}

func ensureJSONEOF(decoder *json.Decoder) error {
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		if err == nil {
			return fmt.Errorf("invalid gbo-simple-v1 JSON: multiple values")
		}
		return fmt.Errorf("invalid gbo-simple-v1 JSON: %w", err)
	}
	return nil
}

var (
	claimPattern    = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)
	currencyPattern = regexp.MustCompile(`^[A-Z]{3}$`)
	numberPattern   = regexp.MustCompile(`^(-?)(0|[1-9][0-9]*)(?:\.([0-9]+))?(?:[eE]([+-]?[0-9]+))?$`)
)

func Validate(mapping Mapping) error {
	if len(mapping) == 0 || len(mapping) > MaxClaims {
		return profileError(CodeMappingInvalid, "", "mapping must contain between 1 and %d claims", MaxClaims)
	}
	claims := sortedClaims(mapping)
	for _, claim := range claims {
		rule := mapping[claim]
		if len(claim) > MaxClaimLength || !claimPattern.MatchString(claim) {
			return profileError(CodeMappingInvalid, claim, "invalid claim name")
		}
		if err := validatePointer(rule.Pointer); err != nil {
			return profileError(CodeMappingInvalid, claim, "invalid pointer: %v", err)
		}
		switch rule.Datatype {
		case "string", "boolean", "integer", "number", "date", "gYear":
		default:
			return profileError(CodeMappingInvalid, claim, "unsupported datatype %q", rule.Datatype)
		}
		if rule.Transform == nil {
			continue
		}
		transform := rule.Transform
		if transform.Operator != "money_scale" {
			return profileError(CodeConversionUnsupported, claim, "unsupported operator %q", transform.Operator)
		}
		if rule.Datatype != "integer" {
			return profileError(CodeMappingInvalid, claim, "money_scale requires datatype integer")
		}
		if err := validatePointer(transform.CurrencyPointer); err != nil {
			return profileError(CodeMappingInvalid, claim, "invalid currency_pointer: %v", err)
		}
		if !currencyPattern.MatchString(transform.Currency) {
			return profileError(CodeMappingInvalid, claim, "currency must be an ISO 4217 alpha-3 code")
		}
		if transform.SourceScale < 0 || transform.SourceScale > MaxMoneyScale || transform.TargetScale < 0 || transform.TargetScale > MaxMoneyScale {
			return profileError(CodeMappingInvalid, claim, "money scales must be between 0 and %d", MaxMoneyScale)
		}
	}
	return nil
}

func Project(root any, resultPointer, cardinality string, mapping Mapping) (Projection, error) {
	if err := Validate(mapping); err != nil {
		return Projection{}, err
	}
	if cardinality != "exactly_one" {
		return Projection{}, profileError(CodeMappingInvalid, "", "unsupported cardinality %q", cardinality)
	}
	if err := validatePointer(resultPointer); err != nil {
		return Projection{}, profileError(CodeMappingInvalid, "", "invalid result_pointer: %v", err)
	}
	selected, ok := jsonPointer(root, resultPointer)
	if !ok {
		return Projection{}, profileError(CodePathMissing, "", "result_pointer %q does not exist", resultPointer)
	}
	rows, ok := selected.([]any)
	if !ok {
		return Projection{}, profileError(CodeResultType, "", "result_pointer must select an array")
	}
	switch len(rows) {
	case 0:
		return Projection{Outcome: OutcomeNoData}, nil
	case 1:
	default:
		return Projection{}, profileError(CodeCardinalityAmbiguous, "", "exactly_one received %d results", len(rows))
	}

	claims := make(map[string]any, len(mapping))
	for _, claim := range sortedClaims(mapping) {
		rule := mapping[claim]
		value, ok := jsonPointer(rows[0], rule.Pointer)
		if !ok {
			return Projection{}, profileError(CodePathMissing, claim, "pointer %q does not exist", rule.Pointer)
		}
		converted, err := convertValue(rows[0], value, rule)
		if err != nil {
			var profileErr *ProfileError
			if errors.As(err, &profileErr) && profileErr.Claim == "" {
				profileErr.Claim = claim
			}
			return Projection{}, err
		}
		claims[claim] = converted
	}
	return Projection{Outcome: OutcomeCredential, Claims: claims}, nil
}

func DecodeJSON(data []byte) (any, error) {
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.UseNumber()
	var value any
	if err := decoder.Decode(&value); err != nil {
		return nil, err
	}
	if err := ensureJSONEOF(decoder); err != nil {
		return nil, err
	}
	return value, nil
}

// EqualJSON compares JSON values semantically. In particular, 2025 decoded
// as float64 by the legacy path equals the int64 produced by this profile.
func EqualJSON(left, right any) bool {
	leftNumber, leftIsNumber := numberText(left)
	rightNumber, rightIsNumber := numberText(right)
	if leftIsNumber || rightIsNumber {
		if !leftIsNumber || !rightIsNumber {
			return false
		}
		leftRat, _, leftOK := parseDecimal(leftNumber)
		rightRat, _, rightOK := parseDecimal(rightNumber)
		return leftOK && rightOK && leftRat.Cmp(rightRat) == 0
	}
	switch l := left.(type) {
	case map[string]any:
		r, ok := right.(map[string]any)
		if !ok || len(l) != len(r) {
			return false
		}
		for key, value := range l {
			other, ok := r[key]
			if !ok || !EqualJSON(value, other) {
				return false
			}
		}
		return true
	case []any:
		r, ok := right.([]any)
		if !ok || len(l) != len(r) {
			return false
		}
		for i := range l {
			if !EqualJSON(l[i], r[i]) {
				return false
			}
		}
		return true
	default:
		return left == right
	}
}

func convertValue(row, value any, rule Rule) (any, error) {
	if rule.Transform != nil {
		return convertMoney(row, value, *rule.Transform)
	}
	switch rule.Datatype {
	case "string":
		text, ok := value.(string)
		if !ok || len(text) > MaxStringLength {
			return nil, profileError(CodeTypeMismatch, "", "value must be a string of at most %d bytes", MaxStringLength)
		}
		return text, nil
	case "boolean":
		boolean, ok := value.(bool)
		if !ok {
			return nil, profileError(CodeTypeMismatch, "", "value must be a boolean")
		}
		return boolean, nil
	case "integer", "gYear":
		number, ok := numberText(value)
		if !ok {
			return nil, profileError(CodeTypeMismatch, "", "value must be an integer")
		}
		rat, _, ok := parseDecimal(number)
		if !ok || !rat.IsInt() || !rat.Num().IsInt64() {
			return nil, profileError(CodeTypeMismatch, "", "value must be an int64")
		}
		integer := rat.Num().Int64()
		if rule.Datatype == "gYear" && (integer < 0 || integer > 9999) {
			return nil, profileError(CodeTypeMismatch, "", "gYear must be between 0 and 9999")
		}
		return integer, nil
	case "number":
		number, ok := numberText(value)
		if !ok {
			return nil, profileError(CodeTypeMismatch, "", "value must be a JSON number")
		}
		if _, _, ok := parseDecimal(number); !ok {
			return nil, profileError(CodeTypeMismatch, "", "value must be a finite JSON number")
		}
		return json.Number(number), nil
	case "date":
		date, ok := value.(string)
		if !ok {
			return nil, profileError(CodeTypeMismatch, "", "value must be an RFC 3339 full-date")
		}
		parsed, err := time.Parse("2006-01-02", date)
		if err != nil || parsed.Format("2006-01-02") != date {
			return nil, profileError(CodeTypeMismatch, "", "value must be an RFC 3339 full-date")
		}
		return date, nil
	default:
		return nil, profileError(CodeConversionUnsupported, "", "unsupported datatype %q", rule.Datatype)
	}
}

func convertMoney(row, value any, transform Transform) (any, error) {
	currencyValue, ok := jsonPointer(row, transform.CurrencyPointer)
	if !ok {
		return nil, profileError(CodePathMissing, "", "currency_pointer %q does not exist", transform.CurrencyPointer)
	}
	currency, ok := currencyValue.(string)
	if !ok || currency != transform.Currency {
		return nil, profileError(CodeCurrencyMismatch, "", "currency is %v, expected %s", currencyValue, transform.Currency)
	}
	number, ok := numberText(value)
	if !ok {
		return nil, profileError(CodeTypeMismatch, "", "money value must be a JSON number")
	}
	rat, scale, ok := parseDecimal(number)
	if !ok {
		return nil, profileError(CodeTypeMismatch, "", "money value must be a finite decimal JSON number")
	}
	if scale > transform.SourceScale {
		return nil, profileError(CodeMoneyInexact, "", "money value has scale %d, maximum source_scale is %d", scale, transform.SourceScale)
	}
	scaled := new(big.Rat).Mul(rat, new(big.Rat).SetInt(powerOfTen(transform.TargetScale)))
	if !scaled.IsInt() || !scaled.Num().IsInt64() {
		return nil, profileError(CodeMoneyInexact, "", "money value cannot be represented exactly at target_scale %d", transform.TargetScale)
	}
	return scaled.Num().Int64(), nil
}

func validatePointer(pointer string) error {
	if pointer == "" || !strings.HasPrefix(pointer, "/") {
		return fmt.Errorf("must be a non-empty absolute JSON Pointer")
	}
	if len(pointer) > MaxPointerLength {
		return fmt.Errorf("exceeds %d bytes", MaxPointerLength)
	}
	tokens := strings.Split(pointer[1:], "/")
	if len(tokens) > MaxPointerDepth {
		return fmt.Errorf("exceeds depth %d", MaxPointerDepth)
	}
	for _, token := range tokens {
		for i := 0; i < len(token); i++ {
			if token[i] != '~' {
				continue
			}
			if i+1 >= len(token) || (token[i+1] != '0' && token[i+1] != '1') {
				return fmt.Errorf("contains an invalid RFC 6901 escape")
			}
			i++
		}
	}
	return nil
}

func jsonPointer(root any, pointer string) (any, bool) {
	current := root
	for _, rawToken := range strings.Split(pointer[1:], "/") {
		token := strings.ReplaceAll(strings.ReplaceAll(rawToken, "~1", "/"), "~0", "~")
		switch value := current.(type) {
		case map[string]any:
			var ok bool
			current, ok = value[token]
			if !ok {
				return nil, false
			}
		case []any:
			if token == "" || (len(token) > 1 && token[0] == '0') {
				return nil, false
			}
			index, err := strconv.Atoi(token)
			if err != nil || index < 0 || index >= len(value) {
				return nil, false
			}
			current = value[index]
		default:
			return nil, false
		}
	}
	return current, true
}

func sortedClaims(mapping Mapping) []string {
	claims := make([]string, 0, len(mapping))
	for claim := range mapping {
		claims = append(claims, claim)
	}
	sort.Strings(claims)
	return claims
}

func numberText(value any) (string, bool) {
	switch number := value.(type) {
	case json.Number:
		return number.String(), true
	case int:
		return strconv.Itoa(number), true
	case int8:
		return strconv.FormatInt(int64(number), 10), true
	case int16:
		return strconv.FormatInt(int64(number), 10), true
	case int32:
		return strconv.FormatInt(int64(number), 10), true
	case int64:
		return strconv.FormatInt(number, 10), true
	case uint:
		return strconv.FormatUint(uint64(number), 10), true
	case uint8:
		return strconv.FormatUint(uint64(number), 10), true
	case uint16:
		return strconv.FormatUint(uint64(number), 10), true
	case uint32:
		return strconv.FormatUint(uint64(number), 10), true
	case uint64:
		return strconv.FormatUint(number, 10), true
	case float32:
		if math.IsNaN(float64(number)) || math.IsInf(float64(number), 0) {
			return "", false
		}
		return strconv.FormatFloat(float64(number), 'g', -1, 32), true
	case float64:
		if math.IsNaN(number) || math.IsInf(number, 0) {
			return "", false
		}
		return strconv.FormatFloat(number, 'g', -1, 64), true
	default:
		return "", false
	}
}

func parseDecimal(text string) (*big.Rat, int, bool) {
	if len(text) == 0 || len(text) > MaxNumberLength {
		return nil, 0, false
	}
	parts := numberPattern.FindStringSubmatch(text)
	if parts == nil {
		return nil, 0, false
	}
	digits := parts[2] + parts[3]
	coefficient := new(big.Int)
	if _, ok := coefficient.SetString(digits, 10); !ok {
		return nil, 0, false
	}
	if parts[1] == "-" {
		coefficient.Neg(coefficient)
	}
	exponent := 0
	if parts[4] != "" {
		parsed, err := strconv.Atoi(parts[4])
		if err != nil || parsed < -MaxNumberLength || parsed > MaxNumberLength {
			return nil, 0, false
		}
		exponent = parsed
	}
	scale := len(parts[3]) - exponent
	for scale > 0 && new(big.Int).Mod(new(big.Int).Abs(new(big.Int).Set(coefficient)), big.NewInt(10)).Sign() == 0 {
		coefficient.Quo(coefficient, big.NewInt(10))
		scale--
	}
	if scale <= 0 {
		coefficient.Mul(coefficient, powerOfTen(-scale))
		return new(big.Rat).SetInt(coefficient), 0, true
	}
	return new(big.Rat).SetFrac(coefficient, powerOfTen(scale)), scale, true
}

func powerOfTen(exponent int) *big.Int {
	return new(big.Int).Exp(big.NewInt(10), big.NewInt(int64(exponent)), nil)
}

func profileError(code ErrorCode, claim, format string, args ...any) error {
	return &ProfileError{Code: code, Claim: claim, Detail: fmt.Sprintf(format, args...)}
}
