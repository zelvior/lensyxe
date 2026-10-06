// Package ci implements the CI-facing surface of Lensyxe: workflow risk
// detection, pull-request comment generation, and the environment plumbing the
// GitHub Action relies on.
//
// Everything here is read-only with respect to the repository and the network.
// The package parses files and environment variables; it never calls the GitHub
// API. Posting a comment is the Action's job, not Lensyxe's, which keeps
// Lensyxe free of a network client and makes every code path here testable
// with a fixture file.
package ci

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"
)

// WorkflowsDir is the conventional location of GitHub Actions workflows,
// relative to the repository root.
const WorkflowsDir = ".github/workflows"

// maxMatrixFanout is the number of matrix jobs above which the build time
// cost is flagged. 256 is chosen as a round number that comfortably covers a
// legitimate OS/language matrix while catching unbounded fan-out.
const maxMatrixFanout = 256

// severity levels for workflow findings, ordered worst first.
type severity string

const (
	severityCritical severity = "critical"
	severityHigh     severity = "high"
	severityMedium   severity = "medium"
	severityLow      severity = "low"
)

func (s severity) rank() int {
	switch s {
	case severityCritical:
		return 4
	case severityHigh:
		return 3
	case severityMedium:
		return 2
	case severityLow:
		return 1
	default:
		return 0
	}
}

// WorkflowFinding is one detected risk in a CI workflow.
type WorkflowFinding struct {
	// Rule is a stable identifier such as "missing_cache".
	Rule string `json:"rule"`
	// Severity grades the risk.
	Severity severity `json:"severity"`
	// Title is a one-line human summary.
	Title string `json:"title"`
	// Detail explains the risk with the specific location and value.
	Detail string `json:"detail"`
	// File is the workflow path, relative to the repository root.
	File string `json:"file"`
	// Job is the job the finding belongs to, empty for workflow-level findings.
	Job string `json:"job,omitempty"`
	// Recommendation is the concrete fix.
	Recommendation string `json:"recommendation"`
}

// WorkflowResult is the analysis of a single workflow file.
type WorkflowResult struct {
	File     string            `json:"file"`
	Name     string            `json:"name"`
	Jobs     []string          `json:"jobs"`
	Findings []WorkflowFinding `json:"findings"`
}

// Report is the analysis of a repository's workflows.
type Report struct {
	// Workflows is the number of workflow files inspected.
	Workflows int `json:"workflows"`
	// Jobs is the total number of jobs across them.
	Jobs int `json:"jobs"`
	// Findings is sorted by severity then rule then location.
	Findings []WorkflowFinding `json:"findings"`
}

// HasFindings reports whether any risk was detected.
func (r Report) HasFindings() bool { return len(r.Findings) > 0 }

// workflow is the subset of a GitHub Actions workflow this analyzer reads.
//
// Fields are typed as `any` or a generic map rather than fully modelled
// because workflows are free-form; over-modelling them would mean silently
// ignoring the parts that matter and reporting false confidence.
type workflow struct {
	Name string         `yaml:"name"`
	On   yaml.Node      `yaml:"on"`
	Jobs map[string]job `yaml:"jobs"`
	// Permissions at the workflow level. `any` because GitHub accepts either a
	// short string ("write-all") or a scope map.
	Permissions any       `yaml:"permissions"`
	Concurrency yaml.Node `yaml:"concurrency"`
}

// job is the subset of a job definition this analyzer reads.
type job struct {
	Name            string         `yaml:"name"`
	RunsOn          any            `yaml:"runs-on"`
	Steps           []step         `yaml:"steps"`
	Permissions     any            `yaml:"permissions"`
	Strategy        map[string]any `yaml:"strategy"`
	TimeoutMinutes  any            `yaml:"timeout-minutes"`
	Uses            string         `yaml:"uses"`
	With            map[string]any `yaml:"with"`
	ContinueOnError any            `yaml:"continue-on-error"`
	Environment     map[string]any `yaml:"environment"`
	Container       map[string]any `yaml:"container"`
	Services        map[string]any `yaml:"services"`
}

