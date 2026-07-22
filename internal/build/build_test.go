package build

import "testing"

func TestResolveVersionUsesModuleVersionForGoInstall(t *testing.T) {
	tests := []struct {
		injected string
		module   string
		want     string
	}{
		{injected: "v1.2.3", module: "v1.2.2", want: "v1.2.3"},
		{injected: "dev", module: "v1.2.3", want: "v1.2.3"},
		{injected: "dev", module: "(devel)", want: "dev"},
		{injected: "dev", module: "", want: "dev"},
	}
	for _, tt := range tests {
		if got := resolveVersion(tt.injected, tt.module); got != tt.want {
			t.Fatalf("resolveVersion(%q, %q) = %q, want %q", tt.injected, tt.module, got, tt.want)
		}
	}
}
