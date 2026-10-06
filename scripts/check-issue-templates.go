//go:build ignore

// Validates the GitHub issue forms against the schema GitHub actually enforces.
//
// YAML syntax alone is not sufficient. An issue form can parse perfectly and
// still fail to render, because GitHub rejects a form whose keys are misplaced,
// whose `id` is not a valid anchor-safe token, or whose `validations` are not a
// sibling of `attributes`. When that happens the repository shows a blank
// template and contributors cannot file a report at all, which is worse than no
// template because the maintainers believe reports are arriving.
//
// This checks the constraints that are cheap to enforce and expensive to get
// wrong:
//
//   - YAML parses, and `body` is a list of field maps
//
//   - every field has a known `type`
//
//   - `attributes` and `validations` are siblings, at the same level
//
//   - `id` matches [a-zA-Z0-9_-]+ so it can be used as an anchor
//
//   - `required: true` only appears on field types that support it
//
//   - dropdown and checkbox `options` are non-empty
//
//   - the top-level keys are the ones GitHub recognises
//
//     go run scripts/check-issue-templates.go
package main

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"
)

// fieldTypes are the element types GitHub accepts in an issue form body.
var fieldTypes = map[string]bool{
	"markdown":   true,
	"input":      true,
	"textarea":   true,
	"dropdown":   true,
	"checkboxes": true,
}

// supportsRequired lists the field types for which `validations.required` is
// meaningful. A markdown block has no value to require, and GitHub rejects the
// form rather than ignoring it.
var supportsRequired = map[string]bool{
	"input":    true,
	"textarea": true,
	"dropdown": true,
}

// topLevelKeys are the keys GitHub reads from a form file. Anything else is
// either a typo or an unsupported extension.
var topLevelKeys = map[string]bool{
	"name":        true,
	"description": true,
	"title":       true,
	"labels":      true,
	"assignees":   true,
	"body":        true,
}

var idRe = regexp.MustCompile(`^[a-zA-Z0-9_-]+$`)

type form struct {
	Name        string `yaml:"name"`
	Description string `yaml:"description"`
	Title       string `yaml:"title"`
	Body        []map[string]any
}

func main() {
	dir := ".github/ISSUE_TEMPLATE"
	entries, err := os.ReadDir(dir)
	if err != nil {
		fmt.Fprintf(os.Stderr, "cannot read %s: %v\n", dir, err)
		os.Exit(1)
	}

	problems := 0
	checked := 0

	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".yml") {
			continue
		}
		if e.Name() == "config.yml" {
			continue // validated separately; it has no body
		}
		path := filepath.Join(dir, e.Name())
		if errs := checkForm(path); len(errs) > 0 {
			fmt.Printf("FAIL %s\n", e.Name())
			for _, m := range errs {
				fmt.Printf("       %s\n", m)
			}
			problems += len(errs)
		} else {
			fmt.Printf("ok   %s\n", e.Name())
		}
		checked++
	}

	if errs := checkConfig(filepath.Join(dir, "config.yml")); len(errs) > 0 {
		fmt.Println("FAIL config.yml")
		for _, m := range errs {
			fmt.Printf("       %s\n", m)
		}
		problems += len(errs)
	} else {
		fmt.Println("ok   config.yml")
	}
	checked++

	fmt.Printf("\n%d file(s) checked, %d problem(s)\n", checked, problems)
	if problems > 0 {
		os.Exit(1)
	}
}

