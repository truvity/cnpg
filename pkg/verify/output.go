package verify

import (
	"encoding/json"
	"fmt"
	"io"
)

// WriteText prints one PASS/FAIL/SKIP line per Result and a summary.
func WriteText(w io.Writer, r Report) {
	_, _ = fmt.Fprintf(w, "== cnpgctl verify: %s/%s\n", r.Namespace, r.Cluster)
	var p, f, s int
	for _, x := range r.Results {
		switch x.Status {
		case Pass:
			p++
		case Fail:
			f++
		case Skip:
			s++
		}
		_, _ = fmt.Fprintf(w, "  %-4s  %s: %s\n", x.Status, x.Name, x.Message)
	}
	_, _ = fmt.Fprintf(w, "== %d passed, %d failed, %d skipped\n", p, f, s)
}

// WriteJSON prints the Report as indented JSON.
func WriteJSON(w io.Writer, r Report) error {
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	return enc.Encode(r)
}
