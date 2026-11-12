// Package derive builds lists from other lists.
package derive

// WithItem returns items followed by item without modifying items.
func WithItem(items []string, item string) []string {
	return append(items, item)
}
