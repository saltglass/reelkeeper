package library

import (
	"context"
	"testing"

	"github.com/saltglass/reelkeeper/internal/store"
	"github.com/saltglass/reelkeeper/internal/store/storetest"
)

func TestTags(t *testing.T) {
	storetest.Each(t, func(t *testing.T, db *store.Store) {
		ctx := context.Background()
		f := newFixture(t, db)
		a, err := f.svc.CreateTag(ctx, " 1 - Dan ")
		if err != nil || a.Label != "1 - dan" {
			t.Fatalf("create = %+v, %v", a, err)
		}
		again, _ := f.svc.CreateTag(ctx, "1 - DAN")
		if again.ID != a.ID {
			t.Error("the same label made a second tag")
		}
		b, _ := f.svc.CreateTag(ctx, "kids")
		if _, err := f.svc.RenameTag(ctx, b.ID, "1 - dan"); err == nil {
			t.Error("renamed onto an existing label")
		}
		if _, err := f.svc.CreateTag(ctx, "  "); err == nil {
			t.Error("an empty label was accepted")
		}

		m, err := f.svc.AddMovie(ctx, 693134, 1, f.hd.ID, true)
		if err != nil {
			t.Fatal(err)
		}
		v := m.Versions[0].ID
		if err := f.svc.SetVersionTags(ctx, v, []int64{a.ID, b.ID, a.ID}); err != nil {
			t.Fatal(err)
		}
		got, _ := f.svc.VersionTags(ctx, []int64{v})
		if len(got[v]) != 2 {
			t.Errorf("tags = %v", got)
		}
		if err := f.svc.SetVersionTags(ctx, v, []int64{999}); err == nil {
			t.Error("an unknown tag was accepted")
		}
		list, _ := f.svc.Tags(ctx)
		if len(list) != 2 || list[0].Label != "1 - dan" {
			t.Errorf("list = %+v", list)
		}
	})
}
