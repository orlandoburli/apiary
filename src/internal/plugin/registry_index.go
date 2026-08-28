package plugin

import (
	"encoding/json"
	"fmt"
	"runtime"
	"sort"
	"strings"

	"github.com/Masterminds/semver/v3"
)

// The registry is a static, reviewed index: metadata and digests only. It never
// carries plugin code, and the daemon never reads it — resolution happens in the
// CLI, before anything is downloaded, so an incompatible or unavailable release
// is reported instead of installed.
const (
	// IndexSchemaVersion is the compiled index format this Apiary understands.
	// An unknown version fails closed rather than being partially interpreted.
	IndexSchemaVersion = 1

	// DefaultRegistryURL is used when apiary.yaml declares no plugin_registries.
	DefaultRegistryURL = "https://orlandoburli.com.br/apiary/registry/v1/index.json"
)

// Index is the compiled registry document: every reviewed entry, in one file, so
// a CLI command needs exactly one request.
type Index struct {
	SchemaVersion int              `json:"schema_version"`
	GeneratedAt   string           `json:"generated_at,omitempty"`
	Plugins       []RegistryPlugin `json:"plugins"`
}

// RegistryPlugin is one plugin's entry: stable identity plus its releases.
type RegistryPlugin struct {
	ID           string       `json:"id" yaml:"id"`
	Summary      string       `json:"summary" yaml:"summary"`
	Capabilities []Capability `json:"capabilities" yaml:"capabilities"`
	Homepage     string       `json:"homepage,omitempty" yaml:"homepage,omitempty"`
	Repository   string       `json:"repository" yaml:"repository"`
	License      string       `json:"license,omitempty" yaml:"license,omitempty"`
	// ConformanceConfig is the plugin config the conformance kit should run
	// with. Registry CI uses it; the CLI never does, and it is not published in
	// the index. Absent means "the kit was not run", which `info` reports as
	// such rather than glossing over.
	ConformanceConfig map[string]any `json:"-" yaml:"conformance_config,omitempty"`
	Releases          []Release      `json:"releases" yaml:"releases"`
}

// Release is one published version of a plugin.
type Release struct {
	Version      string       `json:"version" yaml:"version"`
	Apiary       string       `json:"apiary" yaml:"apiary"`
	Protocol     int          `json:"protocol" yaml:"protocol"`
	PublishedAt  string       `json:"published_at,omitempty" yaml:"published_at,omitempty"`
	Yanked       bool         `json:"yanked,omitempty" yaml:"yanked,omitempty"`
	YankedReason string       `json:"yanked_reason,omitempty" yaml:"yanked_reason,omitempty"`
	Conformance  *Conformance `json:"conformance,omitempty" yaml:"-"`
	Artifacts    []Artifact   `json:"artifacts" yaml:"artifacts"`
}

// Conformance records what registry CI observed when it ran the protocol
// conformance kit against the published artifact. It is written by CI, never by
// the submitter, and is a test result — not an endorsement.
type Conformance struct {
	Status    string `json:"status"` // passed | failed | not_run
	Kit       string `json:"kit,omitempty"`
	CheckedAt string `json:"checked_at,omitempty"`
	Commit    string `json:"commit,omitempty"`
}

// Artifact is one platform's download. Both digests are mandatory: the archive's,
// so the download can be verified before it is unpacked, and the executable's,
// so the installed manifest can be pinned to a value that originates outside the
// publisher's own release.
type Artifact struct {
	OS               string `json:"os" yaml:"os"`
	Arch             string `json:"arch" yaml:"arch"`
	URL              string `json:"url" yaml:"url"`
	ArchiveSHA256    string `json:"archive_sha256" yaml:"archive_sha256"`
	ExecutableSHA256 string `json:"executable_sha256" yaml:"executable_sha256"`
}

