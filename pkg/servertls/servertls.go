// Package servertls validates the per-environment list a catalogue keeps of
// where a project's CloudNativePG server takes its certificate from a
// workload issuer.
//
// The list acts on the project's primary install in each environment: the
// install passes the server certificate to the project's database chart, and
// that Application exists only where the project has a primary endpoint. A
// listed environment with no primary install would change nothing and read as
// done, and a project without the postgres capability has no CNPG server to
// give a certificate to. Both are refused rather than ignored, and so are
// unknown environments and duplicates.
package servertls

import (
	"fmt"
	"slices"
)

// Project is what the check needs to know about one project.
type Project struct {
	// Name is the project's name, for the error text.
	Name string
	// Postgres reports whether the project has the postgres capability.
	Postgres bool
	// Primary lists the environments where the project has a primary install.
	Primary []string
}

// Validate checks the environments listed for the project: the project has the
// postgres capability, and each environment is known, listed once, and one
// where the project has a primary install. field names the catalogue key for
// the error text; an empty list is always valid.
func Validate(known []string, p Project, field string, listed []string) error {
	if len(listed) == 0 {
		return nil
	}

	if !p.Postgres {
		return fmt.Errorf("%q: %s needs capabilities.postgres: the project has no CNPG server", p.Name, field)
	}

	seen := make(map[string]bool, len(listed))

	for _, env := range listed {
		if !slices.Contains(known, env) {
			return fmt.Errorf("%q: %s names unknown environment %q (known: %v)", p.Name, field, env, known)
		}

		if seen[env] {
			return fmt.Errorf("%q: %s lists %q twice", p.Name, field, env)
		}

		seen[env] = true

		if !slices.Contains(p.Primary, env) {
			return fmt.Errorf("%q: %s lists %q, where the project has no primary install to give a certificate to", p.Name, field, env)
		}
	}

	return nil
}
