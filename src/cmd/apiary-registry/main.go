// Command apiary-registry validates plugin registry entries and compiles them
// into the index clients fetch. It is repository tooling, run by CI and by
// `make registry-check` / `make registry-build` — it is not shipped in the
// apiary binary and never runs on an operator's machine.
//
// The checker is what makes a listing worth anything: it downloads each declared
// artifact, re-derives both digests, cross-checks the manifest inside the
// archive against the metadata the entry claims, and optionally runs the
// protocol conformance kit. An entry cannot assert a digest, a compatibility
// range, or a conformance verdict that CI could not reproduce.
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/orlandoburli/apiary/internal/plugin"
)

const (
	maxArtifactBytes = 256 << 20
	downloadTimeout  = 5 * time.Minute
)

func main() {
	if len(os.Args) < 2 {
		usage()
		os.Exit(2)
	}
	var err error
	switch os.Args[1] {
	case "check":
		err = runCheck(os.Args[2:])
	case "build":
		err = runBuild(os.Args[2:])
	default:
		usage()
		os.Exit(2)
	}
	if err != nil {
		fmt.Fprintf(os.Stderr, "apiary-registry: %v\n", err)
		os.Exit(1)
	}
}

func usage() {
	fmt.Fprint(os.Stderr, `usage:
  apiary-registry check [--dir registry] [--offline] [--conformance-runner path] [--results file]
  apiary-registry build [--dir registry] [--results file] --out path
`)
}

// runCheck validates every entry and, unless --offline, every artifact it
// declares.
func runCheck(args []string) error {
	flags := flag.NewFlagSet("check", flag.ExitOnError)
	dir := flags.String("dir", "registry", "registry directory")
	offline := flags.Bool("offline", false, "validate entry metadata only; do not download artifacts")
	runner := flags.String("conformance-runner", "", "path to sdk/conformance/run.py; enables the conformance kit")
	results := flags.String("results", "", "write conformance verdicts to this JSON file")
	strictConformance := flags.Bool("strict-conformance", false, "treat a conformance failure as a check failure")
	if err := flags.Parse(args); err != nil {
		return err
	}
	entries, err := plugin.LoadEntries(filepath.Join(*dir, "plugins"))
	if err != nil {
		return err
	}
	if len(entries) == 0 {
		fmt.Println("no entries to check")
		return nil
	}
	verdicts := plugin.ConformanceResults{}
	var failures []string
	for _, entry := range entries {
		fmt.Printf("== %s (%d release(s))\n", entry.ID, len(entry.Releases))
		if *offline {
			fmt.Printf("   metadata ok (offline: artifacts not verified)\n")
			continue
		}
		for i := range entry.Releases {
			release := &entry.Releases[i]
			verdict, errs := checkRelease(entry, release, *runner, *strictConformance)
			if verdict != nil {
				verdicts[plugin.ConformanceKey(entry.ID, release.Version)] = *verdict
			}
			for _, err := range errs {
				failures = append(failures, fmt.Sprintf("%s %s: %v", entry.ID, release.Version, err))
				fmt.Printf("   ✗ %s: %v\n", release.Version, err)
			}
		}
	}
	if *results != "" {
		if err := writeJSON(*results, verdicts); err != nil {
			return err
		}
		fmt.Printf("→ conformance verdicts written to %s\n", *results)
	}
	if len(failures) > 0 {
		return fmt.Errorf("%d check(s) failed:\n  - %s", len(failures), strings.Join(failures, "\n  - "))
	}
	fmt.Println("✓ registry entries verified")
	return nil
}

