package validation

import (
	"fmt"
	"testing"
)

func TestErrors(t *testing.T) {
	var e Errors
	if e.Err() != nil {
		t.Fatal("empty Errors should be nil")
	}
	e.Add("b", "second field")
	e.Add("a", "first field")
	e.Add("a", "ignored, a already has a problem")
	if e["a"] != "first field" || len(e) != 2 {
		t.Errorf("unexpected %v", e)
	}
	err := fmt.Errorf("saving: %w", e.Err())
	got, ok := As(err)
	if !ok || len(got) != 2 {
		t.Fatalf("As: %v %v", got, ok)
	}
	if want := "invalid input: a: first field; b: second field"; got.Error() != want {
		t.Errorf("Error() = %q, want %q", got.Error(), want)
	}
	if _, ok := As(fmt.Errorf("other")); ok {
		t.Error("As should be false for other errors")
	}
}
