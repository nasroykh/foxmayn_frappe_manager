// Package project reads ffm.yaml, the file an app repository commits to
// describe the development bench it needs, and keeps the record of which
// files' commands the user trusts.
//
// The declarative fields (Frappe branch, apps, database, toolchain) are used
// as they are. Hooks and tooling commands run inside the bench, so they run
// only from a file whose exact content the user trusted (see Trust).
package project

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strings"

	"go.yaml.in/yaml/v3"

	"github.com/nasroykh/foxmayn_frappe_manager/internal/bench"
)

// FileName is the project file ffm looks for.
const FileName = "ffm.yaml"

// File is a parsed ffm.yaml.
type File struct {
	// Version is the file format; only 1 exists.
	Version int `yaml:"version"`
	// Name is the bench name; the project directory's name when empty.
	Name   string `yaml:"name"`
	Frappe struct {
		Branch string `yaml:"branch"`
		Repo   string `yaml:"repo"`
	} `yaml:"frappe"`
	Python string `yaml:"python"`
	Node   string `yaml:"node"`
	// DB is mariadb (default) or postgres.
	DB string `yaml:"db"`
	// Apps are installed as ffm create --apps takes them: erpnext,
	// hrms@version-16, a git URL[@branch].
	Apps  []string `yaml:"apps"`
	Hooks Hooks    `yaml:"hooks"`
	// Tooling are named commands for 'ffm run <name>'.
	Tooling map[string]Tool `yaml:"tooling"`

	// Path is the absolute path the file was read from; Raw its content.
	Path string `yaml:"-"`
	Raw  []byte `yaml:"-"`
}

// Hooks run inside the bench's frappe container, in /workspace/frappe-bench,
// one shell command each, in order.
type Hooks struct {
	// PostCreate runs once, after ffm up created the bench.
	PostCreate []string `yaml:"post_create"`
	// PostUpdate runs after a successful ffm app update.
	PostUpdate []string `yaml:"post_update"`
}

// Tool is one 'ffm run' command. In the file it is either a string (the
// command) or a map with cmd and description.
type Tool struct {
	Cmd         string `yaml:"cmd"`
	Description string `yaml:"description"`
}

func (t *Tool) UnmarshalYAML(n *yaml.Node) error {
	if n.Kind == yaml.ScalarNode {
		return n.Decode(&t.Cmd)
	}
	// node.Decode does not inherit the decoder's KnownFields, so unknown
	// keys are checked here.
	type plain Tool
	var p plain
	if err := n.Decode(&p); err != nil {
		return err
	}
	for i := 0; i+1 < len(n.Content); i += 2 {
		if k := n.Content[i].Value; k != "cmd" && k != "description" {
			return fmt.Errorf("line %d: unknown tooling field %q (cmd, description)", n.Content[i].Line, k)
		}
	}
	*t = Tool(p)
	return nil
}

var (
	toolNameRe = regexp.MustCompile(`^[a-z][a-z0-9_-]{0,31}$`)
	branchRe   = regexp.MustCompile(`^[A-Za-z0-9._/-]+$`)
)

// Find looks for ffm.yaml in dir and its parents, stopping at the first
// directory that holds .git (the repository root) or at the filesystem root.
func Find(dir string) (string, error) {
	dir, err := filepath.Abs(dir)
	if err != nil {
		return "", err
	}
	for {
		p := filepath.Join(dir, FileName)
		if st, err := os.Stat(p); err == nil && st.Mode().IsRegular() {
			return p, nil
		}
		if _, err := os.Stat(filepath.Join(dir, ".git")); err == nil {
			break
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			break
		}
		dir = parent
	}
	return "", fmt.Errorf("no %s in this directory or its parents (up to the repository root)", FileName)
}

// maxFileSize caps ffm.yaml; a description file is small.
const maxFileSize = 256 << 10

// Load reads and validates the file at path.
func Load(path string) (File, error) {
	abs, err := filepath.Abs(path)
	if err != nil {
		return File{}, err
	}
	raw, err := os.ReadFile(abs)
	if err != nil {
		return File{}, err
	}
	if len(raw) > maxFileSize {
		return File{}, fmt.Errorf("%s is larger than %d KiB", abs, maxFileSize>>10)
	}
	f, err := Parse(raw)
	if err != nil {
		return File{}, fmt.Errorf("%s: %w", abs, err)
	}
	f.Path, f.Raw = abs, raw
	if f.Name == "" {
		f.Name = NameFromDir(filepath.Dir(abs))
	}
	if err := bench.ValidateNewName(f.Name); err != nil {
		return File{}, fmt.Errorf("%s: %w (set name: in the file)", abs, err)
	}
	return f, nil
}

