package place

import (
	"cmp"
	"fmt"
	"slices"
	"strings"
)

const (
	maxCardMapFeatures = 2
	maxCardPOIs        = 4
)

func (result Result) String() string {
	return RenderCard(result)
}

func RenderCard(result Result) string {
	var b strings.Builder
	b.WriteString("## Place Context\n\n")
	writeAddress(&b, result.Address, result.Area)
	writeMapFeatures(&b, result)
	writePOIs(&b, result)
	return strings.TrimRight(b.String(), "\n")
}

func writeAddress(b *strings.Builder, address *Address, area []AreaLevel) {
	if address == nil {
		b.WriteString("Address: unavailable\n\n")
		return
	}
	b.WriteString("Address:\n")
	if value := displayAddress(address); value != "" {
		fmt.Fprintf(b, "- Full: %s\n", value)
	}
	if value := areaTrail(address, area); value != "" {
		fmt.Fprintf(b, "- Area: %s\n", value)
	}
	if strings.TrimSpace(address.PostalCode) != "" {
		fmt.Fprintf(b, "- Postal code: %s\n", strings.TrimSpace(address.PostalCode))
	}
	if strings.TrimSpace(address.TimeZone) != "" {
		fmt.Fprintf(b, "- Time zone: %s\n", strings.TrimSpace(address.TimeZone))
	}
	b.WriteString("\n")
}

func writeMapFeatures(b *strings.Builder, result Result) {
	status := firstNonEmpty(result.POIStatus, poiStatus(result))
	noPOIs := status == POIStatusNone
	addressText := strings.Join(compactStrings([]string{
		displayAddress(result.Address),
		areaTrail(result.Address, result.Area),
	}), " ")
	features := usefulMapFeatures(mapFeaturesForCard(result), noPOIs, addressText)
	if len(features) == 0 {
		return
	}
	b.WriteString("Map context:\n")
	for _, feature := range features {
		detail := compactStrings([]string{feature.Kind, feature.Relation, distanceLabel(feature.DistanceM)})
		if len(detail) > 0 {
			fmt.Fprintf(b, "- %s (%s)\n", feature.Name, strings.Join(detail, ", "))
		} else {
			fmt.Fprintf(b, "- %s\n", feature.Name)
		}
	}
	b.WriteString("\n")
}

func writePOIs(b *strings.Builder, result Result) {
	status := firstNonEmpty(result.POIStatus, poiStatus(result))
	if status == POIStatusProviderError {
		return
	}

	b.WriteString("Nearby POIs:\n")
	if status == POIStatusNone {
		fmt.Fprintf(b, "- No named nearby POIs within %.0fm\n", result.RadiusMeters)
		return
	}

	candidates := usefulPOIs(result.POICandidates)
	if len(candidates) == 0 {
		b.WriteString("- No useful named nearby POIs\n")
		return
	}
	for _, candidate := range candidates {
		label := strings.TrimSpace(candidate.Name)
		detail := compactStrings([]string{displayPOICategory(candidate), distanceLabel(candidate.DistanceM)})
		if len(detail) > 0 {
			fmt.Fprintf(b, "- %s (%s)\n", label, strings.Join(detail, ", "))
		} else {
			fmt.Fprintf(b, "- %s\n", label)
		}
	}
}

type cardMapFeature struct {
	Name      string
	Kind      string
	Relation  string
	DistanceM float64
}

func mapFeaturesForCard(result Result) []MapFeature {
	features := append([]MapFeature{}, result.MapFeatures...)
	if result.Address == nil {
		return features
	}
	for _, name := range result.Address.AreasOfInterest {
		name = strings.TrimSpace(name)
		if name == "" {
			continue
		}
		features = append(features, MapFeature{
			Name:     name,
			Kind:     "area of interest",
			Relation: "area",
			Source:   result.Address.Source,
		})
	}
	return features
}

func usefulMapFeatures(features []MapFeature, noPOIs bool, addressArea string) []cardMapFeature {
	slices.SortStableFunc(features, func(a, b MapFeature) int {
		if order := cmp.Compare(mapFeatureRank(a), mapFeatureRank(b)); order != 0 {
			return order
		}
		return cmp.Compare(a.DistanceM, b.DistanceM)
	})

	out := []cardMapFeature{}
	seen := map[string]bool{}
	for _, feature := range features {
		cardFeature, ok := normalizeMapFeature(feature, noPOIs)
		if !ok {
			continue
		}
		if cardFeature.Kind == "area" && containsNormalized(addressArea, cardFeature.Name) {
			continue
		}
		key := normalizedKey(cardFeature.Name + "|" + cardFeature.Kind)
		if key == "" || seen[key] {
			continue
		}
		seen[key] = true
		out = append(out, cardFeature)
		if len(out) == maxCardMapFeatures {
			break
		}
	}
	return out
}

