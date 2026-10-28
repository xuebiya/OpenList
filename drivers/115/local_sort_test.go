package _115

import (
	"context"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/OpenListTeam/OpenList/v4/internal/driver"
	"github.com/OpenListTeam/OpenList/v4/internal/model"
	"github.com/OpenListTeam/OpenList/v4/internal/op"
)

// Keep the real driver configuration and replace only the remote listing.
// These tests exercise OpenList's listing and cache without a 115 account.
type localSortFixture struct {
	*Pan115
	calls int
}

func (d *localSortFixture) GetRoot(context.Context) (model.Obj, error) {
	return &model.Object{ID: "0", Name: "root", IsFolder: true}, nil
}

func (d *localSortFixture) List(context.Context, model.Obj, model.ListArgs) ([]model.Obj, error) {
	d.calls++
	return []model.Obj{
		&model.Object{Name: "file2.txt", Size: 30, Modified: time.Unix(200, 0)},
		&model.Object{Name: "file10.txt", Size: 10, Modified: time.Unix(300, 0)},
		&model.Object{Name: "file1.txt", Size: 20, Modified: time.Unix(100, 0)},
	}, nil
}

var _ driver.Driver = (*localSortFixture)(nil)

func TestPan115LocalSortOptions(t *testing.T) {
	info, ok := op.GetDriverInfoMap()["115 Cloud"]
	if !ok {
		t.Fatal("115 Cloud was not registered")
	}
	t.Logf("LocalSort=%t", info.Config.LocalSort)
	if !info.Config.LocalSort {
		t.Error("115 Cloud must enable OpenList local sorting")
	}
	for name, want := range map[string]string{
		"order_by":        "name,size,modified",
		"order_direction": "asc,desc",
	} {
		found := false
		for _, item := range info.Common {
			if item.Name == name {
				found = true
				t.Logf("common.%s=%s", name, item.Options)
				if item.Type != "select" || item.Options != want {
					t.Errorf("common.%s = %+v, want select with options %q", name, item, want)
				}
			}
		}
		if !found {
			t.Errorf("missing common.%s", name)
		}
	}
}

func TestPan115LocalSortListing(t *testing.T) {
	tests := []struct {
		name string
		sort model.Sort
		want []string
	}{
		{"name_asc", model.Sort{OrderBy: "name", OrderDirection: "asc"}, []string{"file1.txt", "file2.txt", "file10.txt"}},
		{"name_desc", model.Sort{OrderBy: "name", OrderDirection: "desc"}, []string{"file10.txt", "file2.txt", "file1.txt"}},
		{"size_asc", model.Sort{OrderBy: "size", OrderDirection: "asc"}, []string{"file10.txt", "file1.txt", "file2.txt"}},
		{"size_desc", model.Sort{OrderBy: "size", OrderDirection: "desc"}, []string{"file2.txt", "file1.txt", "file10.txt"}},
		{"modified_asc", model.Sort{OrderBy: "modified", OrderDirection: "asc"}, []string{"file1.txt", "file2.txt", "file10.txt"}},
		{"modified_desc", model.Sort{OrderBy: "modified", OrderDirection: "desc"}, []string{"file10.txt", "file2.txt", "file1.txt"}},
		{"unspecified_preserves_api_order", model.Sort{}, []string{"file2.txt", "file10.txt", "file1.txt"}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			d := &localSortFixture{Pan115: &Pan115{Storage: model.Storage{
				MountPath:       "/115-local-sort-test/" + t.Name(),
				CacheExpiration: 30,
				Sort:            tc.sort,
			}}}
			op.Cache.DeleteDirectory(d, "/")
			t.Cleanup(func() { op.Cache.DeleteDirectory(d, "/") })
			for i, label := range []string{"fresh", "cached", "refresh"} {
				objs, err := op.List(context.Background(), d, "/", model.ListArgs{SkipHook: true, Refresh: i == 2})
				if err != nil {
					t.Fatalf("%s listing: %v", label, err)
				}
				got := make([]string, len(objs))
				for j, obj := range objs {
					got[j] = obj.GetName()
				}
				t.Logf("%s=%s", label, strings.Join(got, ","))
				if !reflect.DeepEqual(got, tc.want) {
					t.Errorf("%s order = %v, want %v", label, got, tc.want)
				}
				wantCalls := 1
				if i == 2 {
					wantCalls = 2
				}
				if d.calls != wantCalls {
					t.Errorf("%s remote listing calls = %d, want %d", label, d.calls, wantCalls)
				}
			}
		})
	}
}
