package yaft

// NormaliseFeature turns one entry of a parsed JSON response into a Feature,
// whichever field spelling it arrived in.
//
// Fields are read by presence, not by content (R23): a present but empty
// "value": "" wins over a capitalised "Value", because falling through to the
// other spelling would turn an off feature on. Backends from 0.2.0 on send
// only the lowercase spelling; the capitalised one is read because older
// instances are still around (R22a).
func NormaliseFeature(raw map[string]any) Feature {
	feature := Feature{
		Key:        text(field(raw, "key", "Key")),
		Value:      text(field(raw, "value", "Value")),
		ActiveAt:   text(field(raw, "activeAt", "ActiveAt")),
		DisabledAt: text(field(raw, "disabledAt", "DisabledAt")),
		Tags:       []string{},
	}
	// Filtered rather than asserted: a mixed array would otherwise put a
	// non-string into []string.
	if tags, ok := field(raw, "tags", "Tags").([]any); ok {
		for _, tag := range tags {
			if s, ok := tag.(string); ok {
				feature.Tags = append(feature.Tags, s)
			}
		}
	}
	return feature
}

// NormaliseCollection turns a parsed JSON response into features keyed by
// their key. Three envelopes are accepted, because the backend uses all three
// (R22): {"toggles": [...]} for a UUID group, {"value": [...]} for the same
// thing under another name, and a flat object for a single toggle. An entry
// without a usable key is skipped rather than stored under "" (R25).
func NormaliseCollection(response any) map[string]Feature {
	data := map[string]Feature{}
	body, ok := response.(map[string]any)
	if !ok {
		return data
	}

	entries := []any{body}
	if toggles, ok := body["toggles"].([]any); ok {
		entries = toggles
	} else if value, ok := body["value"].([]any); ok {
		entries = value
	}

	for _, entry := range entries {
		raw, ok := entry.(map[string]any)
		if !ok {
			continue
		}
		if feature := NormaliseFeature(raw); feature.Key != "" {
			data[feature.Key] = feature
		}
	}
	return data
}

// NormaliseBooleans turns a boolean-shape payload, {"myToggle": true}, into
// provider data. Only real booleans are kept (R29): "true", "false", 1 and
// null are dropped, so their keys read as missing and therefore off.
func NormaliseBooleans(response any) map[string]bool {
	data := map[string]bool{}
	body, ok := response.(map[string]any)
	if !ok {
		return data
	}
	for key, value := range body {
		if b, ok := value.(bool); ok {
			data[key] = b
		}
	}
	return data
}

func field(raw map[string]any, lower, upper string) any {
	if v, ok := raw[lower]; ok {
		return v
	}
	return raw[upper]
}

// text reads a string field; anything that is not a string is not set (R33).
// Key and value are strings, and JSON booleans belong in the boolean shape:
// fmt.Sprint would turn "value": true into "true" and the feature on. The
// backend sends null for an unset bound, fixtures send ""; both mean none (R24).
func text(value any) string {
	s, _ := value.(string)
	return s
}
