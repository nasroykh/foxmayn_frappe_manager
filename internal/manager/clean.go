package manager

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"

	"github.com/nasroykh/foxmayn_frappe_manager/internal/config"
	"github.com/nasroykh/foxmayn_frappe_manager/internal/execx"
)

// CleanInput selects what ffm clean removes.
type CleanInput struct {
	// BuildCache also prunes Docker's build cache. It is shared with every
	// other project on the host.
	BuildCache bool
	// Dangling also removes untagged images, from any project.
	Dangling bool
}

// CleanItem is one thing ffm clean found.
type CleanItem struct {
	Kind  string // "volume", "image", "build cache", "dangling images"
	Name  string
	Bench string
	Size  string
}

// composeProjectLabel names the compose project of a volume or image.
const composeProjectLabel = "com.docker.compose.project"

// CleanPlan lists what Clean would remove: volumes and images of ffm benches
// that no longer exist, plus the opt-in shared caches.
//
// A project counts as orphaned only when no bench record has it, its bench
// directory is gone and its bench lock is free. A bench being created has a
// directory and holds its lock but has no record yet, so it is never touched.
func (s *Service) CleanPlan(in CleanInput) ([]CleanItem, error) {
	benches, err := s.LoadBenches()
	if err != nil {
		return nil, err
	}
	known := map[string]bool{}
	for _, b := range benches {
		known[strings.ToLower(b.Name)] = true
	}
	orphan := func(project string) (string, bool) {
		name, ok := strings.CutPrefix(project, "ffm-")
		if !ok || name == "" || name == "proxy" || known[name] {
			return "", false
		}
		if _, err := os.Stat(config.BenchDir(name)); err == nil {
			return "", false
		}
		release, err := s.lockBench(name)
		if err != nil {
			return "", false
		}
		release()
		return name, true
	}

	var items []CleanItem
	vols, err := dockerVolumes()
	if err != nil {
		return nil, err
	}
	for _, v := range vols {
		if name, ok := orphan(labelValue(v.Labels, composeProjectLabel)); ok {
			items = append(items, CleanItem{Kind: "volume", Name: v.Name, Bench: name, Size: v.Size})
		}
	}
	imgs, err := dockerImages()
	if err != nil {
		return nil, err
	}
	for _, im := range imgs {
		if name, ok := orphan(im.Project); ok {
			items = append(items, CleanItem{Kind: "image", Name: im.Ref, Bench: name, Size: im.Size})
		}
	}
	if in.BuildCache {
		items = append(items, CleanItem{Kind: "build cache", Name: "docker builder prune", Size: dockerOut("builder", "du", "--format", "{{.Size}}")})
	}
	if in.Dangling {
		n := len(strings.Fields(dockerOut("image", "ls", "-q", "-f", "dangling=true")))
		items = append(items, CleanItem{Kind: "dangling images", Name: fmt.Sprintf("%d untagged image(s)", n)})
	}
	return items, nil
}

// Clean removes what CleanPlan listed. Failures are reported and the rest
// still go.
func (s *Service) Clean(items []CleanItem, pw ProgressWriter) error {
	if pw == nil {
		pw = CLIProgress{}
	}
	failed := 0
	for _, it := range items {
		var args []string
		switch it.Kind {
		case "volume":
			args = []string{"volume", "rm", it.Name}
		case "image":
			args = []string{"image", "rm", it.Name}
		case "build cache":
			args = []string{"builder", "prune", "-f"}
		case "dangling images":
			args = []string{"image", "prune", "-f"}
		default:
			continue
		}
		if out, err := execx.Command("docker", args...).CombinedOutput(); err != nil {
			pw.Printf("  could not remove %s %s: %s\n", it.Kind, it.Name, strings.TrimSpace(string(out)))
			failed++
			continue
		}
		pw.Printf("  removed %s %s\n", it.Kind, it.Name)
	}
	if failed > 0 {
		return fmt.Errorf("%d item(s) could not be removed", failed)
	}
	return nil
}

func dockerOut(args ...string) string {
	out, err := execx.Command("docker", args...).Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

type dockerVolume struct {
	Name   string `json:"Name"`
	Labels string `json:"Labels"`
	Size   string `json:"Size"`
}

// dockerVolumes lists ffm volumes with their sizes (`docker system df -v`
// is the one listing that has them), falling back to names without sizes.
func dockerVolumes() ([]dockerVolume, error) {
	var vols []dockerVolume
	if out, err := execx.Command("docker", "system", "df", "-v", "--format", "{{json .Volumes}}").Output(); err == nil {
		if json.Unmarshal(out, &vols) == nil {
			return vols, nil
		}
	}
	out, err := execx.Command("docker", "volume", "ls", "--format", "{{json .}}").Output()
	if err != nil {
		return nil, fmt.Errorf("docker volume ls: %w", err)
	}
	vols = vols[:0]
	for _, l := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		var v dockerVolume
		if json.Unmarshal([]byte(l), &v) == nil {
			vols = append(vols, v)
		}
	}
	return vols, nil
}

type dockerImage struct {
	Ref, Size, Project string
}

// dockerImages lists images built for ffm compose projects.
func dockerImages() ([]dockerImage, error) {
	out, err := execx.Command("docker", "image", "ls", "--filter", "label="+composeProjectLabel,
		"--format", `{{.Repository}}:{{.Tag}}`+"\t"+`{{.Size}}`+"\t"+`{{.ID}}`).Output()
	if err != nil {
		return nil, fmt.Errorf("docker image ls: %w", err)
	}
	var imgs []dockerImage
	for _, l := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		f := strings.Split(l, "\t")
		if len(f) != 3 || !strings.HasPrefix(f[0], "ffm-") {
			continue
		}
		project := dockerOut("image", "inspect", "--format", `{{index .Config.Labels "`+composeProjectLabel+`"}}`, f[2])
		imgs = append(imgs, dockerImage{Ref: f[0], Size: f[1], Project: project})
	}
	return imgs, nil
}

// labelValue reads one key from Docker's "k=v,k=v" label string.
func labelValue(labels, key string) string {
	for _, kv := range strings.Split(labels, ",") {
		if v, ok := strings.CutPrefix(kv, key+"="); ok {
			return v
		}
	}
	return ""
}
