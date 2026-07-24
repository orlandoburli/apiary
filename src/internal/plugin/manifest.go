// Package plugin implements Apiary's versioned out-of-process extension protocol.
package plugin

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"

	"github.com/Masterminds/semver/v3"
	pluginsdk "github.com/orlandoburli/apiary/sdk/plugin"
)

const (
	ManifestFilename      = "apiary-plugin.json"
	ManifestSchemaVersion = 1
	ProtocolVersion       = pluginsdk.ProtocolVersion
)

type Capability = pluginsdk.Capability

const (
	CapabilitySource           = pluginsdk.CapabilitySource
	CapabilityRunner           = pluginsdk.CapabilityRunner
	CapabilityWorkflowAction   = pluginsdk.CapabilityWorkflowAction
	CapabilityApprovalProvider = pluginsdk.CapabilityApprovalProvider
	CapabilitySecretProvider   = pluginsdk.CapabilitySecretProvider
	CapabilityEventExporter    = pluginsdk.CapabilityEventExporter
)

var supportedCapabilities = map[Capability]struct{}{
	CapabilitySource: {}, CapabilityRunner: {}, CapabilityWorkflowAction: {},
	CapabilityApprovalProvider: {}, CapabilitySecretProvider: {}, CapabilityEventExporter: {},
}

type SecurityRequirements struct {
	Network    bool     `json:"network,omitempty"`
	ReadPaths  []string `json:"read_paths,omitempty"`
	WritePaths []string `json:"write_paths,omitempty"`
	SecretEnv  []string `json:"secret_env,omitempty"`
}

type Manifest struct {
	SchemaVersion int                  `json:"schema_version"`
	ID            string               `json:"id"`
	Version       string               `json:"version"`
	Apiary        string               `json:"apiary"`
	Protocol      int                  `json:"protocol"`
	Executable    string               `json:"executable"`
	Capabilities  []Capability         `json:"capabilities"`
	ConfigSchema  json.RawMessage      `json:"config_schema,omitempty"`
	Security      SecurityRequirements `json:"security,omitempty"`
	// Checksum is the lowercase hex SHA-256 of the executable file.
	// It is mandatory; plugins without a checksum are refused at load time.
	Checksum string `json:"checksum"`
}

type Installed struct {
	Manifest Manifest `json:"manifest"`
	Root     string   `json:"root"`
	Path     string   `json:"path"`
}

func Load(root, apiaryVersion string) (*Installed, error) {
	root, err := filepath.Abs(root)
	if err != nil {
		return nil, fmt.Errorf("plugin directory %q: %w", root, err)
	}
	manifestPath := filepath.Join(root, ManifestFilename)
	raw, err := os.ReadFile(manifestPath)
	if err != nil {
		return nil, fmt.Errorf("read plugin manifest %q: %w", manifestPath, err)
	}
	var manifest Manifest
	dec := json.NewDecoder(strings.NewReader(string(raw)))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&manifest); err != nil {
		return nil, fmt.Errorf("decode plugin manifest %q: %w", manifestPath, err)
	}
	installed := &Installed{Manifest: manifest, Root: root, Path: manifestPath}
	if err := installed.Validate(apiaryVersion); err != nil {
		return nil, fmt.Errorf("plugin manifest %q: %w", manifestPath, err)
	}
	return installed, nil
}

func (p *Installed) Validate(apiaryVersion string) error {
	m := p.Manifest
	if m.SchemaVersion != ManifestSchemaVersion {
		return fmt.Errorf("unsupported schema_version %d; this Apiary supports %d", m.SchemaVersion, ManifestSchemaVersion)
	}
	if !validPluginID(m.ID) {
		return fmt.Errorf("id %q must be a lowercase reverse-DNS identifier", m.ID)
	}
	if _, err := semver.StrictNewVersion(m.Version); err != nil {
		return fmt.Errorf("version %q is not semantic versioning: %w", m.Version, err)
	}
	if strings.TrimSpace(m.Apiary) == "" {
		return fmt.Errorf("apiary compatibility constraint is required")
	}
	constraint, err := semver.NewConstraint(m.Apiary)
	if err != nil {
		return fmt.Errorf("apiary compatibility %q is invalid: %w", m.Apiary, err)
	}
	if apiaryVersion != "" && apiaryVersion != "dev" {
		v, err := semver.NewVersion(apiaryVersion)
		if err != nil {
			return fmt.Errorf("host Apiary version %q cannot be checked: %w", apiaryVersion, err)
		}
		if !constraint.Check(v) {
			return fmt.Errorf("requires Apiary %s, but host is %s; install a compatible plugin release", m.Apiary, v)
		}
	}
	if m.Protocol != ProtocolVersion {
		return fmt.Errorf("unsupported protocol %d; this Apiary supports protocol %d", m.Protocol, ProtocolVersion)
	}
	if len(m.Capabilities) == 0 {
		return fmt.Errorf("at least one capability is required")
	}
	seen := map[Capability]bool{}
	for _, capability := range m.Capabilities {
		if _, ok := supportedCapabilities[capability]; !ok {
			return fmt.Errorf("unsupported capability %q; supported: %s", capability, strings.Join(SupportedCapabilityNames(), ", "))
		}
		if seen[capability] {
			return fmt.Errorf("duplicate capability %q", capability)
		}
		seen[capability] = true
	}
	if len(m.ConfigSchema) > 0 {
		if err := ValidateSchema(m.ConfigSchema); err != nil {
			return fmt.Errorf("config_schema: %w", err)
		}
	}
	if err := validateSecurity(m.Security); err != nil {
		return err
	}
	execPath, err := secureExecutable(p.Root, m.Executable)
	if err != nil {
		return err
	}
	if err := verifyChecksum(execPath, m.Checksum); err != nil {
		return err
	}
	p.Manifest = m
	p.Path = filepath.Join(p.Root, ManifestFilename)
	p.Root, _ = filepath.Abs(p.Root)
	return nil
}

