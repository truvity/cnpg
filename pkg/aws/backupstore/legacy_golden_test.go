package backupstore_test

import (
	"encoding/json"
	"flag"
	"os"
	"path/filepath"
	"sort"
	"testing"

	"github.com/truvity/cnpg/v2/pkg/aws/backupstore"
)

var update = flag.Bool("update", false, "rewrite the golden files")

// dump renders every registered child (type, name, inputs) as stable JSON.
func dump(t *testing.T, regs []registration) []byte {
	t.Helper()

	type entry struct {
		Type   string         `json:"type"`
		Name   string         `json:"name"`
		Parent string         `json:"parent,omitempty"`
		Inputs map[string]any `json:"inputs"`
	}

	var out []entry

	for _, r := range children(regs) {
		out = append(out, entry{Type: r.typ, Name: r.name, Parent: r.parent, Inputs: r.inputs.Mappable()})
	}

	sort.Slice(out, func(i, j int) bool {
		if out[i].Type != out[j].Type {
			return out[i].Type < out[j].Type
		}

		return out[i].Name < out[j].Name
	})

	raw, err := json.MarshalIndent(out, "", "  ")
	if err != nil {
		t.Fatal(err)
	}

	return append(raw, '\n')
}

func checkGolden(t *testing.T, file string, got []byte) {
	t.Helper()

	path := filepath.Join("testdata", file)

	if *update {
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}

		if err := os.WriteFile(path, got, 0o644); err != nil {
			t.Fatal(err)
		}
	}

	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}

	if string(want) != string(got) {
		t.Errorf("%s differs from the golden file (run with -update to rewrite it after review):\n%s", file, got)
	}
}

// The legacy goldens were captured before the tenant model existed. A caller
// that sets none of the new fields must see exactly these resources.
func TestLegacyResourcesAreByteIdentical(t *testing.T) {
	for name, mk := range map[string]func() *backupstore.Args{"minimal": base, "full": full} {
		regs, err := run(t, mk())
		if err != nil {
			t.Fatal(err)
		}

		checkGolden(t, "legacy/"+name+".json", dump(t, regs))
	}
}