// checkRelease verifies every artifact of one release and returns the
// conformance verdict for the platform this checker runs on (the only artifact
// it can actually execute).
func checkRelease(entry *plugin.Entry, release *plugin.Release, conformanceRunner string, strict bool) (*plugin.Conformance, []error) {
	var errs []error
	var verdict *plugin.Conformance
	for i := range release.Artifacts {
		artifact := &release.Artifacts[i]
		work, err := os.MkdirTemp("", "apiary-registry-*")
		if err != nil {
			return verdict, append(errs, err)
		}
		installed, err := verifyArtifact(entry, release, artifact, work)
		if err != nil {
			errs = append(errs, fmt.Errorf("%s: %w", artifact.Platform(), err))
			_ = os.RemoveAll(work)
			continue
		}
		fmt.Printf("   ✓ %s %s verified (digests, manifest)\n", release.Version, artifact.Platform())

		if conformanceRunner != "" && artifact.OS == runtime.GOOS && artifact.Arch == runtime.GOARCH && len(entry.ConformanceConfig) > 0 {
			status := "passed"
			if err := runConformance(conformanceRunner, entry, installed); err != nil {
				status = "failed"
				// A failing kit run is recorded and published, not suppressed:
				// the registry describes plugins, it does not certify them, and
				// an operator is better served by "conformance FAILED" in
				// `plugins info` than by a listing that quietly never appears.
				if strict {
					errs = append(errs, fmt.Errorf("conformance: %w", err))
				}
			}
			verdict = &plugin.Conformance{
				Status:    status,
				Kit:       "sdk/conformance",
				CheckedAt: time.Now().UTC().Format(time.RFC3339),
				Commit:    os.Getenv("GITHUB_SHA"),
			}
			fmt.Printf("   %s %s conformance %s\n", tick(status == "passed"), release.Version, status)
		}
		_ = os.RemoveAll(work)
	}
	return verdict, errs
}

// verifyArtifact is the checker's core: download, digest, unpack, and confirm
// that the manifest inside the archive says what the entry says it says.
func verifyArtifact(entry *plugin.Entry, release *plugin.Release, artifact *plugin.Artifact, work string) (*plugin.Installed, error) {
	archive := filepath.Join(work, filepath.Base(artifact.URL))
	if err := download(artifact.URL, archive); err != nil {
		return nil, err
	}
	digest, err := plugin.FileSHA256(archive)
	if err != nil {
		return nil, err
	}
	if !plugin.DigestsMatch(digest, artifact.ArchiveSHA256) {
		return nil, fmt.Errorf("archive_sha256 mismatch: entry declares %s, download is %s", artifact.ArchiveSHA256, digest)
	}
	unpacked := filepath.Join(work, "unpacked")
	if err := os.MkdirAll(unpacked, 0o755); err != nil {
		return nil, err
	}
	root, err := plugin.ExtractArchive(archive, unpacked)
	if err != nil {
		return nil, err
	}
	// An empty host version skips the compatibility check: CI validates the
	// manifest's shape, not whether the runner happens to be a matching Apiary.
	installed, err := plugin.Load(root, "")
	if err != nil {
		return nil, err
	}
	manifest := installed.Manifest
	if manifest.ID != entry.ID {
		return nil, fmt.Errorf("manifest id %q does not match the entry id %q", manifest.ID, entry.ID)
	}
	if manifest.Version != release.Version {
		return nil, fmt.Errorf("manifest version %q does not match release %q", manifest.Version, release.Version)
	}
	if manifest.Protocol != release.Protocol {
		return nil, fmt.Errorf("manifest protocol %d does not match release protocol %d", manifest.Protocol, release.Protocol)
	}
	if manifest.Apiary != release.Apiary {
		return nil, fmt.Errorf("manifest apiary constraint %q does not match release %q", manifest.Apiary, release.Apiary)
	}
	if err := sameCapabilities(entry.Capabilities, manifest.Capabilities); err != nil {
		return nil, err
	}
	executable := filepath.Join(root, manifest.Executable)
	executableDigest, err := plugin.FileSHA256(executable)
	if err != nil {
		return nil, err
	}
	if !plugin.DigestsMatch(executableDigest, artifact.ExecutableSHA256) {
		return nil, fmt.Errorf("executable_sha256 mismatch: entry declares %s, archive contains %s", artifact.ExecutableSHA256, executableDigest)
	}
	if manifest.Checksum != "" && !plugin.DigestsMatch(manifest.Checksum, executableDigest) {
		return nil, fmt.Errorf("the manifest pins checksum %s, which is not the executable it ships", manifest.Checksum)
	}
	return installed, nil
}

