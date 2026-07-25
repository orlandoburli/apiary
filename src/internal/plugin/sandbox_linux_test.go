//go:build linux

package plugin

import (
	"context"
	"testing"
)

func TestProbeUsernsSupportIsSafe(t *testing.T) {
	// Verify the probe doesn't panic regardless of kernel configuration.
	supported := probeUsernsSupport()
	t.Logf("unprivileged user namespaces supported: %v", supported)
}

func TestNetworkSandboxIsolatesPlugin(t *testing.T) {
	if !probeUsernsSupport() {
		t.Skip("unprivileged user namespaces unavailable on this kernel; network-namespace isolation is disabled by design")
	}

	parent := t.TempDir()
	// The plugin counts non-loopback interfaces visible to it.
	// Inside a network namespace with no external interfaces, only "lo" exists.
	script := `read req
id=$(printf '%s' "$req" | sed -n 's/.*"request_id":"\([^"]*\)".*/\1/p')
ifaces=$(cat /proc/net/dev | tail -n +3 | awk -F: '{print $1}' | tr -d ' ' | grep -v '^lo$' | wc -l | tr -d ' ')
printf '{"protocol":1,"request_id":"%s","result":{"ifaces":"%s"}}\n' "$id" "$ifaces"`

	root := writeTestPlugin(t, parent, "netcheck", script, Manifest{
		Security: SecurityRequirements{Network: false},
	})
	installed, err := Load(root, "v0.10.0")
	if err != nil {
		t.Fatal(err)
	}
	client, err := NewClient(installed, InstanceConfig{ID: installed.Manifest.ID})
	if err != nil {
		t.Fatal(err)
	}
	var result map[string]string
	if err := client.Invoke(context.Background(), CapabilityEventExporter, "export", nil, &result); err != nil {
		t.Fatal(err)
	}
	if result["ifaces"] != "0" {
		t.Fatalf("expected 0 non-loopback interfaces inside sandbox, got %q", result["ifaces"])
	}
}

func TestNetworkSandboxAllowedWhenDeclared(t *testing.T) {
	// When network: true the plugin runs without namespace isolation.
	// We simply check that invocation succeeds.
	parent := t.TempDir()
	script := `read req
id=$(printf '%s' "$req" | sed -n 's/.*"request_id":"\([^"]*\)".*/\1/p')
printf '{"protocol":1,"request_id":"%s","result":{}}\n' "$id"`

	root := writeTestPlugin(t, parent, "netallowed", script, Manifest{
		Security: SecurityRequirements{Network: true},
	})
	installed, err := Load(root, "v0.10.0")
	if err != nil {
		t.Fatal(err)
	}
	client, err := NewClient(installed, InstanceConfig{ID: installed.Manifest.ID})
	if err != nil {
		t.Fatal(err)
	}
	if err := client.Invoke(context.Background(), CapabilityEventExporter, "export", nil, nil); err != nil {
		t.Fatalf("network:true plugin should invoke successfully: %v", err)
	}
}
