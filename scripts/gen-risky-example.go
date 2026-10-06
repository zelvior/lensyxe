//go:build ignore

// Command gen-risky-example writes examples/risky-go/legacy/order.go.
//
// The example exists so the integration suite has a file that crosses every
// hotspot threshold at once. Its bulk is mechanical, and mechanical bulk is
// exactly the kind of thing that should be generated rather than committed: a
// 600-line hand-written file would be unreviewable, and editing it by hand
// every time a threshold moved would be worse.
//
// Run it from the repository root:
//
//	go run scripts/gen-risky-example.go
package main

import (
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"
)

const target = "examples/risky-go/legacy/order.go"

// head is the hand-written portion: readable, idiomatic Go that a reviewer can
// actually check. Everything after it is generated.
const head = `// Package legacy contains one long, deeply conditional file.
//
// This package exists to be a hotspot. It is imported by nothing in the
// repository: it exists so the integration suite has a file that crosses every
// hotspot threshold at once, which is the only way to exercise the confirmed-
// hotspot path end to end.
package legacy

import "strings"

// DiscountTiers maps a currency to its discount percentage.
var DiscountTiers = map[string]int{
	"bronze": 5,
	"silver": 10,
	"gold":   20,
}

// Order is a customer order.
type Order struct {
	Customer string
	Items    []Item
	Currency string
	Notes    string
	Priority int
}

// Item is one line on an order.
type Item struct {
	SKU       string
	Quantity  int
	UnitCents int
	Category  string
	WeightKg  float64
}

// Total sums the order, applying per-category discounts and a surcharge.
func Total(o Order) int {
	total := 0
	for _, item := range o.Items {
		price := item.UnitCents * item.Quantity
		switch item.Category {
		case "books", "groceries":
			total += price
		case "electronics":
			if item.Quantity > 10 {
				total += int(float64(price) * 0.9)
			} else if item.Quantity > 5 {
				total += int(float64(price) * 0.95)
			} else {
				total += price
			}
		case "clothing":
			total += int(float64(price) * 0.88)
		default:
			total += price
		}
	}
	if o.Priority > 5 {
		total += 500
	} else if o.Priority > 2 {
		total += 250
	}
	if tier, ok := DiscountTiers[o.Currency]; ok {
		total -= total * tier / 100
	}
	if total < 0 {
		total = 0
	}
	return total
}

// Shipping estimates the shipping cost for an order.
func Shipping(o Order) int {
	weight := 0.0
	for _, item := range o.Items {
		weight += item.WeightKg * float64(item.Quantity)
	}
	switch {
	case weight < 1:
		return 500
	case weight < 5:
		return 900
	case weight < 20:
		return 1500
	case weight < 50:
		return 3000
	default:
		return 6000
	}
}

// Label renders a human label for an order.
func Label(o Order) string {
	parts := make([]string, 0, len(o.Items))
	for _, item := range o.Items {
		switch {
		case item.Quantity == 0:
			continue
		case item.Quantity == 1:
			parts = append(parts, item.SKU)
		default:
			parts = append(parts, item.SKU+" x"+itoa(item.Quantity))
		}
	}
	if len(parts) == 0 {
		return "empty order for " + o.Customer
	}
	return strings.Join(parts, ", ")
}

// Validate reports the first problem with an order.
func Validate(o Order) string {
	if o.Customer == "" {
		return "missing customer"
	}
	if o.Currency == "" {
		return "missing currency"
	}
	for _, item := range o.Items {
		switch {
		case item.SKU == "":
			return "missing sku"
		case item.Quantity < 0:
			return "negative quantity for " + item.SKU
		case item.UnitCents < 0:
			return "negative price for " + item.SKU
		case item.WeightKg < 0:
			return "negative weight for " + item.SKU
		}
	}
	if o.Priority < 0 || o.Priority > 10 {
		return "priority out of range"
	}
	return ""
}

// Bucket groups an order into a fulfilment bucket.
func Bucket(o Order) string {
	switch total := Total(o); {
	case total == 0:
		return "empty"
	case total < 1000:
		return "small"
	case total < 10000:
		return "medium"
	case total < 100000:
		return "large"
	default:
		return "wholesale"
	}
}

// RiskScore summarizes how risky an order is to fulfil.
func RiskScore(o Order) int {
	score := 0
	if len(o.Items) > 20 {
		score += 3
	}
	if len(o.Items) > 50 {
		score += 3
	}
	for _, item := range o.Items {
		if item.WeightKg > 30 {
			score += 2
		}
		if item.Category == "electronics" && item.Quantity > 20 {
			score += 2
		}
	}
	if o.Priority > 7 {
		score += 4
	}
	if Shipping(o) > 3000 {
		score += 2
	}
	if score > 10 {
		score = 10
	}
	return score
}

// NeedsReview reports whether an order should be checked by a human.
func NeedsReview(o Order) bool {
	if Validate(o) != "" {
		return true
	}
	if RiskScore(o) >= 6 {
		return true
	}
	switch o.Currency {
	case "USD", "EUR", "GBP":
		return false
	default:
		return true
	}
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	neg := n < 0
	if neg {
		n = -n
	}
	var buf [20]byte
	i := len(buf)
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	if neg {
		i--
		buf[i] = '-'
	}
	return string(buf[i:])
}
`

