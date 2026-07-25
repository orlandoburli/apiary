//go:build linux

package plugin

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
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

func TestFilesystemSandboxBlocksUndeclaredPaths(t *testing.T) {
	if !probeUsernsSupport() {
		t.Skip("unprivileged user namespaces unavailable; skipping filesystem sandbox test")
	}
	if !probeLandlockSupport() {
		t.Skip("Landlock unavailable on this kernel; filesystem-sandbox enforcement is disabled by design")
	}

	parent := t.TempDir()

	// Place a secret file in a sibling temp dir that the plugin must not reach.
	secretDir := t.TempDir()
	secretFile := filepath.Join(secretDir, "secret.txt")
	if err := os.WriteFile(secretFile, []byte("sensitive"), 0644); err != nil {
		t.Fatal(err)
	}

	// The plugin tries to read the secret file; it should see "blocked" because
	// the Landlock ruleset only allows the declared read path (/usr/bin) and
	// the plugin root — the secret directory is neither.
	script := fmt.Sprintf(`read req
id=$(printf '%%s' "$req" | sed -n 's/.*"request_id":"\([^"]*\)".*/\1/p')
content=$(cat %s 2>/dev/null || echo blocked)
printf '{"protocol":1,"request_id":"%%s","result":{"content":"%%s"}}\n' "$id" "$content"`, secretFile)

	root := writeTestPlugin(t, parent, "fssandbox", script, Manifest{
		// Declare a read path that does NOT include secretDir.
		Security: SecurityRequirements{ReadPaths: []string{"/usr/bin"}},
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
	if result["content"] != "blocked" {
		t.Fatalf("expected Landlock to block access to %s, but plugin read: %q", secretFile, result["content"])
	}
}

func TestFilesystemSandboxAllowsDeclaredReadPath(t *testing.T) {
	if !probeUsernsSupport() {
		t.Skip("unprivileged user namespaces unavailable; skipping filesystem sandbox test")
	}
	if !probeLandlockSupport() {
		t.Skip("Landlock unavailable on this kernel; filesystem-sandbox enforcement is disabled by design")
	}

	parent := t.TempDir()

	// Place a readable file in a directory that the plugin explicitly declares.
	allowedDir := t.TempDir()
	allowedFile := filepath.Join(allowedDir, "allowed.txt")
	if err := os.WriteFile(allowedFile, []byte("readable"), 0644); err != nil {
		t.Fatal(err)
	}

	// Plugin reads the allowed file; the declared read path grants access.
	script := fmt.Sprintf(`read req
id=$(printf '%%s' "$req" | sed -n 's/.*"request_id":"\([^"]*\)".*/\1/p')
content=$(cat %s 2>/dev/null || echo blocked)
printf '{"protocol":1,"request_id":"%%s","result":{"content":"%%s"}}\n' "$id" "$content"`, allowedFile)

	root := writeTestPlugin(t, parent, "fsallowed", script, Manifest{
		Security: SecurityRequirements{ReadPaths: []string{allowedDir}},
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
	if result["content"] != "readable" {
		t.Fatalf("expected Landlock to allow access to declared path %s, got: %q", allowedFile, result["content"])
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
