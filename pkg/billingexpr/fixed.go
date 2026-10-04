package billingexpr

import (
	"fmt"
	"math"

	"github.com/expr-lang/expr/ast"
	"github.com/expr-lang/expr/parser"
)

// UsesFixedPricing includes unselected branches, even when compilation later
// optimizes them away. Hosts use it to reject unsupported billing entrances.
func UsesFixedPricing(expression string) bool {
	return UsesFixedPricingByHash(expression, ExprHashString(expression))
}

// UsesFixedPricingByHash avoids hashing an expression already in a snapshot.
func UsesFixedPricingByHash(expression, hash string) bool {
	entry, err := compileEntryFromCacheByHash(expression, hash)
	return err == nil && entry.fixedPricing
}

func containsPricingMarker(node ast.Node) bool {
	return ast.Find(node, func(part ast.Node) bool {
		identifier, ok := part.(*ast.IdentifierNode)
		return ok && (identifier.Value == "tier" || identifier.Value == "fixed")
	}) != nil
}

func isRequestPriceMultiplier(node ast.Node) bool {
	if identifier, ok := node.(*ast.IdentifierNode); ok && identifier.Value == "image_count" {
		return true
	}
	conditional, ok := node.(*ast.ConditionalNode)
	if !ok || !usesRequestProbe(conditional.Cond) || containsPricingMarker(conditional.Cond) {
		return false
	}
	multiplier, multiplierOK := requestRuleNumber(conditional.Exp1)
	fallback, fallbackOK := requestRuleNumber(conditional.Exp2)
	return multiplierOK && fallbackOK && fallback == 1 && multiplier >= 0 && !math.IsNaN(multiplier) && !math.IsInf(multiplier, 0)
}

// validateFixedPricingTree enforces one pricing leaf per execution. Without
// this invariant, adding two tiers or multiplying a fixed price by tokens
// would make both the request charge and its billing-unit trace ambiguous.
func validateFixedPricingTree(node ast.Node) error {
	switch part := node.(type) {
	case *ast.ConditionalNode:
		if containsPricingMarker(part.Cond) {
			break
		}
		if err := validateFixedPricingTree(part.Exp1); err != nil {
			return err
		}
		return validateFixedPricingTree(part.Exp2)
	case *ast.BinaryNode:
		if part.Operator != "*" {
			break
		}
		if isRequestPriceMultiplier(part.Right) {
			return validateFixedPricingTree(part.Left)
		}
		if isRequestPriceMultiplier(part.Left) {
			return validateFixedPricingTree(part.Right)
		}
	case *ast.CallNode:
		callee, ok := part.Callee.(*ast.IdentifierNode)
		if !ok || callee.Value != "tier" || len(part.Arguments) != 2 || containsPricingMarker(part.Arguments[0]) {
			break
		}
		price := part.Arguments[1]
		fixed, ok := price.(*ast.CallNode)
		if ok {
			function, direct := fixed.Callee.(*ast.IdentifierNode)
			if direct && function.Value == "fixed" && len(fixed.Arguments) == 1 {
				amount, literal := requestRuleNumber(fixed.Arguments[0])
				if literal && amount >= 0 && !math.IsNaN(amount) && !math.IsInf(amount*1_000_000, 0) {
					return nil
				}
				return fmt.Errorf("fixed price must be a finite, non-negative numeric literal with a finite v1 value")
			}
		}
		if !containsPricingMarker(price) {
			return nil
		}
	}
	return fmt.Errorf("fixed pricing requires tier(name, fixed(amount)) leaves, conditional tiers and request multipliers; token and fixed charges cannot be combined in one leaf")
}

// pricingLeaves collects the price argument of every tier() leaf reachable
// through pricing conditions and request multipliers. It reports false when
// the expression is not such a tree, for example when tiers are added together.
func pricingLeaves(node ast.Node, leaves *[]ast.Node) bool {
	switch part := node.(type) {
	case *ast.ConditionalNode:
		if containsPricingMarker(part.Cond) {
			return false
		}
		return pricingLeaves(part.Exp1, leaves) && pricingLeaves(part.Exp2, leaves)
	case *ast.BinaryNode:
		if part.Operator != "*" {
			return false
		}
		if isRequestPriceMultiplier(part.Right) {
			return pricingLeaves(part.Left, leaves)
		}
		if isRequestPriceMultiplier(part.Left) {
			return pricingLeaves(part.Right, leaves)
		}
	case *ast.CallNode:
		callee, ok := part.Callee.(*ast.IdentifierNode)
		if ok && callee.Value == "tier" && len(part.Arguments) == 2 && !containsPricingMarker(part.Arguments[0]) {
			*leaves = append(*leaves, part.Arguments[1])
			return true
		}
	}
	return false
}

// compiledPricingLeaves validates the expression with the billing compiler and
// returns its pricing leaves from the original, unoptimized AST.
func compiledPricingLeaves(expression string) ([]ast.Node, bool) {
	if _, err := CompileFromCache(expression); err != nil {
		return nil, false
	}
	_, body := ParseExprVersion(expression)
	tree, err := parser.Parse(body)
	if err != nil {
		return nil, false
	}
	var leaves []ast.Node
	if !pricingLeaves(tree.Node, &leaves) || len(leaves) == 0 {
		return nil, false
	}
	return leaves, true
}

// FixedRequestPricing reports whether every pricing leaf charges a fixed
// request price, so the expression never bills by tokens. When the expression
// has exactly one leaf, price is its USD amount before request multipliers and
// image quantity. It describes listings only and never affects evaluation.
func FixedRequestPricing(expression string) (price float64, single bool, perRequest bool) {
	leaves, ok := compiledPricingLeaves(expression)
	if !ok {
		return 0, false, false
	}
	for _, leaf := range leaves {
		call, isCall := leaf.(*ast.CallNode)
		if !isCall || len(call.Arguments) != 1 {
			return 0, false, false
		}
		callee, direct := call.Callee.(*ast.IdentifierNode)
		if !direct || callee.Value != "fixed" {
			return 0, false, false
		}
		price, _ = requestRuleNumber(call.Arguments[0])
	}
	if len(leaves) != 1 {
		return 0, false, true
	}
	return price, true, true
}

// ZeroTokenPricing reports whether every pricing leaf is a sum of terms that
// multiply by a literal zero, such as tier("base", p * 0 + c * 0). Such an
// expression is free for every input; an explicit fixed(0) is not included.
func ZeroTokenPricing(expression string) bool {
	leaves, ok := compiledPricingLeaves(expression)
	if !ok {
		return false
	}
	for _, leaf := range leaves {
		if !zeroCharge(leaf) {
			return false
		}
	}
	return true
}

func zeroCharge(node ast.Node) bool {
	switch part := node.(type) {
	case *ast.IntegerNode:
		return part.Value == 0
	case *ast.FloatNode:
		return part.Value == 0
	case *ast.BinaryNode:
		switch part.Operator {
		case "+":
			return zeroCharge(part.Left) && zeroCharge(part.Right)
		case "*":
			return zeroCharge(part.Left) || zeroCharge(part.Right)
		}
	}
	return false
}