// step is a single job step.
type step struct {
	Name            string         `yaml:"name"`
	Uses            string         `yaml:"uses"`
	Run             string         `yaml:"run"`
	With            map[string]any `yaml:"with"`
	Env             map[string]any `yaml:"env"`
	If              string         `yaml:"if"`
	ContinueOnError any            `yaml:"continue-on-error"`
	TimeoutMinutes  any            `yaml:"timeout-minutes"`
}

// AnalyzeWorkflows inspects every workflow under root/.github/workflows.
//
// A missing directory is not an error: most repositories have no workflows,
// and that is not a problem worth reporting.
func AnalyzeWorkflows(root string) (Report, error) {
	dir := filepath.Join(root, WorkflowsDir)
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return Report{}, nil
		}
		return Report{}, fmt.Errorf("ci: read %s: %w", WorkflowsDir, err)
	}

	var report Report
	var results []WorkflowResult

	names := make([]string, 0, len(entries))
	for _, e := range entries {
		name := e.Name()
		// Only workflow definitions matter; a README or a directory in that
		// folder is not a build.
		if e.IsDir() || !(strings.HasSuffix(name, ".yml") || strings.HasSuffix(name, ".yaml")) {
			continue
		}
		names = append(names, name)
	}
	sort.Strings(names) // deterministic inspection order

	for _, name := range names {
		full := filepath.Join(dir, name)
		res, err := AnalyzeWorkflowFile(full, filepath.ToSlash(filepath.Join(WorkflowsDir, name)))
		if err != nil {
			return Report{}, err
		}
		report.Workflows++
		report.Jobs += len(res.Jobs)
		report.Findings = append(report.Findings, res.Findings...)
		results = append(results, res)
	}
	_ = results

	sortFindings(report.Findings)
	return report, nil
}

// AnalyzeWorkflowFile inspects a single workflow file.
//
// rel is the path reported in findings, so it can be repo-relative rather than
// whatever absolute path the caller happened to use.
func AnalyzeWorkflowFile(path, rel string) (WorkflowResult, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return WorkflowResult{}, fmt.Errorf("ci: read workflow %s: %w", path, err)
	}

	var wf workflow
	if err := yaml.Unmarshal(data, &wf); err != nil {
		// A workflow that does not parse is itself a finding: GitHub would
		// silently skip it, and the user would never learn why.
		return WorkflowResult{
			File: rel,
			Findings: []WorkflowFinding{{
				Rule:           "unparseable_workflow",
				Severity:       severityHigh,
				Title:          "Workflow could not be parsed",
				Detail:         fmt.Sprintf("%s is not valid workflow YAML: %v", rel, err),
				File:           rel,
				Recommendation: "Fix the YAML syntax error; GitHub ignores workflows it cannot parse.",
			}},
		}, nil
	}

	res := WorkflowResult{File: rel, Name: wf.Name, Findings: []WorkflowFinding{}}

	// Workflow-level permissions.
	checkPermissions(wf.Permissions, rel, "", &res.Findings)
	checkTriggerSecurity(&wf, rel, &res.Findings)

	for jobName, j := range wf.Jobs {
		res.Jobs = append(res.Jobs, jobName)
		checkJob(&wf, rel, jobName, j, &res.Findings)
	}
	sort.Strings(res.Jobs)
	sortFindings(res.Findings)
	return res, nil
}

// checkJob applies every per-job rule.
func checkJob(wf *workflow, rel, jobName string, j job, out *[]WorkflowFinding) {
	// Job-level permissions override the workflow level but are still a
	// risk when broad.
	checkPermissions(j.Permissions, rel, jobName, out)

	checkCaching(j, rel, jobName, out)
	checkActionPinning(j, rel, jobName, out)
	checkMatrix(rel, jobName, j, out)
	checkTimeouts(rel, jobName, j, out)
}