// ParseIndex decodes and fully validates a compiled index. A malformed or
// unknown-version document is rejected outright: a half-understood registry is
// worse than no registry.
func ParseIndex(data []byte) (*Index, error) {
	var index Index
	dec := json.NewDecoder(strings.NewReader(string(data)))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&index); err != nil {
		return nil, fmt.Errorf("decode registry index: %w", err)
	}
	if index.SchemaVersion != IndexSchemaVersion {
		return nil, fmt.Errorf("unsupported registry index schema_version %d; this Apiary supports %d — upgrade Apiary or point plugin_registries at a v%d index",
			index.SchemaVersion, IndexSchemaVersion, IndexSchemaVersion)
	}
	seen := map[string]bool{}
	for i := range index.Plugins {
		entry := &index.Plugins[i]
		if errs := entry.Validate(); len(errs) > 0 {
			return nil, fmt.Errorf("registry index entry %q: %w", entry.ID, errs[0])
		}
		if seen[entry.ID] {
			return nil, fmt.Errorf("registry index lists %q twice", entry.ID)
		}
		seen[entry.ID] = true
	}
	sort.Slice(index.Plugins, func(i, j int) bool { return index.Plugins[i].ID < index.Plugins[j].ID })
	return &index, nil
}

// Validate checks one entry against the rules registry CI enforces at review
// time. The CLI re-runs it on every fetch so a mis-published index cannot make
// a client behave in ways review never approved.
func (p *RegistryPlugin) Validate() []error {
	var errs []error
	if !validPluginID(p.ID) {
		errs = append(errs, fmt.Errorf("id %q must be a lowercase reverse-DNS identifier", p.ID))
	}
	if strings.TrimSpace(p.Summary) == "" {
		errs = append(errs, fmt.Errorf("summary is required"))
	}
	if strings.TrimSpace(p.Repository) == "" {
		errs = append(errs, fmt.Errorf("repository is required: an operator must be able to read the code before running it"))
	}
	if len(p.Capabilities) == 0 {
		errs = append(errs, fmt.Errorf("at least one capability is required"))
	}
	for _, capability := range p.Capabilities {
		if _, ok := supportedCapabilities[capability]; !ok {
			errs = append(errs, fmt.Errorf("unsupported capability %q; supported: %s", capability, strings.Join(SupportedCapabilityNames(), ", ")))
		}
	}
	if len(p.Releases) == 0 {
		errs = append(errs, fmt.Errorf("at least one release is required"))
	}
	versions := map[string]bool{}
	for i := range p.Releases {
		release := &p.Releases[i]
		prefix := fmt.Sprintf("releases[%d]", i)
		if _, err := semver.StrictNewVersion(release.Version); err != nil {
			errs = append(errs, fmt.Errorf("%s: version %q is not semantic versioning", prefix, release.Version))
		} else if versions[release.Version] {
			errs = append(errs, fmt.Errorf("%s: duplicate version %q; releases are immutable, publish a new version instead", prefix, release.Version))
		}
		versions[release.Version] = true
		if strings.TrimSpace(release.Apiary) == "" {
			errs = append(errs, fmt.Errorf("%s: apiary compatibility constraint is required", prefix))
		} else if _, err := semver.NewConstraint(release.Apiary); err != nil {
			errs = append(errs, fmt.Errorf("%s: apiary compatibility %q is invalid: %w", prefix, release.Apiary, err))
		}
		if release.Protocol <= 0 {
			errs = append(errs, fmt.Errorf("%s: protocol is required", prefix))
		}
		if release.Yanked && strings.TrimSpace(release.YankedReason) == "" {
			errs = append(errs, fmt.Errorf("%s: yanked releases must state a yanked_reason", prefix))
		}
		if len(release.Artifacts) == 0 {
			errs = append(errs, fmt.Errorf("%s: at least one artifact is required", prefix))
		}
		platforms := map[string]bool{}
		for j := range release.Artifacts {
			artifact := &release.Artifacts[j]
			where := fmt.Sprintf("%s.artifacts[%d]", prefix, j)
			if artifact.OS == "" || artifact.Arch == "" {
				errs = append(errs, fmt.Errorf("%s: os and arch are required (GOOS/GOARCH spelling)", where))
			}
			platform := artifact.Platform()
			if platforms[platform] {
				errs = append(errs, fmt.Errorf("%s: duplicate platform %q", where, platform))
			}
			platforms[platform] = true
			if !strings.HasPrefix(artifact.URL, "https://") {
				errs = append(errs, fmt.Errorf("%s: url must be https", where))
			}
			for label, digest := range map[string]string{"archive_sha256": artifact.ArchiveSHA256, "executable_sha256": artifact.ExecutableSHA256} {
				if _, _, err := normalizeChecksum(digest); err != nil || strings.TrimSpace(digest) == "" {
					errs = append(errs, fmt.Errorf("%s: %s is required and must be a SHA-256 digest", where, label))
				}
			}
		}
	}
	return errs
}

