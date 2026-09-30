package verify

import (
	"fmt"
	"strings"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

type hbaLine struct {
	n      int // 1-based position in the list
	fields []string
}

func parseLines(raw []string) []hbaLine {
	var out []hbaLine
	for i, l := range raw {
		if h := strings.Index(l, "#"); h >= 0 {
			l = l[:h]
		}
		if f := strings.Fields(l); len(f) > 0 {
			out = append(out, hbaLine{n: i + 1, fields: f})
		}
	}
	return out
}

// isCatchAll: every database, every user, every address.
func (l hbaLine) isCatchAll() bool {
	f := l.fields
	if len(f) < 4 || f[0] == "local" {
		return false
	}
	switch strings.ToLower(f[3]) {
	case "all", "0.0.0.0/0", "::/0":
		return strings.EqualFold(f[1], "all") && strings.EqualFold(f[2], "all")
	}
	return false
}

func stringList(c *unstructured.Unstructured, path ...string) []string {
	v, _, _ := unstructured.NestedStringSlice(c.Object, path...)
	return v
}

// checkHBA reads spec.postgresql.pg_hba and pg_ident. The operator
// appends its own lines after the spec's, so an empty list is fine.
func checkHBA(c *unstructured.Unstructured) []Result {
	hba := parseLines(stringList(c, "spec", "postgresql", "pg_hba"))
	ident := parseLines(stringList(c, "spec", "postgresql", "pg_ident"))

	var trust []string
	maps := map[string][]int{}
	for _, l := range hba {
		for _, tok := range l.fields[1:] {
			if strings.EqualFold(tok, "trust") {
				trust = append(trust, fmt.Sprintf("#%d", l.n))
			}
			if m, ok := strings.CutPrefix(strings.Trim(tok, `"`), "map="); ok {
				maps[m] = append(maps[m], l.n)
			}
		}
	}
	out := []Result{res("pg_hba has no trust", len(trust) == 0,
		fmt.Sprintf("%d pg_hba lines, none use trust", len(hba)),
		fmt.Sprintf("pg_hba line(s) %s use the trust method", strings.Join(trust, ", ")))}

	have := map[string]int{}
	for _, l := range ident {
		have[l.fields[0]]++
	}
	var missing []string
	for m, lines := range maps {
		if have[m] == 0 {
			missing = append(missing, fmt.Sprintf("%q (pg_hba line %d)", m, lines[0]))
		}
	}
	out = append(out, res("pg_hba maps have pg_ident rows", len(missing) == 0,
		fmt.Sprintf("%d map(s) referenced, each has rows", len(maps)),
		"no pg_ident rows for map "+strings.Join(missing, ", ")))

	catchAll := -1
	last := 0
	for i, l := range hba {
		if l.isCatchAll() && catchAll < 0 {
			catchAll = i
		}
		last = i
	}
	switch {
	case catchAll < 0:
		out = append(out, Result{Name: "pg_hba catch-all is last", Status: Pass, Message: "no catch-all in the spec; the operator's own follows the spec's lines"})
	case catchAll == last:
		out = append(out, Result{Name: "pg_hba catch-all is last", Status: Pass, Message: fmt.Sprintf("catch-all is line %d, the last", hba[catchAll].n)})
	default:
		out = append(out, Result{Name: "pg_hba catch-all is last", Status: Fail,
			Message: fmt.Sprintf("catch-all is line %d but %d line(s) follow it and can never match", hba[catchAll].n, last-catchAll)})
	}
	return out
}
