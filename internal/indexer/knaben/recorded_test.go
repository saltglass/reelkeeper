package knaben

import (
	"os"
	"testing"
)

// TestRecordedKnaben reads an answer in the shape of Knaben's API (invented
// releases, trackers and hashes; some hits have no seeders and are dropped).
func TestRecordedKnaben(t *testing.T) {
	data, err := os.ReadFile("../../../testdata/indexers/knaben-search-made-up.json")
	if err != nil {
		t.Fatal(err)
	}
	res, err := parse(data)
	if err != nil {
		t.Fatal(err)
	}
	if len(res) < 20 {
		t.Fatalf("only %d releases", len(res))
	}
	for _, r := range res {
		if r.Title == "" || r.Size <= 0 || r.Seeders <= 0 || r.PublishDate.IsZero() || (r.MagnetURL == "" && r.DownloadURL == "") {
			t.Errorf("incomplete: %+v", r)
		}
	}
	t.Logf("%d releases; first %q", len(res), res[0].Title)
}
