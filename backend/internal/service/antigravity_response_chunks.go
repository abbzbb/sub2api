package service

// mergeAntigravityResponseChunk retains terminal metadata even when the final
// event contains no content, or when usage arrives after finishReason. Content
// parts are accumulated separately and installed once the stream is complete.
func mergeAntigravityResponseChunk(previous, chunk map[string]any) map[string]any {
	merged := shallowCopyMap(previous)
	if merged == nil {
		merged = make(map[string]any)
	}
	for key, value := range chunk {
		if key != "candidates" {
			merged[key] = value
		}
	}
	oldCandidates, _ := previous["candidates"].([]any)
	newCandidates, ok := chunk["candidates"].([]any)
	if !ok || len(newCandidates) == 0 {
		return merged
	}
	candidates := append([]any(nil), oldCandidates...)
	for i, raw := range newCandidates {
		candidate, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		for len(candidates) <= i {
			candidates = append(candidates, nil)
		}
		old, _ := candidates[i].(map[string]any)
		updated := shallowCopyMap(old)
		if updated == nil {
			updated = make(map[string]any)
		}
		for key, value := range candidate {
			if key == "content" {
				if content, ok := value.(map[string]any); ok {
					oldContent, _ := old["content"].(map[string]any)
					combined := shallowCopyMap(content)
					for field, oldValue := range oldContent {
						if _, exists := combined[field]; !exists {
							combined[field] = oldValue
						}
					}
					value = combined
				}
			}
			updated[key] = value
		}
		candidates[i] = updated
	}
	merged["candidates"] = candidates
	return merged
}
