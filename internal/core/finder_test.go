package core

import (
	"reflect"
	"testing"
)

func TestFinderArgsRevealsRatherThanOpens(t *testing.T) {
	got := finderArgs("/Users/x/.kraken/cells/a-1/artifacts/00-overview.md")
	want := []string{"/usr/bin/open", "-R", "/Users/x/.kraken/cells/a-1/artifacts/00-overview.md"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("finderArgs = %v, want %v", got, want)
	}
}
