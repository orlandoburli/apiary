package plugin

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"time"

	pluginsdk "github.com/orlandoburli/apiary/sdk/plugin"
)

const maxProtocolOutput = 4 << 20

type hostRequest struct {
	Protocol   int            `json:"protocol"`
	RequestID  string         `json:"request_id"`
	Capability Capability     `json:"capability"`
	Method     string         `json:"method"`
	Config     map[string]any `json:"config,omitempty"`
	Payload    any            `json:"payload,omitempty"`
}

type hostResponse struct {
	Protocol  int                      `json:"protocol"`
	RequestID string                   `json:"request_id"`
	Result    json.RawMessage          `json:"result,omitempty"`
	Error     *pluginsdk.ResponseError `json:"error,omitempty"`
}

type Client struct {
	installed  *Installed
	instance   InstanceConfig
	timeout    time.Duration
	executable string
}

func NewClient(installed *Installed, instance InstanceConfig) (*Client, error) {
	if installed == nil {
		return nil, fmt.Errorf("installed plugin is required")
	}
	timeout, err := instance.TimeoutDuration()
	if err != nil {
		return nil, err
	}
	executable, err := secureExecutable(installed.Root, installed.Manifest.Executable)
	if err != nil {
		return nil, err
	}
	// Integrity: refuse a plugin whose binary no longer matches its pinned digest.
	if err := verifyChecksum(executable, installed.Manifest.Checksum); err != nil {
		return nil, fmt.Errorf("plugin %q: %w", installed.Manifest.ID, err)
	}
	return &Client{installed: installed, instance: instance, timeout: timeout, executable: executable}, nil
}

func (c *Client) ID() string { return c.installed.Manifest.ID }

func (c *Client) Invoke(ctx context.Context, capability Capability, method string, payload any, result any) error {
	if !c.installed.Manifest.HasCapability(capability) {
		return fmt.Errorf("plugin %q does not declare capability %q", c.ID(), capability)
	}
	if strings.TrimSpace(method) == "" {
		return fmt.Errorf("plugin %q method is required", c.ID())
	}
	requestID := fmt.Sprintf("%d", time.Now().UnixNano())
	request := hostRequest{Protocol: ProtocolVersion, RequestID: requestID, Capability: capability, Method: method, Config: c.instance.Config, Payload: payload}
	raw, err := json.Marshal(request)
	if err != nil {
		return fmt.Errorf("plugin %q encode request: %w", c.ID(), err)
	}
	raw = append(raw, '\n')

	callCtx, cancel := context.WithTimeout(ctx, c.timeout)
	defer cancel()
	// Apply the manifest's declared network policy where the platform can enforce
	// it; applyNetworkIsolation warns (and runs unisolated) rather than failing on
	// hosts without unprivileged netns support.
	launchBin, launchArgs := applyNetworkIsolation(c.ID(), c.executable, c.installed.Manifest.Security.Network)
	command := exec.CommandContext(callCtx, launchBin, launchArgs...)
	command.Dir = c.installed.Root
	command.Env = c.environment()
	command.Stdin = bytes.NewReader(raw)
	stdout := &boundedBuffer{limit: maxProtocolOutput}
	stderr := &boundedBuffer{limit: 64 << 10}
	command.Stdout = stdout
	command.Stderr = stderr
	err = command.Run()
	if errors.Is(callCtx.Err(), context.DeadlineExceeded) {
		return fmt.Errorf("plugin %q timed out after %s while handling %s.%s; process terminated", c.ID(), c.timeout, capability, method)
	}
	if callCtx.Err() != nil {
		return fmt.Errorf("plugin %q invocation canceled while handling %s.%s: %w", c.ID(), capability, method, callCtx.Err())
	}
	if err != nil {
		return fmt.Errorf("plugin %q crashed or exited unsuccessfully while handling %s.%s: %w%s", c.ID(), capability, method, err, stderrSuffix(c.sanitizeDiagnostic(stderr.String())))
	}
	if stdout.truncated {
		return fmt.Errorf("plugin %q response exceeded %d bytes", c.ID(), maxProtocolOutput)
	}
	var response hostResponse
	decoder := json.NewDecoder(bytes.NewReader(stdout.Bytes()))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&response); err != nil {
		return fmt.Errorf("plugin %q returned invalid protocol JSON: %w%s", c.ID(), err, stderrSuffix(c.sanitizeDiagnostic(stderr.String())))
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return fmt.Errorf("plugin %q returned more than one protocol response", c.ID())
	}
	if response.Protocol != ProtocolVersion {
		return fmt.Errorf("plugin %q responded with protocol %d, expected %d", c.ID(), response.Protocol, ProtocolVersion)
	}
	if response.RequestID != requestID {
		return fmt.Errorf("plugin %q response request_id %q does not match %q", c.ID(), response.RequestID, requestID)
	}
	if response.Error != nil {
		if len(response.Result) > 0 || strings.TrimSpace(response.Error.Code) == "" || strings.TrimSpace(response.Error.Message) == "" {
			return fmt.Errorf("plugin %q returned a malformed error response", c.ID())
		}
		return fmt.Errorf("plugin %q error %s: %s", c.ID(), c.sanitizeDiagnostic(response.Error.Code), c.sanitizeDiagnostic(response.Error.Message))
	}
	if result != nil && len(response.Result) > 0 {
		if err := json.Unmarshal(response.Result, result); err != nil {
			return fmt.Errorf("plugin %q result decode: %w", c.ID(), err)
		}
	}
	return nil
}

func (c *Client) sanitizeDiagnostic(value string) string {
	for _, key := range c.installed.Manifest.Security.SecretEnv {
		if secret, ok := os.LookupEnv(key); ok && secret != "" {
			value = strings.ReplaceAll(value, secret, "[REDACTED]")
		}
	}
	return value
}

func (c *Client) environment() []string {
	allowed := []string{"PATH", "HOME", "TMPDIR", "TEMP", "LANG", "LC_ALL", "TZ"}
	allowed = append(allowed, c.installed.Manifest.Security.SecretEnv...)
	env := make([]string, 0, len(allowed)+2)
	seen := map[string]bool{}
	for _, key := range allowed {
		if seen[key] {
			continue
		}
		seen[key] = true
		if value, ok := os.LookupEnv(key); ok {
			env = append(env, key+"="+value)
		}
	}
	env = append(env, "APIARY_PLUGIN_ID="+c.ID(), fmt.Sprintf("APIARY_PLUGIN_PROTOCOL=%d", ProtocolVersion))
	return env
}

type boundedBuffer struct {
	bytes.Buffer
	limit     int
	truncated bool
}

func (b *boundedBuffer) Write(p []byte) (int, error) {
	original := len(p)
	remaining := b.limit - b.Len()
	if remaining <= 0 {
		b.truncated = true
		return original, nil
	}
	if len(p) > remaining {
		b.truncated = true
		p = p[:remaining]
	}
	_, _ = b.Buffer.Write(p)
	return original, nil
}

func stderrSuffix(stderr string) string {
	stderr = strings.TrimSpace(stderr)
	if stderr == "" {
		return ""
	}
	return "; stderr: " + stderr
}