// categories seed the generated handlers. Each one produces a distinct
// function pair, so the file has varied branch shapes rather than a repeated
// block that a reader would tune out.
var categories = []string{
	"books", "groceries", "electronics", "clothing",
	"toys", "garden", "tools", "office", "auto", "pet",
}

func main() {
	var b strings.Builder
	b.WriteString(head)

	// Generated section: one weight handler and one surcharge handler per
	// category. Each carries real branching, which is what pushes the file past
	// both the size threshold and the complexity band.
	for i, cat := range categories {
		fmt.Fprintf(&b, `
// Weight%s factors %s items into a handling weight.
func Weight%s(o Order) float64 {
	weight := 0.0
	switch {
	case o.Priority > %d:
		weight *= 3
	case o.Priority > 2:
		weight *= 2
	case o.Priority > 0:
		weight++
	}
	for _, item := range o.Items {
		if item.Category != %q {
			continue
		}
		switch item.Quantity {
		case 0:
			continue
		case 1:
			weight++
		case 2, 3:
			weight += 2
		default:
			weight += float64(item.Quantity)
		}
		switch {
		case item.WeightKg > %d:
			weight--
		case item.WeightKg > 0:
			weight += 0.5
		}
	}
	switch {
	case weight < 0:
		weight = 0
	case weight > 1000:
		weight = 1000
	}
	return weight
}
`, cap(cat), cat, cap(cat), i%7, cat, (i%5+1)*10)

		fmt.Fprintf(&b, `
// Surcharge%s adjusts the %s subtotal for handling and currency.
func Surcharge%s(o Order) float64 {
	base := float64(len(o.Items)) * 0.5
	for _, item := range o.Items {
		if item.Category != %q {
			base++
			continue
		}
		switch {
		case item.Quantity > 10:
			base -= 2
		case item.Quantity > 5:
			base--
		}
		if item.UnitCents > %d {
			base += 3
		}
	}
	switch o.Currency {
	case "USD", "CAD":
		base *= 1.05
	case "EUR", "GBP":
		base *= 1.1
	default:
		base *= 1.25
	}
	if o.Notes != "" && len(o.Notes) > 100 {
		base += 5
	}
	if base < 0 {
		base = 0
	}
	return base
}
`, cap(cat), cat, cap(cat), cat, 10000+i*500)
	}

	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		log.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(target, []byte(b.String()), 0o644); err != nil {
		log.Fatalf("write: %v", err)
	}

	lines := strings.Count(b.String(), "\n")
	fmt.Printf("wrote %s (%d lines)\n", target, lines)
}

// cap upper-cases the first rune of s.
func cap(s string) string {
	if s == "" {
		return s
	}
	return strings.ToUpper(s[:1]) + s[1:]
}
