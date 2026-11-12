// Package window slices lists into sliding windows.
package window

// Window returns all contiguous windows of the given size.
func Window(xs []int, size int) [][]int {
	var out [][]int
	if size <= 0 {
		return out
	}
	for i := 0; i < len(xs)-size; i++ {
		out = append(out, xs[i:i+size])
	}
	return out
}