func (m Manifest) HasCapability(capability Capability) bool {
	for _, current := range m.Capabilities {
		if current == capability {
			return true
		}
	}
	return false
}

func SupportedCapabilityNames() []string {
	names := make([]string, 0, len(supportedCapabilities))
	for capability := range supportedCapabilities {
		names = append(names, string(capability))
	}
	sort.Strings(names)
	return names
}

func validPluginID(id string) bool {
	parts := strings.Split(id, ".")
	if len(parts) < 3 {
		return false
	}
	for _, part := range parts {
		if part == "" || part[0] < 'a' || part[0] > 'z' {
			return false
		}
		for _, r := range part {
			if (r < 'a' || r > 'z') && (r < '0' || r > '9') && r != '-' {
				return false
			}
		}
	}
	return true
}

func secureExecutable(root, name string) (string, error) {
	if strings.TrimSpace(name) == "" || filepath.IsAbs(name) {
		return "", fmt.Errorf("executable must be a relative path inside the plugin directory")
	}
	root, _ = filepath.Abs(root)
	path := filepath.Clean(filepath.Join(root, name))
	rel, err := filepath.Rel(root, path)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("executable %q escapes the plugin directory", name)
	}
	info, err := os.Lstat(path)
	if err != nil {
		return "", fmt.Errorf("executable %q is not installed: %w", name, err)
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return "", fmt.Errorf("executable %q must not be a symlink", name)
	}
	if !info.Mode().IsRegular() {
		return "", fmt.Errorf("executable %q is not a regular file", name)
	}
	if runtime.GOOS != "windows" && info.Mode().Perm()&0111 == 0 {
		return "", fmt.Errorf("executable %q is not executable; run chmod +x", name)
	}
	return path, nil
}

func validateSecurity(security SecurityRequirements) error {
	seen := map[string]bool{}
	for _, name := range security.SecretEnv {
		if name == "" || strings.Trim(name, "ABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789_") != "" {
			return fmt.Errorf("security.secret_env %q is not a valid uppercase environment variable", name)
		}
		if seen[name] {
			return fmt.Errorf("security.secret_env contains duplicate %q", name)
		}
		seen[name] = true
	}
	for _, entry := range []struct {
		paths []string
		field string
	}{
		{security.ReadPaths, "read_paths"},
		{security.WritePaths, "write_paths"},
	} {
		for _, p := range entry.paths {
			if p == "" {
				return fmt.Errorf("security.%s must not contain empty entries", entry.field)
			}
			clean := filepath.Clean(p)
			if clean == ".." || strings.HasPrefix(clean, ".."+string(filepath.Separator)) {
				return fmt.Errorf("security.%s %q must not escape the plugin directory", entry.field, p)
			}
		}
	}
	return nil
}

// verifyChecksum computes the SHA-256 of execPath and confirms it matches the hex digest
// declared in the manifest. An empty expected value is rejected so unsigned plugins cannot load.
func verifyChecksum(execPath, expected string) error {
	if expected == "" {
		return fmt.Errorf("executable checksum is required; add a \"checksum\" field (sha256 hex) to the manifest")
	}
	f, err := os.Open(execPath)
	if err != nil {
		return fmt.Errorf("open executable for checksum verification: %w", err)
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return fmt.Errorf("compute executable checksum: %w", err)
	}
	actual := hex.EncodeToString(h.Sum(nil))
	if actual != expected {
		return fmt.Errorf("executable checksum mismatch: manifest declares %s but file is %s; the binary may have been replaced", expected, actual)
	}
	return nil
}

// ComputeChecksum returns the lowercase hex SHA-256 digest of the file at path.
// Plugin authors call this when adding the checksum field to their manifest.
func ComputeChecksum(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}
