package execution

import (
	"strings"
	"testing"
)

func TestResolvePermissionArgs(t *testing.T) {
	const (
		permFlag  = "--permission-mode"
		toolsFlag = "--allowedTools"
	)
	bypass := []string{"--dangerously-skip-permissions"}

	tests := []struct {
		name         string
		mode         string
		allowedTools []string
		permFlag     string
		bypassArgs   []string
		toolsFlag    string
		want         []string
		wantErr      string
	}{
		{name: "bypass", mode: "bypass", permFlag: permFlag, bypassArgs: bypass, toolsFlag: toolsFlag,
			want: []string{"--dangerously-skip-permissions"}},
		{name: "bypassPermissions alias", mode: "bypassPermissions", permFlag: permFlag, bypassArgs: bypass, toolsFlag: toolsFlag,
			want: []string{"--dangerously-skip-permissions"}},
		{name: "default emits nothing", mode: "default", permFlag: permFlag, bypassArgs: bypass, toolsFlag: toolsFlag,
			want: nil},
		// Unknown modes pass through so new upstream modes need no code change.
		{name: "named mode passthrough", mode: "acceptEdits", permFlag: permFlag, bypassArgs: bypass, toolsFlag: toolsFlag,
			want: []string{"--permission-mode", "acceptEdits"}},
		{name: "allowed tools joined", mode: "default", allowedTools: []string{"Bash", "Read"}, permFlag: permFlag, bypassArgs: bypass, toolsFlag: toolsFlag,
			want: []string{"--allowedTools", "Bash,Read"}},
		{name: "allowed tools alongside mode", mode: "plan", allowedTools: []string{"Read"}, permFlag: permFlag, bypassArgs: bypass, toolsFlag: toolsFlag,
			want: []string{"--permission-mode", "plan", "--allowedTools", "Read"}},
		{name: "bypass unsupported", mode: "bypass", wantErr: "no permission_bypass_args"},
		{name: "named mode unsupported", mode: "acceptEdits", bypassArgs: bypass, wantErr: "no permission_flag"},
		{name: "allowed tools unsupported", mode: "default", allowedTools: []string{"Bash"}, wantErr: "no allowed_tools_flag"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := resolvePermissionArgs(tc.mode, tc.allowedTools, tc.permFlag, tc.bypassArgs, tc.toolsFlag)
			if tc.wantErr != "" {
				if err == nil {
					t.Fatalf("expected error containing %q, got args %v", tc.wantErr, got)
				}
				if !strings.Contains(err.Error(), tc.wantErr) {
					t.Fatalf("error %q does not mention %q", err, tc.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if strings.Join(got, " ") != strings.Join(tc.want, " ") {
				t.Errorf("got %v, want %v", got, tc.want)
			}
		})
	}
}