// checkPermissions flags write-all and overly broad write scopes.
//
// permissions is either a scalar ("write-all", "read-all") or a scope map.
func checkPermissions(permissions any, rel, jobName string, out *[]WorkflowFinding) {
	where := rel
	if jobName != "" {
		where = fmt.Sprintf("%s job %q", rel, jobName)
	}

	switch v := permissions.(type) {
	case string:
		if strings.EqualFold(v, "write-all") {
			*out = append(*out, WorkflowFinding{
				Rule:     "write_all_permissions",
				Severity: severityHigh,
				Title:    "Workflow grants write-all permissions",
				Detail: fmt.Sprintf(
					"%s sets `permissions: write-all`, giving every step token write access to the whole repository. A compromised dependency can push commits or release tags.",
					where),
				File:           rel,
				Job:            jobName,
				Recommendation: "Declare only the scopes the job needs, for example `permissions: contents: read`.",
			})
		}
	case map[string]any:
		// A map with a write scope is far less dangerous than write-all, but
		// contents: write is still worth flagging on a build job.
		for scope, level := range v {
			ls, lok := level.(string)
			if !lok || !strings.EqualFold(ls, "write") {
				continue
			}
			sev := severityLow
			title := "Workflow requests a write scope"
			if scope == "contents" {
				sev = severityMedium
				title = "Workflow requests contents: write"
			}
			*out = append(*out, WorkflowFinding{
				Rule:     "write_scope",
				Severity: sev,
				Title:    title,
				Detail: fmt.Sprintf(
					"%s requests `%s: write`. Read-only is sufficient for analysis, test, and build jobs.",
					where, scope),
				File:           rel,
				Job:            jobName,
				Recommendation: "Drop the write scope unless the job actually pushes commits or releases.",
			})
		}
	case nil:
		// No explicit permissions: GitHub falls back to the repository
		// default, which may itself be permissive. Not a finding on its own
		// because the repository setting is not visible from here.
	}
}

// checkCaching flags language setup actions used without their cache.
//
// Building the same toolchain from scratch on every run is the most common
// avoidable cost in CI, and every major action ships a cache switch for it.
func checkCaching(j job, rel, jobName string, out *[]WorkflowFinding) {
	for i, s := range j.Steps {
		uses := normalizeUses(s.Uses)
		switch uses {
		case "actions/setup-node":
			if !cacheEnabled(s.With) {
				*out = append(*out, WorkflowFinding{
					Rule:     "missing_cache",
					Severity: severityMedium,
					Title:    "actions/setup-node without caching",
					Detail: fmt.Sprintf(
						"%s job %q step %d uses actions/setup-node with no `cache:` option, so npm downloads are repeated on every run.",
						rel, jobName, stepIndex(i, s)),
					File:           rel,
					Job:            jobName,
					Recommendation: "Add `with: cache: npm` to the setup-node step.",
				})
			}
		case "actions/setup-go":
			if !cacheEnabled(s.With) {
				*out = append(*out, WorkflowFinding{
					Rule:     "missing_cache",
					Severity: severityMedium,
					Title:    "actions/setup-go without caching",
					Detail: fmt.Sprintf(
						"%s job %q step %d uses actions/setup-go with no `cache:` option, so the module cache is rebuilt every run.",
						rel, jobName, stepIndex(i, s)),
					File:           rel,
					Job:            jobName,
					Recommendation: "Add `with: cache: true` to the setup-go step.",
				})
			}
		case "actions/setup-python":
			if !cacheEnabled(s.With) && hasAnyKey(s.With, "cache", "cache-dependency-path") {
				// setup-python only caches when asked; a project with a
				// requirements file is the normal case, so this is only
				// reported when a dependency file is declared.
				*out = append(*out, WorkflowFinding{
					Rule:     "missing_cache",
					Severity: severityLow,
					Title:    "actions/setup-python without caching",
					Detail: fmt.Sprintf(
						"%s job %q step %d uses actions/setup-python with no `cache:` option.",
						rel, jobName, stepIndex(i, s)),
					File:           rel,
					Job:            jobName,
					Recommendation: "Add `with: cache: pip` if the job installs Python dependencies.",
				})
			}
		}
	}
}

