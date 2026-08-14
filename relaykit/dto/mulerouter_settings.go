package dto

import (
	"fmt"
	"math"
	"sort"
	"strconv"
	"strings"
)

const (
	// MuleRouterPathPrefix is the path prefix MuleRouter publishes its vendor
	// endpoints under, both upstream and on our compatible routes:
	// /vendors/{vendor}/v1/{model}/{action}.
	MuleRouterPathPrefix = "/vendors"
	// MuleRouterDefaultAction is the action segment used by most vendors.
	MuleRouterDefaultAction = "generation"

	MuleRouterVarKindInt  = "int"
	MuleRouterVarKindEnum = "enum"
)

// muleRouterCostParams are request fields known to scale upstream cost. A
// request carrying one of them without a matching billing_vars declaration is
// rejected, so an upstream pricing dimension the administrator has not
// configured can never slip through as an unbounded billing multiplier.
var muleRouterCostParams = map[string]bool{
	"n":            true,
	"count":        true,
	"num_images":   true,
	"num_outputs":  true,
	"batch_size":   true,
	"duration":     true,
	"seconds":      true,
	"resolution":   true,
	"size":         true,
	"width":        true,
	"height":       true,
	"quality":      true,
	"fps":          true,
	"frames":       true,
	"num_frames":   true,
	"steps":        true,
	"sample_count": true,
}

// MuleRouterConfig is the channel-level route table for MuleRouter channels.
// Adding a vendor or a model is a configuration change, not a code change.
type MuleRouterConfig struct {
	Routes []MuleRouterRoute `json:"routes,omitempty"`
}

// MuleRouterRoute maps one upstream endpoint to one internal model name and
// declares every request field that participates in its billing.
type MuleRouterRoute struct {
	Vendor      string                 `json:"vendor"`
	Model       string                 `json:"model"`
	Action      string                 `json:"action,omitempty"`
	BillingVars []MuleRouterBillingVar `json:"billing_vars,omitempty"`
}

// MuleRouterBillingVar declares both the accepted domain of a request field and
// the multiplier it contributes. The two jobs are deliberately fused: a field
// can only become a billing multiplier by first being bounded.
//
// Defaults are always written as strings ("5", "480p", "true") because enum
// lookups stringify the incoming request value.
type MuleRouterBillingVar struct {
	Name    string             `json:"name"`
	Source  string             `json:"source,omitempty"`
	Kind    string             `json:"kind"`
	Default string             `json:"default"`
	Min     *float64           `json:"min,omitempty"`
	Max     *float64           `json:"max,omitempty"`
	Enum    []float64          `json:"enum,omitempty"`
	Values  map[string]float64 `json:"values,omitempty"`
	// Divisor turns an int value into a multiplier (value / divisor). Zero means
	// the field is bounds-checked but does not affect price.
	Divisor float64 `json:"divisor,omitempty"`
}

// FindRouteByModelName resolves the internal model name "{vendor}/{model}/{action}"
// back to its route. Unlisted names are rejected by the caller: an unconfigured
// model has neither a price nor bounded multipliers.
func (c *MuleRouterConfig) FindRouteByModelName(name string) (*MuleRouterRoute, bool) {
	if c == nil {
		return nil, false
	}
	name = strings.TrimSpace(name)
	for i := range c.Routes {
		if c.Routes[i].ModelName() == name {
			return &c.Routes[i], true
		}
	}
	return nil, false
}

func (r *MuleRouterRoute) ResolvedAction() string {
	if action := strings.TrimSpace(r.Action); action != "" {
		return action
	}
	return MuleRouterDefaultAction
}

// ModelName is the internal model name used for pricing, model permissions and
// channel selection. The action segment is part of it because sibling actions
// of the same upstream model (text-to-video vs image-to-video) are priced
// differently and new-api indexes per-call prices by model name alone.
func (r *MuleRouterRoute) ModelName() string {
	return fmt.Sprintf("%s/%s/%s", strings.TrimSpace(r.Vendor), strings.TrimSpace(r.Model), r.ResolvedAction())
}

// MuleRouterUpstreamPath builds the submit path for an internal model name of
// the form "{vendor}/{model}/{action}". It is derived from the name alone so
// the polling loop, which only carries the model name, can rebuild it without
// the channel's route table.
func MuleRouterUpstreamPath(modelName string) (string, bool) {
	parts := strings.Split(strings.TrimSpace(modelName), "/")
	if len(parts) != 3 || parts[0] == "" || parts[1] == "" || parts[2] == "" {
		return "", false
	}
	return fmt.Sprintf("%s/%s/v1/%s/%s", MuleRouterPathPrefix, parts[0], parts[1], parts[2]), true
}

