package text

import (
	"reflect"
	"testing"
)

func TestTerms(t *testing.T) {
	cases := map[string][]string{
		"Fix the failing login test": {"fix", "failing", "login", "test"},
		"部署 to prod, prod":           {"部署", "prod"},
		"a an of it":                 nil,
		"tests: go test ./... tests": {"tests", "test"},
	}
	for in, want := range cases {
		if got := Terms(in); !reflect.DeepEqual(got, want) {
			t.Errorf("Terms(%q) = %v, want %v", in, got, want)
		}
	}
}
