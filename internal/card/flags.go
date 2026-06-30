package card

import "strings"

// FlagEmoji converts a full country name (as Liquipedia returns, e.g. "China")
// to a flag emoji. Returns "" for unknown names. Regions map to a globe.
func FlagEmoji(country string) string {
	iso := nameToISO(strings.TrimSpace(country))
	if iso == "" {
		return ""
	}
	if e, ok := regionEmoji[iso]; ok {
		return e
	}
	if len(iso) != 2 {
		return ""
	}
	a, b := iso[0], iso[1]
	if a < 'A' || a > 'Z' || b < 'A' || b > 'Z' {
		return ""
	}
	return string([]rune{0x1F1E6 + rune(a) - 'A', 0x1F1E6 + rune(b) - 'A'})
}

var regionEmoji = map[string]string{
	"R_SA": "🌎", "R_NA": "🌎", "R_AS": "🌏", "R_OC": "🌏",
	"R_CIS": "🌍", "R_AF": "🌍", "R_WW": "🌍",
}

func nameToISO(s string) string {
	if iso, ok := countryNameMap[s]; ok {
		return iso
	}
	return ""
}

// countryNameMap covers the nationalities that appear in WarCraft III esports
// (plus common others). Extend as needed.
var countryNameMap = map[string]string{
	"China": "CN", "South Korea": "KR", "Korea": "KR", "North Korea": "KP",
	"Taiwan": "TW", "Hong Kong": "HK", "Japan": "JP", "Mongolia": "MN",
	"Vietnam": "VN", "Thailand": "TH", "Singapore": "SG", "Malaysia": "MY",
	"Indonesia": "ID", "Philippines": "PH", "India": "IN", "Kazakhstan": "KZ",
	"Russia": "RU", "Russian Federation": "RU", "Ukraine": "UA", "Belarus": "BY",
	"Poland": "PL", "Germany": "DE", "France": "FR", "Spain": "ES", "Portugal": "PT",
	"Italy": "IT", "Netherlands": "NL", "Belgium": "BE", "Sweden": "SE",
	"Norway": "NO", "Finland": "FI", "Denmark": "DK", "Iceland": "IS",
	"United Kingdom": "GB", "England": "GB", "Scotland": "GB", "Ireland": "IE",
	"Austria": "AT", "Switzerland": "CH", "Czech Republic": "CZ", "Czechia": "CZ",
	"Slovakia": "SK", "Hungary": "HU", "Romania": "RO", "Bulgaria": "BG",
	"Greece": "GR", "Turkey": "TR", "Serbia": "RS", "Croatia": "HR",
	"Slovenia": "SI", "Estonia": "EE", "Latvia": "LV", "Lithuania": "LT",
	"Moldova": "MD", "Georgia": "GE", "Armenia": "AM", "Azerbaijan": "AZ",
	"United States": "US", "United States of America": "US", "USA": "US",
	"Canada": "CA", "Mexico": "MX", "Brazil": "BR", "Argentina": "AR",
	"Chile": "CL", "Peru": "PE", "Colombia": "CO", "Uruguay": "UY",
	"Venezuela": "VE", "Bolivia": "BO", "Ecuador": "EC", "Paraguay": "PY",
	"Australia": "AU", "New Zealand": "NZ", "South Africa": "ZA",
	"Israel": "IL", "Iran": "IR", "Saudi Arabia": "SA", "United Arab Emirates": "AE",
	"Egypt": "EG", "Morocco": "MA", "Tunisia": "TN",
	// regions without a single flag
	"Europe": "EU", "South America": "R_SA", "North America": "R_NA",
	"Asia": "R_AS", "Oceania": "R_OC", "CIS": "R_CIS", "Africa": "R_AF",
	"World": "R_WW", "International": "R_WW",
}

// FactionName maps the single-letter race code to a short display label.
func FactionName(code string) string {
	switch strings.ToLower(code) {
	case "h":
		return "Human"
	case "o":
		return "Orc"
	case "u":
		return "Undead"
	case "n":
		return "Night Elf"
	case "r":
		return "Random"
	}
	return ""
}

// FactionTag is a compact 2-3 letter race tag for headings.
func FactionTag(code string) string {
	switch strings.ToLower(code) {
	case "h":
		return "HU"
	case "o":
		return "OR"
	case "u":
		return "UD"
	case "n":
		return "NE"
	case "r":
		return "RND"
	}
	return ""
}