func (v *MuleRouterBillingVar) SourceKey() string {
	if source := strings.TrimSpace(v.Source); source != "" {
		return source
	}
	return strings.TrimSpace(v.Name)
}

// EvaluateBilling validates every declared billing field against the merged
// request parameters and returns the multipliers they contribute. It also
// rejects undeclared fields that are known to scale cost. Any returned error is
// a client error: the request must be refused before quota is pre-consumed.
func (r *MuleRouterRoute) EvaluateBilling(params map[string]any) (map[string]float64, error) {
	declared := make(map[string]bool, len(r.BillingVars))
	ratios := make(map[string]float64, len(r.BillingVars))

	for i := range r.BillingVars {
		v := &r.BillingVars[i]
		declared[v.SourceKey()] = true

		raw, ok := params[v.SourceKey()]
		if !ok || raw == nil {
			raw = v.Default
		}

		ratio, err := v.evaluate(raw)
		if err != nil {
			return nil, err
		}
		if ratio > 0 {
			ratios[strings.TrimSpace(v.Name)] = ratio
		}
	}

	undeclared := make([]string, 0)
	for key, value := range params {
		if declared[key] || value == nil || !muleRouterCostParams[key] {
			continue
		}
		undeclared = append(undeclared, key)
	}
	if len(undeclared) > 0 {
		sort.Strings(undeclared)
		// Name the offending parameters but not the route: this message reaches
		// the caller, who knows which model they asked for and has no business
		// learning which upstream vendor serves it.
		return nil, fmt.Errorf(
			"parameter(s) %s affect the price of this model and are not enabled on this endpoint; remove them",
			strings.Join(undeclared, ", "))
	}

	return ratios, nil
}

// evaluate bounds-checks a single value and converts it to a multiplier.
// A zero return means "validated, but does not affect price".
func (v *MuleRouterBillingVar) evaluate(raw any) (float64, error) {
	switch v.Kind {
	case MuleRouterVarKindEnum:
		key := muleRouterStringify(raw)
		ratio, ok := v.Values[key]
		if !ok {
			return 0, fmt.Errorf("invalid value %q for %s, allowed: %s", key, v.SourceKey(), strings.Join(muleRouterSortedKeys(v.Values), ", "))
		}
		return ratio, nil

	case MuleRouterVarKindInt:
		value, err := muleRouterToNumber(raw)
		if err != nil {
			return 0, fmt.Errorf("invalid value for %s: %w", v.SourceKey(), err)
		}
		if value != math.Trunc(value) {
			return 0, fmt.Errorf("%s must be an integer, got %v", v.SourceKey(), raw)
		}
		if len(v.Enum) > 0 {
			allowed := false
			for _, candidate := range v.Enum {
				if candidate == value {
					allowed = true
					break
				}
			}
			if !allowed {
				return 0, fmt.Errorf("invalid value %v for %s, allowed: %s", value, v.SourceKey(), muleRouterFormatNumbers(v.Enum))
			}
		}
		if v.Min != nil && value < *v.Min {
			return 0, fmt.Errorf("%s must be >= %s", v.SourceKey(), muleRouterFormatNumber(*v.Min))
		}
		if v.Max != nil && value > *v.Max {
			return 0, fmt.Errorf("%s must be <= %s", v.SourceKey(), muleRouterFormatNumber(*v.Max))
		}
		if v.Divisor <= 0 {
			return 0, nil
		}
		ratio := value / v.Divisor
		if ratio <= 0 || math.IsNaN(ratio) || math.IsInf(ratio, 0) {
			return 0, fmt.Errorf("%s produced a non-positive billing multiplier", v.SourceKey())
		}
		return ratio, nil

	default:
		return 0, fmt.Errorf("unsupported billing var kind %q", v.Kind)
	}
}