// cacheEnabled reports whether a setup action was asked to cache.
//
// The flag name differs per action (`cache: true` for Go, `cache: npm` for
// Node), so presence of a non-empty `cache` key is the common test.
func cacheEnabled(with map[string]any) bool {
	if with == nil {
		return false
	}
	v, ok := with["cache"]
	if !ok {
		return false
	}
	switch t := v.(type) {
	case bool:
		return t
	case string:
		return t != "" && !strings.EqualFold(t, "false")
	case nil:
		return false
	default:
		// A non-empty value of any other type is an explicit request.
		return true
	}
}

// checkActionPinning flags actions that are not pinned to an immutable ref.
//
// An action referenced by branch or tag can be rewritten by its owner at any
// time, which turns a supply-chain compromise into arbitrary code execution in
// the pipeline with no change on your side.
func checkActionPinning(j job, rel, jobName string, out *[]WorkflowFinding) {
	for i, s := range j.Steps {
		if s.Uses == "" {
			continue
		}
		uses := strings.TrimSpace(s.Uses)
		at := strings.LastIndex(uses, "@")
		if at < 0 {
			// A local action (./path) has no ref and needs no pinning.
			if strings.HasPrefix(uses, "./") {
				continue
			}
			*out = append(*out, WorkflowFinding{
				Rule:     "unpinned_action",
				Severity: severityHigh,
				Title:    "Action reference has no version",
				Detail: fmt.Sprintf(
					"%s job %q step %d uses %q with no @ref, which resolves to an implicit default branch.",
					rel, jobName, stepIndex(i, s), uses),
				File:           rel,
				Job:            jobName,
				Recommendation: "Pin the action to a release tag or, preferably, a full commit SHA.",
			})
			continue
		}

		ref := uses[at+1:]
		switch {
		case isCommitSHA(ref):
			// Immutable. Best practice.
		case ref == "":
			*out = append(*out, WorkflowFinding{
				Rule:     "unpinned_action",
				Severity: severityHigh,
				Title:    "Action pinned to an empty ref",
				Detail: fmt.Sprintf(
					"%s job %q step %d uses %q, which ends in @ with no version.",
					rel, jobName, stepIndex(i, s), uses),
				File:           rel,
				Job:            jobName,
				Recommendation: "Pin to a release tag or a full commit SHA.",
			})
		case strings.EqualFold(ref, "latest") || strings.EqualFold(ref, "main") ||
			strings.EqualFold(ref, "master"):
			*out = append(*out, WorkflowFinding{
				Rule:     "mutable_ref",
				Severity: severityHigh,
				Title:    "Action pinned to a moving ref",
				Detail: fmt.Sprintf(
					"%s job %q step %d uses %s@%s. That ref can be moved by the action's owner at any time.",
					rel, jobName, stepIndex(i, s), actionName(uses), ref),
				File: rel, Job: jobName,
				Recommendation: "Pin to a full commit SHA; Dependabot or Renovate will keep it current.",
			})
		case strings.HasPrefix(ref, "v") && strings.Contains(ref, "."):
			// A version tag: acceptable, though a SHA is stronger.
		default:
			*out = append(*out, WorkflowFinding{
				Rule:     "mutable_ref",
				Severity: severityMedium,
				Title:    "Action pinned to a non-SHA ref",
				Detail: fmt.Sprintf(
					"%s job %q step %d uses %s@%s, which is not an immutable commit SHA.",
					rel, jobName, stepIndex(i, s), actionName(uses), ref),
				File: rel, Job: jobName,
				Recommendation: "Pin to a full commit SHA for a supply-chain guarantee.",
			})
		}
	}
}

