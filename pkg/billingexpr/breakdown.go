package billingexpr

import (
	"fmt"
	"math"
)

// costSplitVars are the token dimensions an expression can price. Order is
// irrelevant to the result; it only fixes the order probes run in.
var costSplitVars = []string{"p", "c", "cr", "cc", "cc1h", "img", "img_o", "ai", "ao"}

// CostSplit is one expression evaluation broken down into the cost each token
// dimension contributed, in the expression's own units ($ per the v1 contract).
type CostSplit struct {
	Total float64
	Tier  string
	// Costs is keyed by token variable name. A dimension the expression never
	// references, or one whose token count is zero, is absent rather than zero.
	Costs map[string]float64
}

// SplitCost attributes an expression's output to the individual token
// dimensions that produced it, so a statement can quote a per-component price
// for expression-priced models the way it does for ratio-priced ones.
//
// The attribution is a finite difference: a dimension's cost is what the total
// drops by when that dimension's tokens are removed. For the linear pricing
// expressions v1 describes (`p * 3 + c * 15 + cr * 0.3`) this recovers the
// coefficients exactly, and it keeps working for tiered expressions, where the
// coefficients differ per tier and no single coefficient exists in the source.
//
// Removing tokens must not move the request to a different tier, or the
// difference would mix two price lists. Tier conditions key off `len`, which
// SplitCost never perturbs, but an expression is free to branch on `p` or `c`
// directly. Any probe that lands in another tier — or that reports a component
// costing less than nothing — makes the split unsafe to publish, so it is
// reported as an error rather than an approximation.
func SplitCost(exprStr string, params TokenParams) (CostSplit, error) {
	total, trace, err := RunExpr(exprStr, params)
	if err != nil {
		return CostSplit{}, err
	}
	if math.IsNaN(total) || math.IsInf(total, 0) {
		return CostSplit{}, fmt.Errorf("expr total is not a finite number: %v", total)
	}

	used := UsedVars(exprStr)
	costs := make(map[string]float64, len(costSplitVars))
	for _, name := range costSplitVars {
		if !used[name] {
			continue
		}
		probe := params
		var tokens float64
		switch name {
		case "p":
			tokens, probe.P = params.P, 0
		case "c":
			tokens, probe.C = params.C, 0
		case "cr":
			tokens, probe.CR = params.CR, 0
		case "cc":
			tokens, probe.CC = params.CC, 0
		case "cc1h":
			tokens, probe.CC1h = params.CC1h, 0
		case "img":
			tokens, probe.Img = params.Img, 0
		case "img_o":
			tokens, probe.ImgO = params.ImgO, 0
		case "ai":
			tokens, probe.AI = params.AI, 0
		case "ao":
			tokens, probe.AO = params.AO, 0
		}
		if tokens <= 0 {
			continue
		}

		rest, probeTrace, err := RunExpr(exprStr, probe)
		if err != nil {
			return CostSplit{}, err
		}
		if probeTrace.MatchedTier != trace.MatchedTier {
			return CostSplit{}, fmt.Errorf("expr is not separable: removing %q moves the request from tier %q to tier %q", name, trace.MatchedTier, probeTrace.MatchedTier)
		}
		cost := total - rest
		if math.IsNaN(cost) || cost < 0 {
			return CostSplit{}, fmt.Errorf("expr is not separable: %q contributes %v", name, cost)
		}
		costs[name] = cost
	}

	return CostSplit{Total: total, Tier: trace.MatchedTier, Costs: costs}, nil
}