// Platform is the artifact's GOOS/GOARCH pair as one comparable string.
func (a Artifact) Platform() string { return a.OS + "/" + a.Arch }

// Find returns the entry for an id.
func (idx *Index) Find(id string) (*RegistryPlugin, bool) {
	if idx == nil {
		return nil, false
	}
	for i := range idx.Plugins {
		if idx.Plugins[i].ID == id {
			return &idx.Plugins[i], true
		}
	}
	return nil, false
}

// Search matches a free-text query against ids and summaries, optionally
// filtered by capability. An empty query lists everything.
func (idx *Index) Search(query string, capability Capability) []*RegistryPlugin {
	if idx == nil {
		return nil
	}
	needle := strings.ToLower(strings.TrimSpace(query))
	var matches []*RegistryPlugin
	for i := range idx.Plugins {
		entry := &idx.Plugins[i]
		if capability != "" && !entry.HasCapability(capability) {
			continue
		}
		haystack := strings.ToLower(entry.ID + " " + entry.Summary)
		if needle == "" || strings.Contains(haystack, needle) {
			matches = append(matches, entry)
		}
	}
	return matches
}

// HasCapability reports whether the entry declares a capability.
func (p *RegistryPlugin) HasCapability(capability Capability) bool {
	for _, current := range p.Capabilities {
		if current == capability {
			return true
		}
	}
	return false
}

// Suggest returns ids close to a miss, so a typo does not read as "no such
// plugin". Comparison is a cheap prefix/substring match on the last label.
func (idx *Index) Suggest(id string) []string {
	if idx == nil {
		return nil
	}
	needle := strings.ToLower(id)
	if at := strings.LastIndex(needle, "."); at >= 0 {
		needle = needle[at+1:]
	}
	var out []string
	for i := range idx.Plugins {
		candidate := idx.Plugins[i].ID
		if needle != "" && strings.Contains(strings.ToLower(candidate), needle) {
			out = append(out, candidate)
		}
	}
	sort.Strings(out)
	return out
}

// ResolveOptions selects one artifact for one host.
type ResolveOptions struct {
	Version       string // exact version; empty means "newest usable"
	ApiaryVersion string // host version; "" or "dev" skips the constraint check
	GOOS          string // defaults to the running platform
	GOARCH        string
}

// Resolved is the outcome of resolution: which release, and which download.
type Resolved struct {
	Plugin   *RegistryPlugin
	Release  *Release
	Artifact *Artifact
	// CompatibilityUnchecked is set when the host's own version string is not
	// semantic versioning — a development build, typically. The constraint is
	// skipped rather than failing every resolution, and commands say so instead
	// of implying a check that did not happen.
	CompatibilityUnchecked string
}