// checkMatrix flags redundant or excessive matrix expansion.
func checkMatrix(rel, jobName string, j job, out *[]WorkflowFinding) {
	raw, ok := j.Strategy["matrix"]
	if !ok || raw == nil {
		return
	}
	m, ok := raw.(map[string]any)
	if !ok {
		// `matrix: ${{ fromJSON(...) }}` cannot be evaluated statically.
		return
	}

	// An axis that never varies multiplies nothing but still suggests the
	// author expected it to, which usually means the real variants live in
	// `include`.
	for _, axis := range sortedKeys(m) {
		values := asList(m[axis])
		if len(values) == 1 {
			*out = append(*out, WorkflowFinding{
				Rule:     "constant_matrix_axis",
				Severity: severityLow,
				Title:    "Matrix axis has a single value",
				Detail: fmt.Sprintf(
					"%s job %q declares matrix.%s with one value (%v), so it multiplies nothing. This usually signals a leftover axis or a missing variant.",
					rel, jobName, axis, values),
				File: rel, Job: jobName,
				Recommendation: "Remove the axis, or add the variants you actually intended to build.",
			})
		}
	}

	// Total fan-out from the plain axes. `include` and `exclude` are applied by
	// GitHub in ways that are not statically predictable, so they are excluded
	// from the count rather than guessed at.
	fanout := 1
	for _, axis := range sortedKeys(m) {
		if axis == "include" || axis == "exclude" {
			continue
		}
		fanout *= len(asList(m[axis]))
	}
	if fanout > maxMatrixFanout {
		*out = append(*out, WorkflowFinding{
			Rule:     "excessive_matrix",
			Severity: severityMedium,
			Title:    "Matrix expands to a very large job count",
			Detail: fmt.Sprintf(
				"%s job %q expands to roughly %d jobs from its matrix axes. At that scale, runner minutes and wall-clock time dominate.",
				rel, jobName, fanout),
			File: rel, Job: jobName,
			Recommendation: "Split rarely-changed axes into a separate manually triggered workflow.",
		})
	}
}

// checkTimeouts flags jobs with no timeout.
//
// A hung job burns runner minutes until GitHub's own limit, which defaults to
// six hours.
func checkTimeouts(rel, jobName string, j job, out *[]WorkflowFinding) {
	if j.TimeoutMinutes != nil {
		return
	}
	for _, s := range j.Steps {
		if s.TimeoutMinutes != nil {
			return
		}
	}
	*out = append(*out, WorkflowFinding{
		Rule:     "missing_timeout",
		Severity: severityLow,
		Title:    "Job has no timeout",
		Detail: fmt.Sprintf(
			"%s job %q sets no timeout-minutes, so a hung step can run until the platform limit (six hours by default).",
			rel, jobName),
		File: rel, Job: jobName,
		Recommendation: "Add `timeout-minutes` to the job.",
	})
}

// checkTriggerSecurity flags the dangerous trigger combination.
func checkTriggerSecurity(wf *workflow, rel string, out *[]WorkflowFinding) {
	if !triggerIs(wf.On, "pull_request_target") {
		return
	}
	// pull_request_target runs with a privileged token while checking out PR
	// code. That is the setup for a repository compromise.
	for _, name := range sortedJobNames(wf.Jobs) {
		j := wf.Jobs[name]
		for i, s := range j.Steps {
			if normalizeUses(s.Uses) != "actions/checkout" {
				continue
			}
			if hasAnyKey(s.With, "ref", "repository") {
				*out = append(*out, WorkflowFinding{
					Rule:     "privileged_trigger_checkout",
					Severity: severityCritical,
					Title:    "pull_request_target checks out untrusted code",
					Detail: fmt.Sprintf(
						"%s job %q step %d combines pull_request_target (a privileged, write-capable token) with an explicit checkout ref. Anything the PR changes then runs with write access to the repository.",
						rel, name, stepIndex(i, s)),
					File: rel, Job: name,
					Recommendation: "Use pull_request, or split the privileged job so it never checks out PR head code.",
				})
			}
		}
	}
}

