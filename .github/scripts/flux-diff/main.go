package main

import (
	"bytes"
	"crypto/md5"
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"gopkg.in/yaml.v3"
)

// ---------------------------------------------------------------------------
// Flux / Kubernetes resource types (only the fields we care about)
// ---------------------------------------------------------------------------

type Metadata struct {
	Name      string `yaml:"name"`
	Namespace string `yaml:"namespace"`
}

// HelmRelease represents a Flux HelmRelease resource.
type HelmRelease struct {
	Kind     string   `yaml:"kind"`
	Metadata Metadata `yaml:"metadata"`
	Spec     struct {
		ChartRef *struct {
			Kind string `yaml:"kind"`
			Name string `yaml:"name"`
		} `yaml:"chartRef"`
		Chart *struct {
			Spec struct {
				Chart     string `yaml:"chart"`
				Version   string `yaml:"version"`
				SourceRef struct {
					Kind string `yaml:"kind"`
					Name string `yaml:"name"`
				} `yaml:"sourceRef"`
			} `yaml:"spec"`
		} `yaml:"chart"`
		Values     map[string]any `yaml:"values"`
		ValuesFrom []struct {
			Kind      string `yaml:"kind"`
			Name      string `yaml:"name"`
			ValuesKey string `yaml:"valuesKey"`
		} `yaml:"valuesFrom"`
	} `yaml:"spec"`
}

// HelmRepository represents a Flux HelmRepository resource.
type HelmRepository struct {
	Kind     string   `yaml:"kind"`
	Metadata Metadata `yaml:"metadata"`
	Spec     struct {
		URL string `yaml:"url"`
	} `yaml:"spec"`
}

// OCIRepository represents a Flux OCIRepository resource.
type OCIRepository struct {
	Kind     string   `yaml:"kind"`
	Metadata Metadata `yaml:"metadata"`
	Spec     struct {
		URL string `yaml:"url"`
		Ref struct {
			Tag    string `yaml:"tag"`
			Semver string `yaml:"semver"`
		} `yaml:"ref"`
	} `yaml:"spec"`
}

// ---------------------------------------------------------------------------
// chartInfo holds the resolved chart reference for a HelmRelease.
// ---------------------------------------------------------------------------

type chartInfo struct {
	Type    string // "oci" or "helm-repo"
	Name    string // chart name (helm-repo) or OCI URL (oci)
	Version string // pinned version / tag
	Semver  string // semver range (oci only)
	RepoURL string // HelmRepository URL (helm-repo only)
}

// ---------------------------------------------------------------------------
// releaseResult captures the diff output for one HelmRelease.
// ---------------------------------------------------------------------------

type releaseResult struct {
	DisplayName string
	ReleaseName string
	ChangeDesc  string
	Diff        string // empty means no diff
	Additions   int
	Deletions   int
	Failed      bool
	FailedSide  string
	Deleted     bool
}

// ---------------------------------------------------------------------------
// YAML helpers
// ---------------------------------------------------------------------------

// readKind reads a YAML file and returns its "kind" field.
func readKind(path string) string {
	data, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	var m struct {
		Kind string `yaml:"kind"`
	}
	if err := yaml.Unmarshal(data, &m); err != nil {
		return ""
	}
	return m.Kind
}

// parseHelmRelease reads a HelmRelease from a YAML file.
func parseHelmRelease(path string) (*HelmRelease, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var hr HelmRelease
	if err := yaml.Unmarshal(data, &hr); err != nil {
		return nil, fmt.Errorf("parsing %s: %w", path, err)
	}
	if hr.Kind != "HelmRelease" {
		return nil, fmt.Errorf("%s is not a HelmRelease (kind=%s)", path, hr.Kind)
	}
	return &hr, nil
}

// findHelmReleaseFiles returns all .yaml files in dir that contain a HelmRelease.
func findHelmReleaseFiles(dir string) []string {
	entries, err := filepath.Glob(filepath.Join(dir, "*.yaml"))
	if err != nil {
		return nil
	}
	var out []string
	for _, e := range entries {
		if readKind(e) == "HelmRelease" {
			out = append(out, e)
		}
	}
	return out
}