// Resolve picks the release to install, running every check that can be made
// before a byte is downloaded. When nothing qualifies it explains which
// predicate eliminated the newest candidate — "0.3.0 requires apiary >= 0.20.0,
// you are on 0.19.1" is the answer worth having before a download, not after.
func (p *RegistryPlugin) Resolve(opts ResolveOptions) (*Resolved, error) {
	if opts.GOOS == "" {
		opts.GOOS = runtime.GOOS
	}
	if opts.GOARCH == "" {
		opts.GOARCH = runtime.GOARCH
	}
	candidates := make([]*Release, 0, len(p.Releases))
	for i := range p.Releases {
		if opts.Version != "" && p.Releases[i].Version != opts.Version {
			continue
		}
		candidates = append(candidates, &p.Releases[i])
	}
	if len(candidates) == 0 {
		return nil, fmt.Errorf("%s has no release %s; published versions: %s", p.ID, opts.Version, strings.Join(p.versions(), ", "))
	}
	sort.Slice(candidates, func(i, j int) bool {
		left, errLeft := semver.NewVersion(candidates[i].Version)
		right, errRight := semver.NewVersion(candidates[j].Version)
		if errLeft != nil || errRight != nil {
			return candidates[i].Version > candidates[j].Version
		}
		return left.GreaterThan(right)
	})

	var firstRejection error
	for _, release := range candidates {
		artifact, unchecked, err := release.usableOn(opts)
		if err != nil {
			if firstRejection == nil {
				firstRejection = fmt.Errorf("%s %s: %w", p.ID, release.Version, err)
			}
			continue
		}
		return &Resolved{Plugin: p, Release: release, Artifact: artifact, CompatibilityUnchecked: unchecked}, nil
	}
	return nil, fmt.Errorf("no usable release of %s: %w", p.ID, firstRejection)
}

// usableOn evaluates the pre-download predicates in the order an operator would
// ask them: is it withdrawn, can this Apiary speak to it, is it built for this
// machine.
func (r *Release) usableOn(opts ResolveOptions) (artifact *Artifact, unchecked string, err error) {
	if r.Yanked {
		return nil, "", fmt.Errorf("withdrawn by its publisher (%s)", r.YankedReason)
	}
	if r.Protocol != ProtocolVersion {
		return nil, "", fmt.Errorf("speaks plugin protocol %d; this Apiary speaks protocol %d", r.Protocol, ProtocolVersion)
	}
	if opts.ApiaryVersion != "" && opts.ApiaryVersion != "dev" {
		constraint, err := semver.NewConstraint(r.Apiary)
		if err != nil {
			return nil, "", fmt.Errorf("declares an invalid apiary constraint %q", r.Apiary)
		}
		host, hostErr := semver.NewVersion(opts.ApiaryVersion)
		switch {
		case hostErr != nil:
			// Development builds carry version strings git describe invented.
			// Refusing every plugin on that basis would be a worse answer than
			// installing one and letting the daemon's own manifest check —
			// which runs against the same constraint — have the final word.
			unchecked = opts.ApiaryVersion
		case !constraint.Check(host):
			return nil, "", fmt.Errorf("requires apiary %s, this host is %s", r.Apiary, host)
		}
	}
	artifact = r.ArtifactFor(opts.GOOS, opts.GOARCH)
	if artifact == nil {
		return nil, "", fmt.Errorf("has no build for %s/%s (available: %s)", opts.GOOS, opts.GOARCH, strings.Join(r.Platforms(), ", "))
	}
	return artifact, unchecked, nil
}

// ArtifactFor returns the download for one platform, or nil.
func (r *Release) ArtifactFor(goos, goarch string) *Artifact {
	for i := range r.Artifacts {
		if r.Artifacts[i].OS == goos && r.Artifacts[i].Arch == goarch {
			return &r.Artifacts[i]
		}
	}
	return nil
}

// Platforms lists the release's platforms, for error messages and `info`.
func (r *Release) Platforms() []string {
	out := make([]string, 0, len(r.Artifacts))
	for _, artifact := range r.Artifacts {
		out = append(out, artifact.Platform())
	}
	sort.Strings(out)
	return out
}

func (p *RegistryPlugin) versions() []string {
	out := make([]string, 0, len(p.Releases))
	for _, release := range p.Releases {
		out = append(out, release.Version)
	}
	return out
}