// triggerIs reports whether a workflow trigger list contains name.
//
// `on:` is parsed as the boolean true by YAML 1.1 resolvers, so the literal
// key can be either "on" or a boolean node. Both are accepted.
func triggerIs(node yaml.Node, name string) bool {
	if node.IsZero() {
		return false
	}
	n := &node
	if n.Kind == yaml.DocumentNode && len(n.Content) > 0 {
		n = n.Content[0]
	}
	switch n.Kind {
	case yaml.MappingNode:
		for i := 0; i+1 < len(n.Content); i += 2 {
			key := n.Content[i].Value
			// "on" may arrive as the string "on" or as the bool true.
			if key == name || (name == "on" && key == "true") {
				return true
			}
		}
	case yaml.SequenceNode:
		for _, item := range n.Content {
			if item.Value == name {
				return true
			}
		}
	case yaml.ScalarNode:
		return n.Value == name || (name == "on" && n.Value == "true")
	}
	return false
}

// isCommitSHA reports whether ref is a full 40-character commit SHA.
func isCommitSHA(ref string) bool {
	if len(ref) != 40 {
		return false
	}
	for i := 0; i < len(ref); i++ {
		c := ref[i]
		if (c >= '0' && c <= '9') || (c >= 'a' && c <= 'f') || (c >= 'A' && c <= 'F') {
			continue
		}
		return false
	}
	return true
}

// normalizeUses lowercases the owner/repo portion of an action reference so
// comparisons are case-insensitive, as GitHub treats them.
func normalizeUses(uses string) string {
	at := strings.Index(uses, "@")
	if at >= 0 {
		uses = uses[:at]
	}
	return strings.ToLower(strings.TrimSpace(uses))
}

// actionName returns the owner/repo portion of an action reference.
func actionName(uses string) string {
	at := strings.Index(uses, "@")
	if at >= 0 {
		return uses[:at]
	}
	return uses
}

// stepIndex renders a 1-based step number for human-facing messages.
func stepIndex(i int, s step) int { return i + 1 }

// asList coerces a matrix axis value to a list of values.
func asList(v any) []string {
	switch t := v.(type) {
	case []any:
		out := make([]string, 0, len(t))
		for _, item := range t {
			out = append(out, fmt.Sprint(item))
		}
		return out
	case nil:
		return nil
	default:
		return []string{fmt.Sprint(t)}
	}
}

// hasAnyKey reports whether a `with` block declares any of keys.
//
// Used for checkout, where a `ref` or `repository` overrides what is checked
// out and therefore where the code comes from.
func hasAnyKey(with map[string]any, keys ...string) bool {
	if with == nil {
		return false
	}
	for _, k := range keys {
		if _, ok := with[k]; ok {
			return true
		}
	}
	return false
}

// sortedKeys returns map keys in sorted order for deterministic iteration.
func sortedKeys(m map[string]any) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// sortedJobNames returns job names in sorted order.
func sortedJobNames(jobs map[string]job) []string {
	out := make([]string, 0, len(jobs))
	for k := range jobs {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// sortFindings orders by severity desc, then rule, then file, then job, so the
// report is total and stable.
func sortFindings(f []WorkflowFinding) {
	sort.SliceStable(f, func(i, j int) bool {
		a, b := f[i], f[j]
		if a.Severity.rank() != b.Severity.rank() {
			return a.Severity.rank() > b.Severity.rank()
		}
		if a.Rule != b.Rule {
			return a.Rule < b.Rule
		}
		if a.File != b.File {
			return a.File < b.File
		}
		if a.Job != b.Job {
			return a.Job < b.Job
		}
		return a.Detail < b.Detail
	})
}
