package main

import "strings"

const categoryNeutralColor = "#64748B"

// Vivid colour per icon, so a category's colour follows what its name means.
// All are dark enough for a white icon on top.
var categoryIconColors = map[string]string{
	"utensils": "#EA580C", "cart": "#65A30D", "coffee": "#B45309", "cup": "#C026D3", "cake": "#DB2777",
	"car": "#2563EB", "bus": "#0284C7", "flame": "#DC2626", "fuel": "#CA8A04", "plane": "#0891B2", "bed": "#7C3AED",
	"home": "#0D9488", "bolt": "#D97706", "droplet": "#0EA5E9", "wifi": "#4F46E5", "smartphone": "#6366F1",
	"trash": "#57534E", "wrench": "#475569", "shield": "#16A34A",
	"heart": "#E11D48", "pill": "#F43F5E", "dumbbell": "#15803D", "scissors": "#A21CAF", "shirt": "#9333EA",
	"graduation": "#1D4ED8", "book": "#4338CA", "baby": "#EC4899", "paw": "#92400E", "gift": "#EF4444",
	"film": "#7E22CE", "music": "#A855F7", "gamepad": "#0F766E", "dice": "#4D7C0F", "ticket": "#C2410C",
	"piggy": "#059669", "trending-up": "#16A34A", "coins": "#D97706", "wallet": "#0E7490", "banknote": "#16A34A",
	"laptop": "#6D28D9", "bag": "#BE123C", "receipt": "#0891B2", "briefcase": "#374151", "tag": categoryNeutralColor,
}

// Colours for names the keyword table does not know. The same name always
// lands on the same colour.
var categoryFallbackColors = []string{
	"#EA580C", "#2563EB", "#16A34A", "#9333EA", "#DB2777", "#0891B2", "#D97706", "#DC2626",
	"#4F46E5", "#0D9488", "#C026D3", "#65A30D", "#0284C7", "#E11D48", "#7C3AED", "#CA8A04",
}

// Extra words, checked after the main table in trends.go.
var extraCategoryWords = map[string]string{
	"salary": "banknote", "wage": "banknote", "payroll": "banknote", "bonus": "banknote",
	"commission": "banknote", "freelance": "banknote", "income": "banknote", "refund": "banknote",
	"cashback": "banknote", "earning": "banknote",
	"family": "gift", "parent": "gift", "mum": "gift", "mom": "gift", "dad": "gift", "remittance": "gift",
	"pos": "receipt", "withdrawal": "receipt", "atm": "receipt", "transfer": "receipt",
	"charge": "receipt", "bank": "receipt", "stamp": "receipt", "penalty": "receipt",
	"inventory": "cart", "dining": "utensils", "fun": "gamepad", "leisure": "ticket", "hobby": "ticket",
	"skincare": "scissors", "perfume": "scissors", "fragrance": "scissors",
	"solar": "bolt", "inverter": "bolt", "camera": "film", "photography": "film",
}

// matchCategoryIcon finds the icon a name means: a whole word first, then a
// longer word that contains a known keyword ("foodstuffs", "netflixsub").
func matchCategoryIcon(name string) (string, bool) {
	name = strings.ToLower(strings.TrimSpace(name))
	if name == "" {
		return "", false
	}
	words := categoryWords(name)
	for _, w := range words {
		for _, form := range []string{w, strings.TrimSuffix(w, "s"), strings.TrimSuffix(w, "es")} {
			if icon, ok := categoryWordIcons[form]; ok {
				return icon, true
			}
			if icon, ok := extraCategoryWords[form]; ok {
				return icon, true
			}
		}
	}
	for _, w := range words {
		if len(w) < 6 {
			continue
		}
		for _, rule := range categoryRuleIcons {
			for _, kw := range rule.words {
				if len(kw) >= 4 && strings.Contains(w, kw) {
					return rule.icon, true
				}
			}
		}
	}
	return "", false
}

// catColor returns a hex colour for a category name.
func catColor(name string) string {
	name = strings.ToLower(strings.TrimSpace(name))
	if name == "" {
		return categoryNeutralColor
	}
	if icon, ok := matchCategoryIcon(name); ok {
		if c, ok := categoryIconColors[icon]; ok {
			return c
		}
	}
	h := uint32(2166136261)
	for _, r := range name {
		h = (h ^ uint32(r)) * 16777619
	}
	return categoryFallbackColors[h%uint32(len(categoryFallbackColors))]
}

// Categories a new account starts with. Every one can be renamed or removed.
var defaultCategoryList = []string{"Food", "Transport", "Bills", "Rent", "Airtime & Data", "Health", "Shopping", "Savings"}

func defaultCategories() []string { return append([]string(nil), defaultCategoryList...) }