func mapFeatureRank(feature MapFeature) int {
	kind := normalizeMapKind(feature.Kind)
	name := strings.TrimSpace(feature.Name)
	switch {
	case name != "" && kind != "area":
		return 0
	case kind == "area":
		return 1
	case name != "":
		return 2
	default:
		return 3
	}
}

func normalizeMapFeature(feature MapFeature, noPOIs bool) (cardMapFeature, bool) {
	name := cleanFeatureName(feature.Name)
	kind := normalizeMapKind(feature.Kind)
	if lowValueMapName(name) {
		return cardMapFeature{}, false
	}
	if kind == "" && name == "" {
		return cardMapFeature{}, false
	}
	if kind == "" && name != "" && !noPOIs {
		return cardMapFeature{}, false
	}
	if name == "" {
		if !unnamedMapKindUseful(kind) {
			return cardMapFeature{}, false
		}
		name = kind
		kind = ""
	}
	if !noPOIs && lowValueMapFeature(kind) {
		return cardMapFeature{}, false
	}
	if strings.EqualFold(name, kind) {
		kind = ""
	}
	return cardMapFeature{
		Name:      name,
		Kind:      kind,
		Relation:  normalizeRelation(feature.Relation),
		DistanceM: feature.DistanceM,
	}, true
}

func usefulPOIs(candidates []POICandidate) []POICandidate {
	slices.SortStableFunc(candidates, comparePOICandidates)

	out := []POICandidate{}
	seen := map[string]bool{}
	categoryCounts := map[string]int{}
	for _, candidate := range candidates {
		name := strings.TrimSpace(candidate.Name)
		category := displayPOICategory(candidate)
		if name == "" || !usefulPOI(candidate) {
			continue
		}
		key := poiStem(name, category)
		if key == "" || seen[key] {
			continue
		}
		if categoryCounts[category] >= maxPOIsForCategory(category) {
			continue
		}
		seen[key] = true
		categoryCounts[category]++
		out = append(out, candidate)
		if len(out) == maxCardPOIs {
			break
		}
	}
	return out
}

func usefulPOI(candidate POICandidate) bool {
	category := displayPOICategory(candidate)
	name := strings.ToLower(strings.TrimSpace(candidate.Name))
	if lowValuePOIName(name) {
		return false
	}
	if category == "" {
		return relevantPOIName(name)
	}
	return usefulPOICategory(category) || relevantPOIName(name)
}

func relevantPOIName(name string) bool {
	return strings.Contains(name, "airport") ||
		strings.Contains(name, "terminal") ||
		strings.Contains(name, "station") ||
		strings.Contains(name, "pier") ||
		strings.Contains(name, "gate") ||
		strings.Contains(name, "security") ||
		strings.Contains(name, "baggage") ||
		strings.Contains(name, "lounge") ||
		strings.Contains(name, "trail") ||
		strings.Contains(name, "falls") ||
		strings.Contains(name, "bridge") ||
		strings.Contains(name, "hotel")
}

func lowValuePOIName(name string) bool {
	return strings.Contains(name, "charging") ||
		strings.Contains(name, "ev charger") ||
		strings.Contains(name, "atm") ||
		strings.Contains(name, "cash machine")
}

func maxPOIsForCategory(category string) int {
	switch category {
	case "hotel", "public transport":
		return 1
	case "restaurant", "cafe":
		return 2
	case "":
		return maxCardPOIs
	default:
		return 2
	}
}

func displayPOICategory(candidate POICandidate) string {
	category := shortCategory(candidate.Category)
	inferred := inferredPOICategory(candidate.Name)
	switch category {
	case "", "parking", "store", "shop", "atm":
		return inferred
	default:
		if usefulPOICategory(category) {
			return category
		}
		return inferred
	}
}

func inferredPOICategory(name string) string {
	name = strings.ToLower(strings.TrimSpace(name))
	switch {
	case strings.Contains(name, "airport"):
		return "airport"
	case strings.Contains(name, "terminal"):
		return "terminal"
	case strings.Contains(name, "railway station") || strings.Contains(name, "train station"):
		return "rail station"
	case strings.Contains(name, "station") || strings.Contains(name, " stop "):
		return "public transport"
	case strings.Contains(name, "hotel"):
		return "hotel"
	case strings.Contains(name, "trail"):
		return "trail"
	case strings.Contains(name, "falls"):
		return "waterfall"
	case strings.Contains(name, "bridge"):
		return "bridge"
	default:
		return ""
	}
}

func usefulPOICategory(category string) bool {
	switch category {
	case "airport", "hotel", "restaurant", "cafe", "landmark", "public transport",
		"museum", "park", "national park", "beach", "campground", "marina",
		"theater", "music venue", "stadium", "university", "school", "nightlife",
		"terminal", "rail station", "trail", "waterfall", "bridge":
		return true
	default:
		return false
	}
}
