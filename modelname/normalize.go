package modelname

import (
	"regexp"
	"strings"
)

var (
	dateSuffixPattern = regexp.MustCompile(`(?i)([-_])20\d{2}[-_]?\d{2}[-_]?\d{2}$`)
	shortDatePattern  = regexp.MustCompile(`(?i)([-_])\d{6}$`)
	oneMSuffixPattern = regexp.MustCompile(`(?i)\s*\[(1m|\d+m)\]$`)
	dupDashPattern   = regexp.MustCompile(`[-_]{2,}`)
	versionPat       = regexp.MustCompile(`(?i)([a-z]+-?(?:m|t)\d+)[-.](\d+(?:[._-]\d+)*)`)
	simpleVersionPat = regexp.MustCompile(`(?i)^([a-z]+)-(\d+-\d+)`)
	minimaxVersionPat = regexp.MustCompile(`(?i)^(minimax)-m(\d+(?:[._-]\d+)*)$`)
	plainVersionPat   = regexp.MustCompile(`(?i)^([a-z]+)-(\d+(?:[._-]\d+)*)$`)
	featureMap        = map[string]bool{
		"highspeed": true, "thinking": true, "reasoning": true,
		"flash": true, "turbo": true, "preview": true,
		"pro": true, "max": true, "mini": true, "nano": true,
		"chat": true, "instruct": true, "coder": true, "code": true,
		"vision": true, "audio": true, "air": true,
	}
)

func NormalizeRouteKey(model string) string {
	model = strings.TrimSpace(strings.ToLower(model))
	if model == "" {
		return ""
	}
	if idx := strings.LastIndex(model, "/"); idx >= 0 {
		model = model[idx+1:]
	}
	model = oneMSuffixPattern.ReplaceAllString(model, "")
	model = dateSuffixPattern.ReplaceAllString(model, "")
	model = shortDatePattern.ReplaceAllString(model, "")
	model = dupDashPattern.ReplaceAllString(model, "-")
	model = normalizeVersionDots(model)
	return strings.Trim(model, "-_ ")
}

func normalizeVersionDots(model string) string {
	idx := versionPat.FindStringIndex(model)
	if idx != nil && len(idx) >= 2 {
		matches := versionPat.FindStringSubmatch(model)
		if len(matches) >= 3 {
			family := matches[1]
			version := matches[2]
			prefix := model[:idx[0]]
			rest := model[idx[1]:]
			normalizedVer := strings.ReplaceAll(version, "-", ".")
			normalizedVer = strings.ReplaceAll(normalizedVer, "_", ".")
			result := prefix + family + "." + normalizedVer + rest
			result = strings.ReplaceAll(result, ".-", "-")
			result = strings.ReplaceAll(result, "-.", "-")
			return result
		}
	}

	simpleIdx := simpleVersionPat.FindStringIndex(model)
	if simpleIdx != nil && simpleIdx[0] == 0 {
		matches := simpleVersionPat.FindStringSubmatch(model)
		if len(matches) >= 3 {
			family := matches[1]
			version := matches[2]
			rest := model[simpleIdx[1]:]
			if rest == "" || strings.HasPrefix(rest, "-") && !strings.ContainsAny(rest, "0123456789") {
				return family + "-" + strings.ReplaceAll(version, "-", ".") + rest
			}
		}
	}
	return model
}

func NormalizeModelRef(model string) (provider string, baseModel string, version string) {
	model = strings.TrimSpace(strings.ToLower(model))
	if idx := strings.Index(model, "/"); idx >= 0 {
		provider = model[:idx]
		model = model[idx+1:]
	}
	norm := NormalizeRouteKey(model)

	if matches := minimaxVersionPat.FindStringSubmatch(norm); len(matches) >= 3 {
		return provider, matches[1], matches[2]
	}

	matches := versionPat.FindStringSubmatch(norm)
	if len(matches) >= 3 {
		family := strings.ReplaceAll(matches[1], "-", "")
		family = strings.ReplaceAll(family, "_", "")
		version = strings.ReplaceAll(matches[2], "-", ".")
		version = strings.ReplaceAll(version, "_", ".")
		return provider, family, version
	}
	if matches := simpleVersionPat.FindStringSubmatch(norm); len(matches) >= 3 {
		family := strings.ReplaceAll(matches[1], "-", "")
		family = strings.ReplaceAll(family, "_", "")
		version = strings.ReplaceAll(matches[2], "-", ".")
		version = strings.ReplaceAll(version, "_", ".")
		return provider, family, version
	}
	if matches := plainVersionPat.FindStringSubmatch(norm); len(matches) >= 3 {
		family := strings.ReplaceAll(matches[1], "-", "")
		family = strings.ReplaceAll(family, "_", "")
		version = strings.ReplaceAll(matches[2], "-", ".")
		version = strings.ReplaceAll(version, "_", ".")
		return provider, family, version
	}
	baseModel = strings.ReplaceAll(norm, "-", "")
	baseModel = strings.ReplaceAll(baseModel, "_", "")
	baseModel = strings.ReplaceAll(baseModel, ".", "")
	return provider, baseModel, version
}

