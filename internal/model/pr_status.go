package model

// Unknown API values remain visible facts, never inferred merge readiness.
func PRStatusLabel(lifecycle, common string) string {
	if lifecycle == "merged" {
		return "Merged"
	}
	labels := map[string]string{
		"draft": "Draft", "discarded": "Discarded", "merged": "Merged",
		"waiting_for_review": "Waiting for review", "waiting_for_changes": "Waiting for changes",
		"waiting_for_checks": "Waiting for checks", "review_completed": "Review completed",
		"ready_to_merge": "Ready to merge",
	}
	if label := labels[common]; label != "" {
		return label
	}
	if common != "" {
		return "Unknown · " + common
	}
	if lifecycle != "" {
		return "Unknown · " + lifecycle
	}
	return ""
}
