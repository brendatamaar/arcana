package derive

import "testing"

func TestVisibleDerivedSlicesAreIndependent(t *testing.T) {
	base := make([]string, 1, 10)
	base[0] = "a"
	x := WithItem(base, "x")
	y := WithItem(base, "y")
	if x[1] != "x" || y[1] != "y" {
		t.Fatalf("x=%v y=%v", x, y)
	}
}
