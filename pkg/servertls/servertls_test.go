package servertls_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/truvity/cnpg/v2/pkg/servertls"
)

func TestValidate(t *testing.T) {
	known := []string{"devel", "stage", "prod"}
	p := servertls.Project{Name: "app", Postgres: true, Primary: []string{"devel", "prod"}}

	require.NoError(t, servertls.Validate(known, p, "server_tls", nil))
	require.NoError(t, servertls.Validate(known, p, "server_tls", []string{"devel", "prod"}))

	// An empty list needs no capability.
	require.NoError(t, servertls.Validate(known, servertls.Project{Name: "x"}, "server_tls", nil))

	cases := []struct {
		name   string
		p      servertls.Project
		listed []string
		want   string
	}{
		{"no capability", servertls.Project{Name: "x", Primary: []string{"devel"}}, []string{"devel"}, "needs capabilities.postgres"},
		{"unknown environment", p, []string{"nowhere"}, "unknown environment"},
		{"twice", p, []string{"devel", "devel"}, "twice"},
		{"no primary", p, []string{"stage"}, "no primary install"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			assert.ErrorContains(t, servertls.Validate(known, c.p, "server_tls", c.listed), c.want)
		})
	}
}
