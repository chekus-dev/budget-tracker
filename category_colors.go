package main

import "strings"

const categoryNeutralColor = "#5B6B7A"

// Colour per icon, so a category's colour follows what its name means.
var categoryIconColors = map[string]string{
	"utensils": "#C2410C", "cart": "#B45309", "coffee": "#7C4A2D", "cup": "#A21CAF", "cake": "#BE185D",
	"car": "#1D4ED8", "bus": "#0369A1", "flame": "#DC2626", "fuel": "#92400E", "plane": "#0E7490", "bed": "#6D28D9",
	"home": "#0F766E", "bolt": "#A16207", "droplet": "#1E6FA8", "wifi": "#4F46E5", "smartphone": "#4338CA",
	"trash": "#57534E", "wrench": "#475569", "shield": "#166534",
	"heart": "#BE123C", "pill": "#9D174D", "dumbbell": "#15803D", "scissors": "#86198F", "shirt": "#7E22CE",
	"graduation": "#1E40AF", "book": "#3730A3", "baby": "#DB2777", "paw": "#854D0E", "gift": "#B91C1C",
	"film": "#6B21A8", "music": "#9333EA", "gamepad": "#115E59", "dice": "#365314", "ticket": "#9A3412",
	"piggy": "#0B7A6B", "trending-up": "#166D4A", "coins": "#8A5A00", "wallet": "#1D6F6F", "banknote": "#047857",
	"laptop": "#334155", "bag": "#9F1239", "receipt": "#4D7C0F", "briefcase": "#1F2937", "tag": "#5B6B7A",
}

// Colours for names the keyword table does not know; the same name always
// lands on the same colour.
var categoryFallbackColors = []string{
	"#C2410C", "#1D4ED8", "#0F766E", "#9333EA", "#BE185D", "#15803D",
	"#A16207", "#0369A1", "#86198F", "#B91C1C", "#4338CA", "#475569",
}

// catColor returns a hex colour for a category name.
func catColor(name string) string {
	name = strings.ToLower(strings.TrimSpace(name))
	if name == "" {
		return categoryNeutralColor
	}
	for _, w := range categoryWords(name) {
		for _, form := range []string{w, strings.TrimSuffix(w, "s"), strings.TrimSuffix(w, "es")} {
			if icon, ok := categoryWordIcons[form]; ok {
				if c, ok := categoryIconColors[icon]; ok {
					return c
				}
			}
		}
	}
	h := uint32(2166136261)
	for _, r := range name {
		h = (h ^ uint32(r)) * 16777619
	}
	return categoryFallbackColors[h%uint32(len(categoryFallbackColors))]
}