// Parse decodes and validates ffm.yaml content. Unknown fields are errors,
// so a typo does not silently fall back to a default.
func Parse(raw []byte) (File, error) {
	var f File
	dec := yaml.NewDecoder(bytes.NewReader(raw))
	dec.KnownFields(true)
	if err := dec.Decode(&f); err != nil {
		if errors.Is(err, io.EOF) {
			return f, fmt.Errorf("the file is empty")
		}
		return f, err
	}
	return f, f.validate()
}

func (f File) validate() error {
	if f.Version != 1 {
		return fmt.Errorf("version: %d is not supported (this ffm reads version: 1)", f.Version)
	}
	if f.Frappe.Branch != "" && !branchRe.MatchString(f.Frappe.Branch) {
		return fmt.Errorf("frappe.branch: %q is not a branch name", f.Frappe.Branch)
	}
	if f.Frappe.Repo != "" && !strings.HasPrefix(f.Frappe.Repo, "https://") && !strings.HasPrefix(f.Frappe.Repo, "git@") {
		return fmt.Errorf("frappe.repo: %q must be an https:// or git@ URL", f.Frappe.Repo)
	}
	if f.Python != "" && !slices.Contains(bench.ImagePythons, f.Python) {
		return fmt.Errorf("python: %q: the bench image ships %s", f.Python, strings.Join(bench.ImagePythons, " and "))
	}
	switch f.DB {
	case "", "mariadb", "postgres":
	default:
		return fmt.Errorf("db: %q: use mariadb or postgres", f.DB)
	}
	for _, a := range f.Apps {
		if strings.TrimSpace(a) == "" || strings.ContainsAny(a, " \t\n'\"`$;|&") {
			return fmt.Errorf("apps: %q is not an app spec (erpnext, hrms@version-16 or a git URL)", a)
		}
	}
	for name, t := range f.Tooling {
		if !toolNameRe.MatchString(name) {
			return fmt.Errorf("tooling: %q: names are lowercase letters, digits, - and _ (at most 32)", name)
		}
		if strings.TrimSpace(t.Cmd) == "" {
			return fmt.Errorf("tooling.%s: cmd is empty", name)
		}
	}
	for _, h := range append(append([]string{}, f.Hooks.PostCreate...), f.Hooks.PostUpdate...) {
		if strings.TrimSpace(h) == "" {
			return fmt.Errorf("hooks: an empty command")
		}
	}
	return nil
}

// HasCommands reports whether the file runs anything: hooks or tooling.
func (f File) HasCommands() bool {
	return len(f.Hooks.PostCreate)+len(f.Hooks.PostUpdate)+len(f.Tooling) > 0
}

// Commands lists every command the file can run, for the trust prompt.
func (f File) Commands() []string {
	var out []string
	for _, h := range f.Hooks.PostCreate {
		out = append(out, "post_create: "+h)
	}
	for _, h := range f.Hooks.PostUpdate {
		out = append(out, "post_update: "+h)
	}
	for _, name := range f.ToolNames() {
		out = append(out, "ffm run "+name+": "+f.Tooling[name].Cmd)
	}
	return out
}

// ToolNames are the tooling names, sorted.
func (f File) ToolNames() []string {
	names := make([]string, 0, len(f.Tooling))
	for n := range f.Tooling {
		names = append(names, n)
	}
	sort.Strings(names)
	return names
}

// Hash identifies the file's exact content for the trust record.
func (f File) Hash() string {
	sum := sha256.Sum256(f.Raw)
	return hex.EncodeToString(sum[:])
}

var nameCleanRe = regexp.MustCompile(`[^a-z0-9-]+`)

// NameFromDir turns a directory name into a bench name: lowercase, runs of
// other characters as one hyphen.
func NameFromDir(dir string) string {
	n := nameCleanRe.ReplaceAllString(strings.ToLower(filepath.Base(dir)), "-")
	return strings.Trim(n, "-")
}