// findRepoInDir scans dir for a HelmRepository or OCIRepository with the given
// kind and name, returning its parsed bytes.
func findRepoInDir(dir, kind, name string) ([]byte, error) {
	entries, err := filepath.Glob(filepath.Join(dir, "*.yaml"))
	if err != nil {
		return nil, err
	}
	for _, e := range entries {
		data, err := os.ReadFile(e)
		if err != nil {
			continue
		}
		var m struct {
			Kind     string   `yaml:"kind"`
			Metadata Metadata `yaml:"metadata"`
		}
		if err := yaml.Unmarshal(data, &m); err != nil {
			continue
		}
		if m.Kind == kind && m.Metadata.Name == name {
			return data, nil
		}
	}
	return nil, fmt.Errorf("%s/%s not found in %s", kind, name, dir)
}

// ---------------------------------------------------------------------------
// Chart resolution
// ---------------------------------------------------------------------------

// resolveChart determines the chart source for a HelmRelease.
func resolveChart(hr *HelmRelease, dir string) (chartInfo, error) {
	if hr.Spec.ChartRef != nil && hr.Spec.ChartRef.Kind == "OCIRepository" {
		data, err := findRepoInDir(dir, "OCIRepository", hr.Spec.ChartRef.Name)
		if err != nil {
			return chartInfo{}, err
		}
		var repo OCIRepository
		if err := yaml.Unmarshal(data, &repo); err != nil {
			return chartInfo{}, err
		}
		return chartInfo{
			Type:    "oci",
			Name:    repo.Spec.URL,
			Version: repo.Spec.Ref.Tag,
			Semver:  repo.Spec.Ref.Semver,
		}, nil
	}

	if hr.Spec.Chart == nil {
		return chartInfo{}, fmt.Errorf("HelmRelease %s has neither chartRef nor chart", hr.Metadata.Name)
	}

	cs := hr.Spec.Chart.Spec
	data, err := findRepoInDir(dir, "HelmRepository", cs.SourceRef.Name)
	if err != nil {
		return chartInfo{}, err
	}
	var repo HelmRepository
	if err := yaml.Unmarshal(data, &repo); err != nil {
		return chartInfo{}, err
	}
	return chartInfo{
		Type:    "helm-repo",
		Name:    cs.Chart,
		Version: cs.Version,
		RepoURL: repo.Spec.URL,
	}, nil
}

// effectiveVersion returns the most specific version string for display.
func (ci chartInfo) effectiveVersion() string {
	if ci.Version != "" {
		return ci.Version
	}
	return ci.Semver
}

// ---------------------------------------------------------------------------
// Values resolution
// ---------------------------------------------------------------------------

// resolveValues merges inline spec.values with any valuesFrom ConfigMap files.
func resolveValues(hr *HelmRelease, dir string) (map[string]any, error) {
	merged := make(map[string]any)

	// Start with inline values.
	for k, v := range hr.Spec.Values {
		merged[k] = v
	}

	// Overlay each valuesFrom ConfigMap.
	for _, vf := range hr.Spec.ValuesFrom {
		if vf.Kind != "ConfigMap" {
			continue
		}
		key := vf.ValuesKey
		if key == "" {
			key = "values.yaml"
		}
		path := filepath.Join(dir, key)
		data, err := os.ReadFile(path)
		if err != nil {
			// File might not exist locally; skip.
			continue
		}
		var overlay map[string]any
		if err := yaml.Unmarshal(data, &overlay); err != nil {
			continue
		}
		merged = mergeMaps(merged, overlay)
	}

	return merged, nil
}

// mergeMaps performs a deep merge of b into a (b wins on conflicts).
func mergeMaps(a, b map[string]any) map[string]any {
	out := make(map[string]any, len(a))
	for k, v := range a {
		out[k] = v
	}
	for k, v := range b {
		if existing, ok := out[k]; ok {
			existMap, existOK := existing.(map[string]any)
			newMap, newOK := v.(map[string]any)
			if existOK && newOK {
				out[k] = mergeMaps(existMap, newMap)
				continue
			}
		}
		out[k] = v
	}
	return out
}