func sameCapabilities(declared, manifest []plugin.Capability) error {
	want := map[plugin.Capability]bool{}
	for _, capability := range declared {
		want[capability] = true
	}
	for _, capability := range manifest {
		if !want[capability] {
			return fmt.Errorf("manifest declares capability %q, which the entry does not list", capability)
		}
		delete(want, capability)
	}
	for capability := range want {
		return fmt.Errorf("entry lists capability %q, which the manifest does not declare", capability)
	}
	return nil
}

// runConformance runs the protocol conformance kit against the published
// executable. The verdict goes into the index as a test result — never as an
// endorsement of what the plugin does with the access it declares.
func runConformance(runnerPath string, entry *plugin.Entry, installed *plugin.Installed) error {
	config, err := entry.ConformanceConfigJSON()
	if err != nil {
		return err
	}
	executable := filepath.Join(installed.Root, installed.Manifest.Executable)
	command := exec.Command("python3", runnerPath, "--name", entry.ID, "--config", config, "--", executable)
	command.Stdout = os.Stdout
	command.Stderr = os.Stderr
	return command.Run()
}

func download(url, target string) error {
	client := &http.Client{Timeout: downloadTimeout}
	response, err := client.Get(url)
	if err != nil {
		return fmt.Errorf("download %s: %w", url, err)
	}
	defer func() { _ = response.Body.Close() }()
	if response.StatusCode != http.StatusOK {
		return fmt.Errorf("download %s: unexpected status %s", url, response.Status)
	}
	file, err := os.Create(target)
	if err != nil {
		return err
	}
	written, err := io.Copy(file, io.LimitReader(response.Body, maxArtifactBytes+1))
	if closeErr := file.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		return err
	}
	if written > maxArtifactBytes {
		return fmt.Errorf("download %s: larger than %d bytes; refusing it", url, int64(maxArtifactBytes))
	}
	return nil
}

// runBuild compiles the reviewed entries into the published index.
func runBuild(args []string) error {
	flags := flag.NewFlagSet("build", flag.ExitOnError)
	dir := flags.String("dir", "registry", "registry directory")
	results := flags.String("results", "", "conformance verdicts produced by `check --results`")
	out := flags.String("out", "", "path to write the compiled index to")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if *out == "" {
		return fmt.Errorf("--out is required")
	}
	entries, err := plugin.LoadEntries(filepath.Join(*dir, "plugins"))
	if err != nil {
		return err
	}
	verdicts := plugin.ConformanceResults{}
	if *results != "" {
		raw, err := os.ReadFile(*results)
		if os.IsNotExist(err) {
			// Building without a checker run is legitimate (a docs deploy that
			// only needs the metadata). Every release is then published as
			// "conformance not run", which is exactly what happened.
			fmt.Fprintf(os.Stderr, "no conformance results at %s; publishing every release as not_run\n", *results)
			raw, err = []byte("{}"), nil
		}
		if err != nil {
			return err
		}
		if err := json.Unmarshal(raw, &verdicts); err != nil {
			return fmt.Errorf("read %s: %w", *results, err)
		}
	}
	index, err := plugin.CompileIndex(entries, verdicts, time.Now())
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(*out), 0o755); err != nil {
		return err
	}
	if err := writeJSON(*out, index); err != nil {
		return err
	}
	fmt.Printf("→ %s (%d plugin(s))\n", *out, len(index.Plugins))
	return nil
}

func writeJSON(path string, value any) error {
	encoded, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, append(encoded, '\n'), 0o644)
}

func tick(ok bool) string {
	if ok {
		return "✓"
	}
	return "✗"
}
