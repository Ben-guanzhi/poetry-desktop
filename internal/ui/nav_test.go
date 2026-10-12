package ui

import (
	"testing"

	"poetry-desktop/internal/model"
)

// navItems and model.Datasets are two views of the same content set. They were
// coupled only by sidebar position and by Chinese name strings compared at
// runtime, so renaming or reordering a dataset in models.go moved with the
// sidebar and the failure mode was a page reading "数据集不存在". These tests
// make that drift a test failure instead.

func TestNavItemRoutesAreWellFormed(t *testing.T) {
	for i, it := range navItems {
		switch it.Kind {
		case navDataset:
			if it.Dataset == "" {
				t.Errorf("navItems[%d] %q is a dataset entry with no dataset name", i, it.Title)
				continue
			}
			if _, ok := model.GetDataset(it.Dataset); !ok {
				t.Errorf("navItems[%d] %q points at unknown dataset %q", i, it.Title, it.Dataset)
			}
		case navDynasty:
			if it.Dynasty == "" {
				t.Errorf("navItems[%d] %q is a dynasty entry with no dynasty", i, it.Title)
				continue
			}
			if !dynastyHasDataset(it.Dynasty) {
				t.Errorf("navItems[%d] %q shows dynasty %q which no dataset serves", i, it.Title, it.Dynasty)
			}
		default:
			if it.Dataset != "" || it.Dynasty != "" {
				t.Errorf("navItems[%d] %q kind %d must not carry a dataset or dynasty", i, it.Title, it.Kind)
			}
		}
	}
}

func TestNavTitlesAreUnique(t *testing.T) {
	seen := map[string]int{}
	for i, it := range navItems {
		if j, dup := seen[it.Title]; dup {
			t.Errorf("navItems[%d] and navItems[%d] both titled %q; navIndex resolves to the first", j, i, it.Title)
		}
		seen[it.Title] = i
	}
}

// TestEveryDatasetIsReachableFromNav closes the drift in the other direction:
// a dataset can be registered and downloaded from settings while staying
// unreachable from the sidebar.
func TestEveryDatasetIsReachableFromNav(t *testing.T) {
	byName := map[string]bool{}
	byDynasty := map[model.Dynasty]bool{}
	for _, it := range navItems {
		if it.Kind == navDataset {
			byName[it.Dataset] = true
		}
		if it.Kind == navDynasty {
			byDynasty[it.Dynasty] = true
		}
	}
	for _, ds := range model.Datasets {
		if byName[ds.Name] || byDynasty[ds.Dynasty] {
			continue
		}
		t.Errorf("dataset %q (dir %q) is registered but reachable from no sidebar entry", ds.Name, ds.Dir)
	}
}

func dynastyHasDataset(d model.Dynasty) bool {
	for _, ds := range model.Datasets {
		if ds.Dynasty == d {
			return true
		}
	}
	return false
}
