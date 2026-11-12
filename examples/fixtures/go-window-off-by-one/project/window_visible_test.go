package window

import (
	"reflect"
	"testing"
)

func TestVisibleAllWindows(t *testing.T) {
	got := Window([]int{1, 2, 3, 4}, 2)
	want := [][]int{{1, 2}, {2, 3}, {3, 4}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v want %v", got, want)
	}
}