func ExtractFeatures(model string) []string {
	norm := NormalizeRouteKey(model)
	tokens := regexp.MustCompile(`[^a-z0-9]+`).Split(norm, -1)
	var features []string
	for _, token := range tokens {
		token = strings.TrimSpace(token)
		if token != "" && len(token) >= 3 && featureMap[token] {
			features = append(features, token)
		}
	}
	return features
}

func MatchModelOffer(clientModel string, offerModel string) bool {
	clientNorm := NormalizeRouteKey(clientModel)
	offerNorm := NormalizeRouteKey(offerModel)
	if clientNorm == offerNorm {
		return true
	}
	_, clientBase, clientVer := NormalizeModelRef(clientModel)
	_, offerBase, offerVer := NormalizeModelRef(offerModel)
	if clientBase != offerBase {
		return false
	}
	if clientVer != "" && offerVer != "" && clientVer != offerVer {
		return false
	}
	clientFeatures := ExtractFeatures(clientModel)
	offerFeatures := ExtractFeatures(offerModel)
	if len(offerFeatures) > 0 && !hasOverlap(clientFeatures, offerFeatures) {
		return false
	}
	return true
}

func hasOverlap(a, b []string) bool {
	if len(a) == 0 || len(b) == 0 {
		return true
	}
	set := make(map[string]bool)
	for _, v := range a {
		set[v] = true
	}
	for _, v := range b {
		if set[v] {
			return true
		}
	}
	return false
}

func StandardizeName(rawName string) string {
	model := strings.TrimSpace(strings.ToLower(rawName))
	if model == "" {
		return ""
	}

	// Strip provider prefix (e.g., "scnet/minimax-m2.5" → "minimax-m2.5")
	if idx := strings.LastIndex(model, "/"); idx >= 0 {
		model = model[idx+1:]
	}

	// Strip [1M] suffix
	model = oneMSuffixPattern.ReplaceAllString(model, "")

	// Strip date suffixes
	model = dateSuffixPattern.ReplaceAllString(model, "")
	model = shortDatePattern.ReplaceAllString(model, "")

	// Normalize duplicate dashes
	model = dupDashPattern.ReplaceAllString(model, "-")

	// Handle minimax: minimax-m2.7, minimax-m2.5 (keep m prefix as part of version)
	if strings.HasPrefix(model, "minimax-m") {
		// Extract version from minimax-mX.X pattern
		matches := minimaxVersionPat.FindStringSubmatch(model)
		if len(matches) >= 3 {
			return "minimax-m" + matches[2]
		}
		// Fallback: try to extract anyway
		if idx := strings.Index(model, "-m"); idx >= 0 && idx+2 < len(model) {
			version := model[idx+2:]
			// Remove any features after the version
			if dashIdx := strings.Index(version, "-"); dashIdx > 0 {
				version = version[:dashIdx]
			}
			if regexp.MustCompile(`^\d+\.\d+$`).MatchString(version) {
				return "minimax-m" + version
			}
		}
	}

	// Handle GLM: glm-4-7 → glm-4.7, glm-4-5-flash → glm-4.5-flash
	if strings.HasPrefix(model, "glm-") {
		// Convert dash-separated version to dot (glm-4-7 → glm-4.7)
		replacer := regexp.MustCompile(`(?i)^(glm-\d+)-(\d+)(.*)$`)
		matches := replacer.FindStringSubmatch(model)
		if len(matches) >= 4 {
			return matches[1] + "." + matches[2] + matches[3]
		}
	}

	// For other models, just clean up
	model = strings.Trim(model, "-_ ")

	return model
}