func checkForm(path string) []string {
	data, err := os.ReadFile(path)
	if err != nil {
		return []string{err.Error()}
	}

	var raw struct {
		Name        string           `yaml:"name"`
		Description string           `yaml:"description"`
		Title       string           `yaml:"title"`
		Body        []map[string]any `yaml:"body"`
	}
	if err := yaml.Unmarshal(data, &raw); err != nil {
		return []string{fmt.Sprintf("does not parse as YAML: %v", err)}
	}

	var errs []string

	if raw.Name == "" {
		errs = append(errs, "missing `name`")
	}
	if raw.Description == "" {
		errs = append(errs, "missing `description`")
	}
	if len(raw.Body) == 0 {
		errs = append(errs, "missing `body`; a form with no fields renders blank")
		return errs
	}

	seenIDs := map[string]bool{}

	for i, field := range raw.Body {
		where := fmt.Sprintf("body[%d]", i)

		typ, ok := field["type"].(string)
		if !ok {
			errs = append(errs, fmt.Sprintf("%s has no `type`", where))
			continue
		}
		if !fieldTypes[typ] {
			errs = append(errs, fmt.Sprintf("%s has unknown type %q (want one of %s)",
				where, typ, keys(fieldTypes)))
			continue
		}

		attrs, hasAttrs := field["attributes"]
		if typ != "markdown" && !hasAttrs {
			errs = append(errs, fmt.Sprintf("%s of type %q has no `attributes`", where, typ))
		}

		if id, ok := field["id"].(string); ok {
			if !idRe.MatchString(id) {
				errs = append(errs, fmt.Sprintf("%s has id %q, which is not usable as an anchor", where, id))
			}
			if seenIDs[id] {
				errs = append(errs, fmt.Sprintf("%s repeats id %q", where, id))
			}
			seenIDs[id] = true
		} else if typ != "markdown" {
			errs = append(errs, fmt.Sprintf("%s of type %q has no `id`", where, typ))
		}

		// `validations` must be a sibling of `attributes`, not nested inside it.
		if v, ok := field["validations"]; ok {
			if _, isMap := v.(map[string]any); !isMap {
				errs = append(errs, fmt.Sprintf("%s has `validations` that is not a mapping", where))
			}
			if _, supported := supportsRequired[typ]; !supported {
				errs = append(errs, fmt.Sprintf("%s is type %q, which does not support `validations`", where, typ))
			}
		}

		for k := range field {
			switch k {
			case "type", "id", "attributes", "validations":
			default:
				errs = append(errs, fmt.Sprintf("%s has unsupported key %q", where, k))
			}
		}

		// Options must exist and be non-empty for the choice field types.
		//
		// The two types take different option shapes. A dropdown option is a
		// bare string, because there is nothing to pair it with: selecting it
		// yields the string itself. A checkbox option must be a `label: value`
		// mapping, because selecting it has to record which label was ticked.
		// Treating them the same produces twenty false failures on correct
		// forms, which is how a validator like this gets ignored.
		switch typ {
		case "dropdown":
			if len(optionsOf(attrs)) == 0 {
				errs = append(errs, fmt.Sprintf("%s is a dropdown with no `options`", where))
			}
		case "checkboxes":
			opts := optionsOf(attrs)
			if len(opts) == 0 {
				errs = append(errs, fmt.Sprintf("%s is checkboxes with no `options`", where))
			}
			for j, o := range opts {
				if !strings.Contains(o, ":") {
					errs = append(errs, fmt.Sprintf(
						"%s options[%d] %q must be a `label: value` mapping", where, j, o))
				}
			}
		}

		if typ == "input" || typ == "textarea" || typ == "dropdown" {
			if !hasLabel(attrs) {
				errs = append(errs, fmt.Sprintf("%s of type %q has no `label`", where, typ))
			}
		}
	}

	return errs
}

// optionsOf digs `options` out of whatever shape attributes decoded to.
func optionsOf(attrs any) []string {
	m, ok := attrs.(map[string]any)
	if !ok {
		return nil
	}
	list, ok := m["options"].([]any)
	if !ok {
		return nil
	}
	out := make([]string, 0, len(list))
	for _, v := range list {
		out = append(out, fmt.Sprint(v))
	}
	return out
}

func hasLabel(attrs any) bool {
	m, ok := attrs.(map[string]any)
	if !ok {
		return false
	}
	s, ok := m["label"].(string)
	return ok && strings.TrimSpace(s) != ""
}

func checkConfig(path string) []string {
	data, err := os.ReadFile(path)
	if err != nil {
		return []string{err.Error()}
	}

	var raw map[string]any
	if err := yaml.Unmarshal(data, &raw); err != nil {
		return []string{fmt.Sprintf("does not parse as YAML: %v", err)}
	}

	var errs []string
	for k := range raw {
		switch k {
		case "blank_issues_enabled", "contact_links", "description", "name":
		default:
			errs = append(errs, fmt.Sprintf("unsupported top-level key %q", k))
		}
	}

	if v, ok := raw["blank_issues_enabled"]; ok {
		if _, isBool := v.(bool); !isBool {
			errs = append(errs, "`blank_issues_enabled` must be a boolean")
		}
	}

	links, ok := raw["contact_links"].([]any)
	if !ok || len(links) == 0 {
		errs = append(errs, "no `contact_links`, so there is nowhere to send "+
			"a report that does not belong in an issue")
	}
	for _, l := range links {
		m, ok := l.(map[string]any)
		if !ok {
			errs = append(errs, "a contact_links entry is not a mapping")
			continue
		}
		for _, k := range []string{"name", "url", "about"} {
			s, _ := m[k].(string)
			if strings.TrimSpace(s) == "" {
				errs = append(errs, fmt.Sprintf("a contact_links entry is missing %q", k))
			}
		}
	}

	// The links must resolve to files in this repository, or to a URL. A dead
	// internal link sends a contributor to a 404 at the moment they are trying
	// to do the right thing.
	for _, l := range links {
		m, _ := l.(map[string]any)
		u, _ := m["url"].(string)
		if strings.HasPrefix(u, "http://") || strings.HasPrefix(u, "https://") {
			continue
		}
		if _, err := os.Stat(filepath.Join("..", filepath.FromSlash(u))); err != nil {
			if _, err := os.Stat(filepath.FromSlash(u)); err != nil {
				errs = append(errs, fmt.Sprintf("contact_link url %q does not exist", u))
			}
		}
	}

	return errs
}

func keys(m map[string]bool) string {
	var out []string
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return strings.Join(out, ", ")
}
