package counter

import "testing"

func TestVisibleZeroValueIsUsable(t *testing.T) {
	var c Counter
	c.Add("a")
	if c.Count("a") != 1 {
		t.Fatalf("count = %d", c.Count("a"))
	}
}