// writeValuesFile serializes values to a temp file and returns its path.
func writeValuesFile(values map[string]any) (string, error) {
	data, err := yaml.Marshal(values)
	if err != nil {
		return "", err
	}
	f, err := os.CreateTemp("", "values-*.yaml")
	if err != nil {
		return "", err
	}
	if _, err := f.Write(data); err != nil {
		f.Close()
		return "", err
	}
	f.Close()
	return f.Name(), nil
}

// ---------------------------------------------------------------------------
// Helm template execution
// ---------------------------------------------------------------------------

// helmTemplate renders a chart and returns the rendered manifest bytes.
func helmTemplate(hr *HelmRelease, ci chartInfo, valuesPath string) ([]byte, error) {
	ns := hr.Metadata.Namespace
	if ns == "" {
		ns = "default"
	}

	args := []string{"template", hr.Metadata.Name}

	switch ci.Type {
	case "oci":
		args = append(args, ci.Name)
		if ci.Version != "" {
			args = append(args, "--version", ci.Version)
		}
	case "helm-repo":
		alias := fmt.Sprintf("repo-%x", md5.Sum([]byte(ci.RepoURL)))[:14]
		// Add and update repo.
		run("helm", "repo", "add", alias, ci.RepoURL, "--force-update")
		run("helm", "repo", "update", alias)
		args = append(args, alias+"/"+ci.Name)
		if ci.Version != "" {
			args = append(args, "--version", ci.Version)
		}
	}

	args = append(args, "--namespace", ns, "--values", valuesPath)

	cmd := exec.Command("helm", args...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("helm template failed: %s\n%s", err, stderr.String())
	}
	return stdout.Bytes(), nil
}

// run executes a command silently (best-effort, ignores errors).
func run(name string, args ...string) {
	cmd := exec.Command(name, args...)
	cmd.Stdout = nil
	cmd.Stderr = nil
	_ = cmd.Run()
}

// ---------------------------------------------------------------------------
// Diff
// ---------------------------------------------------------------------------

// computeDiff shells out to diff(1) for a unified diff.
func computeDiff(baseLabel string, base []byte, prLabel string, pr []byte) (string, int, int, error) {
	baseFile, err := os.CreateTemp("", "base-*.yaml")
	if err != nil {
		return "", 0, 0, err
	}
	defer os.Remove(baseFile.Name())
	baseFile.Write(base)
	baseFile.Close()

	prFile, err := os.CreateTemp("", "pr-*.yaml")
	if err != nil {
		return "", 0, 0, err
	}
	defer os.Remove(prFile.Name())
	prFile.Write(pr)
	prFile.Close()

	cmd := exec.Command("diff", "-u",
		"--label", baseLabel, baseFile.Name(),
		"--label", prLabel, prFile.Name())
	var stdout bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = nil
	_ = cmd.Run() // exit 1 means differences found

	out := stdout.String()
	if out == "" {
		return "", 0, 0, nil
	}

	adds, dels := 0, 0
	for _, line := range strings.Split(out, "\n") {
		if strings.HasPrefix(line, "+") && !strings.HasPrefix(line, "+++") {
			adds++
		}
		if strings.HasPrefix(line, "-") && !strings.HasPrefix(line, "---") {
			dels++
		}
	}
	return out, adds, dels, nil
}

// ---------------------------------------------------------------------------
// Display name
// ---------------------------------------------------------------------------

// displayName turns a directory like "infrastructure/monitoring/controllers/kube-prometheus-stack"
// into a concise label like "monitoring/kube-prometheus-stack".
func displayName(dir string) string {
	d := dir
	for _, prefix := range []string{"infrastructure/", "services/"} {
		d = strings.TrimPrefix(d, prefix)
	}
	d = strings.ReplaceAll(d, "/controllers/", "/")
	return d
}

// ---------------------------------------------------------------------------
// Main
// ---------------------------------------------------------------------------

