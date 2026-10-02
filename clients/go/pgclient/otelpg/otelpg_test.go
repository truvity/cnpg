package otelpg

import "testing"

func TestOperationIsABoundedKeyword(t *testing.T) {
	for in, want := range map[string]string{
		"select 1":                       "SELECT",
		"  INSERT INTO t VALUES ($1)":    "INSERT",
		"\n(select 1) union (select 2)":  "SELECT",
		"with x as (select 1) select *":  "WITH",
		"":                               "QUERY",
		"$$weird$$":                      "QUERY",
		"averyveryverylongkeywordindeed": "QUERY",
	} {
		if got := operation(in); got != want {
			t.Errorf("operation(%q) = %q, want %q", in, got, want)
		}
	}
}
