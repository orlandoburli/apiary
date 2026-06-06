package config

import "testing"

// TestExpandEnv covers the env-var expansion done before YAML parsing. The key
// invariant — beyond expanding genuine ${VAR}/$VAR references — is that workflow
// expression delimiters `${{ … }}` survive untouched, since os.Expand would
// otherwise mangle them into invalid YAML before the parser ever sees them.
func TestExpandEnv(t *testing.T) {
	t.Setenv("APIARY_TEST_TOKEN", "secret123")

	cases := []struct {
		name string
		in   string
		want string
	}{
		{"braced ref", "key: ${APIARY_TEST_TOKEN}", "key: secret123"},
		{"bare ref", "key: $APIARY_TEST_TOKEN", "key: secret123"},
		{"spaced braced ref", "key: ${ APIARY_TEST_TOKEN }", "key: secret123"},
		{"unset ref expands empty", "key: ${APIARY_TEST_UNSET}", "key: "},
		{
			"expression delimiter preserved",
			`if: ${{ memory.complexity == "high" }}`,
			`if: ${{ memory.complexity == "high" }}`,
		},
		{
			"expression with simple ident preserved",
			`if: ${{ cell.priority == "high" }}`,
			`if: ${{ cell.priority == "high" }}`,
		},
		{
			"env ref and expression coexist in one string",
			`api_key: ${APIARY_TEST_TOKEN}` + "\n" + `if: ${{ memory.x == "y" }}`,
			`api_key: secret123` + "\n" + `if: ${{ memory.x == "y" }}`,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := expandEnv(tc.in); got != tc.want {
				t.Errorf("expandEnv(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}