// Validate is the save-time check for a channel's MuleRouter configuration.
func (c *MuleRouterConfig) Validate() error {
	if c == nil {
		return fmt.Errorf("mulerouter is required")
	}
	if len(c.Routes) == 0 {
		return fmt.Errorf("mulerouter requires at least one route")
	}
	seen := make(map[string]int, len(c.Routes))
	for i := range c.Routes {
		route := &c.Routes[i]
		for field, value := range map[string]string{"vendor": route.Vendor, "model": route.Model} {
			if strings.TrimSpace(value) == "" {
				return fmt.Errorf("mulerouter.routes[%d].%s is required", i, field)
			}
			if strings.ContainsAny(value, "/ ") {
				return fmt.Errorf("mulerouter.routes[%d].%s must not contain '/' or spaces", i, field)
			}
		}
		if strings.ContainsAny(route.ResolvedAction(), "/ ") {
			return fmt.Errorf("mulerouter.routes[%d].action must not contain '/' or spaces", i)
		}
		if previous, ok := seen[route.ModelName()]; ok {
			return fmt.Errorf("mulerouter.routes[%d] duplicates the model %s already declared at routes[%d]", i, route.ModelName(), previous)
		}
		seen[route.ModelName()] = i

		if err := route.validateBillingVars(i); err != nil {
			return err
		}
	}
	return nil
}

func (r *MuleRouterRoute) validateBillingVars(routeIndex int) error {
	names := make(map[string]bool, len(r.BillingVars))
	sources := make(map[string]bool, len(r.BillingVars))

	for j := range r.BillingVars {
		v := &r.BillingVars[j]
		prefix := fmt.Sprintf("mulerouter.routes[%d].billing_vars[%d]", routeIndex, j)

		if strings.TrimSpace(v.Name) == "" {
			return fmt.Errorf("%s.name is required", prefix)
		}
		if names[strings.TrimSpace(v.Name)] {
			return fmt.Errorf("%s.name %q is duplicated", prefix, v.Name)
		}
		names[strings.TrimSpace(v.Name)] = true
		if sources[v.SourceKey()] {
			return fmt.Errorf("%s.source %q is duplicated", prefix, v.SourceKey())
		}
		sources[v.SourceKey()] = true

		switch v.Kind {
		case MuleRouterVarKindEnum:
			if len(v.Values) == 0 {
				return fmt.Errorf("%s.values is required for enum vars", prefix)
			}
			for key, ratio := range v.Values {
				if ratio <= 0 || math.IsNaN(ratio) || math.IsInf(ratio, 0) {
					return fmt.Errorf("%s.values[%q] must be a positive finite number", prefix, key)
				}
			}
		case MuleRouterVarKindInt:
			if v.Min != nil && v.Max != nil && *v.Min > *v.Max {
				return fmt.Errorf("%s.min must not exceed max", prefix)
			}
			if v.Divisor < 0 {
				return fmt.Errorf("%s.divisor must not be negative", prefix)
			}
			// An unbounded multiplier is exactly the failure mode this config
			// exists to prevent.
			if v.Divisor > 0 && v.Max == nil && len(v.Enum) == 0 {
				return fmt.Errorf("%s must declare max or enum because it contributes a billing multiplier", prefix)
			}
		default:
			return fmt.Errorf("%s.kind must be %q or %q", prefix, MuleRouterVarKindInt, MuleRouterVarKindEnum)
		}

		if strings.TrimSpace(v.Default) == "" {
			return fmt.Errorf("%s.default is required so an omitted field bills deterministically", prefix)
		}
		if _, err := v.evaluate(v.Default); err != nil {
			return fmt.Errorf("%s.default is invalid: %w", prefix, err)
		}
	}
	return nil
}

func muleRouterStringify(raw any) string {
	switch value := raw.(type) {
	case string:
		return value
	case bool:
		return strconv.FormatBool(value)
	case float64:
		return muleRouterFormatNumber(value)
	case float32:
		return muleRouterFormatNumber(float64(value))
	case int:
		return strconv.Itoa(value)
	case int64:
		return strconv.FormatInt(value, 10)
	default:
		return fmt.Sprintf("%v", raw)
	}
}

func muleRouterToNumber(raw any) (float64, error) {
	switch value := raw.(type) {
	case float64:
		return value, nil
	case float32:
		return float64(value), nil
	case int:
		return float64(value), nil
	case int64:
		return float64(value), nil
	case string:
		parsed, err := strconv.ParseFloat(strings.TrimSpace(value), 64)
		if err != nil {
			return 0, fmt.Errorf("%q is not a number", value)
		}
		return parsed, nil
	default:
		return 0, fmt.Errorf("%v is not a number", raw)
	}
}

func muleRouterFormatNumber(value float64) string {
	return strconv.FormatFloat(value, 'f', -1, 64)
}

func muleRouterFormatNumbers(values []float64) string {
	formatted := make([]string, 0, len(values))
	for _, value := range values {
		formatted = append(formatted, muleRouterFormatNumber(value))
	}
	return strings.Join(formatted, ", ")
}

func muleRouterSortedKeys(values map[string]float64) []string {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}