func main() {
	dirs := strings.Fields(os.Getenv("CHANGED_DIRS"))
	if len(dirs) == 0 {
		log.Fatal("CHANGED_DIRS is empty")
	}

	var results []releaseResult

	for _, dir := range dirs {
		dname := displayName(dir)
		baseDir := filepath.Join("base", "k8s", dir)
		prDir := filepath.Join("pr", "k8s", dir)

		baseFiles := findHelmReleaseFiles(baseDir)
		prFiles := findHelmReleaseFiles(prDir)

		// Index base releases by filename for lookup.
		baseByFile := make(map[string]string, len(baseFiles))
		for _, f := range baseFiles {
			baseByFile[filepath.Base(f)] = f
		}

		// Process each release in the PR branch.
		for _, prPath := range prFiles {
			prHR, err := parseHelmRelease(prPath)
			if err != nil {
				log.Printf("WARNING: skipping %s: %v", prPath, err)
				continue
			}
			releaseName := prHR.Metadata.Name
			basePath := baseByFile[filepath.Base(prPath)]

			// ── Detect what changed ──────────────────────────────────
			var versionChange, changeDesc string
			valuesChanged := false

			if basePath != "" {
				baseHR, err := parseHelmRelease(basePath)
				if err == nil {
					baseCI, _ := resolveChart(baseHR, baseDir)
					prCI, _ := resolveChart(prHR, prDir)

					baseVer := baseCI.effectiveVersion()
					prVer := prCI.effectiveVersion()
					if baseVer != prVer {
						versionChange = baseVer + " → " + prVer
					}

					baseVals, _ := resolveValues(baseHR, baseDir)
					prVals, _ := resolveValues(prHR, prDir)
					baseYAML, _ := yaml.Marshal(baseVals)
					prYAML, _ := yaml.Marshal(prVals)
					if !bytes.Equal(baseYAML, prYAML) {
						valuesChanged = true
					}
				}
			} else {
				versionChange = "new release"
			}

			// Build change description.
			if versionChange != "" {
				changeDesc = "version: `" + versionChange + "`"
			}
			if valuesChanged {
				if changeDesc != "" {
					changeDesc += ", "
				}
				changeDesc += "values changed"
			}
			if changeDesc == "" {
				changeDesc = "configuration changed"
			}

			// ── Template both versions ───────────────────────────────
			var baseManifest, prManifest []byte
			failedSide := ""

			if basePath != "" {
				baseHR, _ := parseHelmRelease(basePath)
				baseCI, _ := resolveChart(baseHR, baseDir)
				baseVals, _ := resolveValues(baseHR, baseDir)
				vf, _ := writeValuesFile(baseVals)
				defer os.Remove(vf)
				out, err := helmTemplate(baseHR, baseCI, vf)
				if err != nil {
					log.Printf("WARNING: base template failed for %s: %v", releaseName, err)
					failedSide = "base"
				} else {
					baseManifest = out
				}
			}

			prCI, err := resolveChart(prHR, prDir)
			if err != nil {
				log.Printf("WARNING: cannot resolve chart for %s: %v", releaseName, err)
				results = append(results, releaseResult{
					DisplayName: dname,
					ReleaseName: releaseName,
					ChangeDesc:  changeDesc,
					Failed:      true,
					FailedSide:  "pr (chart resolution)",
				})
				continue
			}
			prVals, _ := resolveValues(prHR, prDir)
			vf, _ := writeValuesFile(prVals)
			defer os.Remove(vf)
			prOut, err := helmTemplate(prHR, prCI, vf)
			if err != nil {
				log.Printf("WARNING: PR template failed for %s: %v", releaseName, err)
				if failedSide != "" {
					failedSide = "base + pr"
				} else {
					failedSide = "pr"
				}
			} else {
				prManifest = prOut
			}

			if failedSide != "" {
				results = append(results, releaseResult{
					DisplayName: dname,
					ReleaseName: releaseName,
					ChangeDesc:  changeDesc,
					Failed:      true,
					FailedSide:  failedSide,
				})
				continue
			}

			// ── Diff ─────────────────────────────────────────────────
			diffText, adds, dels, err := computeDiff(
				"base/"+releaseName, baseManifest,
				"pr/"+releaseName, prManifest,
			)
			if err != nil {
				log.Printf("WARNING: diff failed for %s: %v", releaseName, err)
				continue
			}

			results = append(results, releaseResult{
				DisplayName: dname,
				ReleaseName: releaseName,
				ChangeDesc:  changeDesc,
				Diff:        diffText,
				Additions:   adds,
				Deletions:   dels,
			})
		}

		// ── Deleted releases ─────────────────────────────────────────
		prByFile := make(map[string]bool, len(prFiles))
		for _, f := range prFiles {
			prByFile[filepath.Base(f)] = true
		}
		for _, basePath := range baseFiles {
			if !prByFile[filepath.Base(basePath)] {
				baseHR, err := parseHelmRelease(basePath)
				if err != nil {
					continue
				}
				results = append(results, releaseResult{
					DisplayName: dname,
					ReleaseName: baseHR.Metadata.Name,
					Deleted:     true,
				})
			}
		}
	}

	// ── Render Markdown ──────────────────────────────────────────────────
	var buf bytes.Buffer
	buf.WriteString("## Flux Diff Preview\n\n")

	if len(results) > 0 {
		buf.WriteString("| HelmRelease | Change | Impact |\n")
		buf.WriteString("|-------------|--------|--------|\n")
		for _, r := range results {
			name := r.DisplayName + "/" + r.ReleaseName
			switch {
			case r.Deleted:
				fmt.Fprintf(&buf, "| %s | deleted | — |\n", name)
			case r.Failed:
				fmt.Fprintf(&buf, "| %s | %s | :warning: template failed |\n", name, r.ChangeDesc)
			case r.Diff != "":
				fmt.Fprintf(&buf, "| %s | %s | +%d/-%d |\n", name, r.ChangeDesc, r.Additions, r.Deletions)
			default:
				fmt.Fprintf(&buf, "| %s | %s | no manifest diff |\n", name, r.ChangeDesc)
			}
		}
	} else {
		buf.WriteString("> No HelmRelease manifest changes detected.\n")
	}

	buf.WriteString("\n")

	for _, r := range results {
		name := r.DisplayName + "/" + r.ReleaseName
		switch {
		case r.Deleted:
			fmt.Fprintf(&buf, "<details>\n<summary>:wastebasket: <b>%s</b> — deleted</summary>\n\n", name)
			buf.WriteString("> This HelmRelease has been removed in this PR.\n\n</details>\n\n")

		case r.Failed:
			fmt.Fprintf(&buf, "<details>\n<summary>:warning: <b>%s</b> — %s (template failed: %s)</summary>\n\n", name, r.ChangeDesc, r.FailedSide)
			buf.WriteString("> Helm template failed. This can happen with charts that require CRDs or\n")
			buf.WriteString("> dependencies not available in a standalone template context.\n\n</details>\n\n")

		case r.Diff != "":
			fmt.Fprintf(&buf, "<details>\n<summary>:memo: <b>%s</b> — %s (+%d/-%d)</summary>\n\n", name, r.ChangeDesc, r.Additions, r.Deletions)
			fmt.Fprintf(&buf, "```diff\n%s\n```\n\n</details>\n\n", r.Diff)

		default:
			fmt.Fprintf(&buf, "<details>\n<summary>:white_check_mark: <b>%s</b> — %s (no manifest diff)</summary>\n\n", name, r.ChangeDesc)
			buf.WriteString("> The rendered manifests are identical. The change may only affect Flux\n")
			buf.WriteString("> metadata (intervals, dependencies, etc.) that don't alter deployed resources.\n\n</details>\n\n")
		}
	}

	outputPath := "/tmp/comment-body.md"
	if err := os.WriteFile(outputPath, buf.Bytes(), 0644); err != nil {
		log.Fatalf("Failed to write output: %v", err)
	}
	fmt.Printf("Comment written to %s\n", outputPath)
}
