package collection

// SnapshotGeneration returns the canonical generation for a set of collection
// items. Callers outside collection must use this function instead of
// reimplementing the manifest schema used to derive a snapshot generation.
func SnapshotGeneration(items []Item) string {
	canonical := cloneItems(items)
	sortItems(canonical)
	return generation(canonical)
}
