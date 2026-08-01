// Package marketplace validates immutable behavioral skill packages without
// executing marketplace content.
package marketplace

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

const (
	maxSkillFiles = 256
	maxSkillBytes = 4 << 20
)

type Catalog struct {
	ID      string       `json:"id"`
	Version string       `json:"version"`
	Skills  []SkillEntry `json:"skills"`
}

type SkillEntry struct {
	Name          string   `json:"name"`
	Path          string   `json:"path"`
	RequiresTools []string `json:"requires_tools,omitempty"`
}

type SkillSnapshot struct {
	MarketplaceID string   `json:"marketplace_id"`
	Version       string   `json:"version"`
	Name          string   `json:"name"`
	Root          string   `json:"root"`
	Files         []string `json:"files"`
	SharedRoot    string   `json:"shared_root,omitempty"`
	SharedFiles   []string `json:"shared_files,omitempty"`
	SharedHash    string   `json:"shared_hash,omitempty"`
	RequiresTools []string `json:"requires_tools,omitempty"`
	Hash          string   `json:"hash"`
}

var ErrUnsafeMarketplace = errors.New("unsafe marketplace content")

func Load(root, catalogPath string) (Catalog, []SkillSnapshot, error) {
	root, err := filepath.Abs(root)
	if err != nil {
		return Catalog{}, nil, err
	}
	if _, err := containedPath(root, catalogPath); err != nil {
		return Catalog{}, nil, err
	}
	data, err := readRootFile(root, catalogPath, 1<<20)
	if err != nil {
		return Catalog{}, nil, err
	}
	decoder := json.NewDecoder(strings.NewReader(string(data)))
	decoder.DisallowUnknownFields()
	var catalog Catalog
	if err := decoder.Decode(&catalog); err != nil {
		return Catalog{}, nil, fmt.Errorf("decode catalog: %w", err)
	}
	if catalog.ID == "" || catalog.Version == "" || len(catalog.Skills) == 0 {
		return Catalog{}, nil, fmt.Errorf("catalog ID, version, and skills are required")
	}
	seen := make(map[string]struct{})
	snapshots := make([]SkillSnapshot, 0, len(catalog.Skills))
	for _, entry := range catalog.Skills {
		if entry.Name == "" || entry.Path == "" {
			return Catalog{}, nil, fmt.Errorf("skill name and path are required")
		}
		if _, duplicate := seen[entry.Name]; duplicate {
			return Catalog{}, nil, fmt.Errorf("duplicate skill %q", entry.Name)
		}
		seen[entry.Name] = struct{}{}
		snapshot, err := snapshotSkill(root, catalog, entry)
		if err != nil {
			return Catalog{}, nil, fmt.Errorf("skill %s: %w", entry.Name, err)
		}
		snapshots = append(snapshots, snapshot)
	}
	sort.Slice(snapshots, func(i, j int) bool { return snapshots[i].Name < snapshots[j].Name })
	return catalog, snapshots, nil
}

func snapshotSkill(root string, catalog Catalog, entry SkillEntry) (SkillSnapshot, error) {
	skillRoot, err := containedPath(root, entry.Path)
	if err != nil {
		return SkillSnapshot{}, err
	}
	info, err := os.Lstat(skillRoot)
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return SkillSnapshot{}, fmt.Errorf("%w: skill root must be a real directory", ErrUnsafeMarketplace)
	}
	if _, err := os.Stat(filepath.Join(skillRoot, "SKILL.md")); err != nil {
		return SkillSnapshot{}, fmt.Errorf("SKILL.md is required")
	}
	var files []string
	var total int64
	hash := sha256.New()
	err = filepath.WalkDir(skillRoot, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("%w: symlink %s", ErrUnsafeMarketplace, path)
		}
		if entry.IsDir() {
			if entry.Name() == "scripts" || entry.Name() == "assets" {
				return fs.SkipDir
			}
			if entry.Name() == "hooks" || entry.Name() == "plugins" {
				return fmt.Errorf("%w: executable extension directory %s", ErrUnsafeMarketplace, entry.Name())
			}
			return nil
		}
		if !info.Mode().IsRegular() || info.Mode().Perm()&0o111 != 0 {
			return fmt.Errorf("%w: non-regular or executable file %s", ErrUnsafeMarketplace, path)
		}
		ext := strings.ToLower(filepath.Ext(path))
		if ext != ".md" && ext != ".txt" && ext != ".json" && ext != ".yaml" && ext != ".yml" {
			return fmt.Errorf("%w: unsupported behavioral file %s", ErrUnsafeMarketplace, path)
		}
		relative, _ := filepath.Rel(skillRoot, path)
		files = append(files, filepath.ToSlash(relative))
		total += info.Size()
		if len(files) > maxSkillFiles || total > maxSkillBytes {
			return fmt.Errorf("%w: skill exceeds size limits", ErrUnsafeMarketplace)
		}
		return nil
	})
	if err != nil {
		return SkillSnapshot{}, err
	}
	sort.Strings(files)
	for _, relative := range files {
		data, err := readRootFile(skillRoot, filepath.FromSlash(relative), maxSkillBytes)
		if err != nil {
			return SkillSnapshot{}, err
		}
		_, _ = hash.Write([]byte(relative))
		_, _ = hash.Write([]byte{0})
		_, _ = hash.Write(data)
		_, _ = hash.Write([]byte{0})
	}
	return SkillSnapshot{MarketplaceID: catalog.ID, Version: catalog.Version, Name: entry.Name, Root: skillRoot, Files: files, RequiresTools: append([]string(nil), entry.RequiresTools...), Hash: "sha256:" + hex.EncodeToString(hash.Sum(nil))}, nil
}

type pluginMarketplace struct {
	Name    string `json:"name"`
	Plugins []struct {
		Name    string `json:"name"`
		Source  string `json:"source"`
		Version string `json:"version"`
	} `json:"plugins"`
}

func LoadPluginMarketplace(root, catalogPath string) ([]SkillSnapshot, error) {
	return loadPluginMarketplace(root, catalogPath, "")
}

// LoadPlugin loads every behavioral skill from exactly one named plugin. The
// plugin may intentionally contain no skills, which supports an empty base
// plugin without weakening fail-closed source and manifest validation.
func LoadPlugin(root, catalogPath, pluginName string) ([]SkillSnapshot, error) {
	if strings.TrimSpace(pluginName) == "" {
		return nil, fmt.Errorf("plugin name is required")
	}
	return loadPluginMarketplace(root, catalogPath, pluginName)
}

func loadPluginMarketplace(root, catalogPath, selectedPlugin string) ([]SkillSnapshot, error) {
	absolute, err := filepath.Abs(root)
	if err != nil {
		return nil, err
	}
	data, err := readRootFile(absolute, catalogPath, 1<<20)
	if err != nil {
		return nil, err
	}
	var catalog pluginMarketplace
	decoder := json.NewDecoder(strings.NewReader(string(data)))
	if err := decoder.Decode(&catalog); err != nil {
		return nil, err
	}
	if catalog.Name == "" || len(catalog.Plugins) == 0 {
		return nil, fmt.Errorf("plugin marketplace name and plugins are required")
	}
	var snapshots []SkillSnapshot
	foundSelected := selectedPlugin == ""
	for _, plugin := range catalog.Plugins {
		if plugin.Name == "" || plugin.Source == "" {
			return nil, fmt.Errorf("plugin name and source are required")
		}
		if selectedPlugin != "" && plugin.Name != selectedPlugin {
			continue
		}
		foundSelected = true
		pluginRoot, err := containedPath(absolute, plugin.Source)
		if err != nil {
			return nil, err
		}
		skillsRoot := filepath.Join(pluginRoot, "skills")
		entries, err := os.ReadDir(skillsRoot)
		if err != nil {
			return nil, err
		}
		sharedFiles, sharedHash, err := snapshotSharedReferences(skillsRoot)
		if err != nil {
			return nil, fmt.Errorf("plugin %s shared references: %w", plugin.Name, err)
		}
		version := plugin.Version
		if version == "" {
			version = "unversioned"
		}
		for _, entry := range entries {
			if !entry.IsDir() || strings.HasPrefix(entry.Name(), ".") {
				continue
			}
			relative, err := filepath.Rel(absolute, filepath.Join(skillsRoot, entry.Name()))
			if err != nil {
				return nil, err
			}
			// Codex discovers skills at .agents/skills/<skill>/SKILL.md, so
			// retain the plugin in MarketplaceID and keep the skill's runtime
			// name flat. Combining plugins then fails closed on name collisions.
			snapshot, err := snapshotSkill(absolute, Catalog{ID: catalog.Name + "/" + plugin.Name, Version: version}, SkillEntry{Name: entry.Name(), Path: relative})
			if err != nil {
				return nil, fmt.Errorf("plugin %s skill %s: %w", plugin.Name, entry.Name(), err)
			}
			if len(sharedFiles) > 0 {
				snapshot.SharedRoot = skillsRoot
				snapshot.SharedFiles = append([]string(nil), sharedFiles...)
				snapshot.SharedHash = sharedHash
			}
			snapshots = append(snapshots, snapshot)
		}
	}
	if !foundSelected {
		return nil, fmt.Errorf("plugin %q is not present in marketplace %q", selectedPlugin, catalog.Name)
	}
	sort.Slice(snapshots, func(i, j int) bool {
		if snapshots[i].MarketplaceID == snapshots[j].MarketplaceID {
			return snapshots[i].Name < snapshots[j].Name
		}
		return snapshots[i].MarketplaceID < snapshots[j].MarketplaceID
	})
	return snapshots, nil
}

func snapshotSharedReferences(skillsRoot string) ([]string, string, error) {
	referencesRoot := filepath.Join(skillsRoot, ".references")
	info, err := os.Lstat(referencesRoot)
	if errors.Is(err, os.ErrNotExist) {
		return nil, "", nil
	}
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return nil, "", fmt.Errorf("%w: shared references must be a real directory", ErrUnsafeMarketplace)
	}
	var files []string
	var total int64
	err = filepath.WalkDir(referencesRoot, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("%w: shared reference symlink %s", ErrUnsafeMarketplace, path)
		}
		if entry.IsDir() {
			return nil
		}
		if !info.Mode().IsRegular() || info.Mode().Perm()&0o111 != 0 {
			return fmt.Errorf("%w: executable shared reference %s", ErrUnsafeMarketplace, path)
		}
		ext := strings.ToLower(filepath.Ext(path))
		if ext != ".md" && ext != ".txt" && ext != ".json" && ext != ".yaml" && ext != ".yml" {
			return fmt.Errorf("%w: unsupported shared reference %s", ErrUnsafeMarketplace, path)
		}
		relative, _ := filepath.Rel(skillsRoot, path)
		files = append(files, filepath.ToSlash(relative))
		total += info.Size()
		if len(files) > maxSkillFiles || total > maxSkillBytes {
			return fmt.Errorf("%w: shared references exceed size limits", ErrUnsafeMarketplace)
		}
		return nil
	})
	if err != nil {
		return nil, "", err
	}
	sort.Strings(files)
	hash := sha256.New()
	for _, relative := range files {
		data, err := readRootFile(skillsRoot, filepath.FromSlash(relative), maxSkillBytes)
		if err != nil {
			return nil, "", err
		}
		_, _ = hash.Write([]byte(relative))
		_, _ = hash.Write([]byte{0})
		_, _ = hash.Write(data)
		_, _ = hash.Write([]byte{0})
	}
	return files, "sha256:" + hex.EncodeToString(hash.Sum(nil)), nil
}

type Registry struct{ snapshots []SkillSnapshot }

func NewRegistry(snapshots []SkillSnapshot) (*Registry, error) {
	resolved, err := Resolve(snapshots, map[string]bool{})
	if err != nil {
		return nil, err
	}
	result := make([]SkillSnapshot, 0, len(resolved))
	for _, snapshot := range resolved {
		result = append(result, snapshot)
	}
	sort.Slice(result, func(i, j int) bool { return result[i].Name < result[j].Name })
	return &Registry{snapshots: result}, nil
}
func (r *Registry) List() []SkillSnapshot { return append([]SkillSnapshot(nil), r.snapshots...) }

func readRootFile(root, relative string, limit int64) ([]byte, error) {
	scoped, err := os.OpenRoot(root)
	if err != nil {
		return nil, err
	}
	defer scoped.Close()
	file, err := scoped.Open(relative)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	return io.ReadAll(io.LimitReader(file, limit+1))
}

func Resolve(snapshots []SkillSnapshot, availableTools map[string]bool) (map[string]SkillSnapshot, error) {
	resolved := make(map[string]SkillSnapshot)
	for _, snapshot := range snapshots {
		if existing, duplicate := resolved[snapshot.Name]; duplicate && existing.Hash != snapshot.Hash {
			return nil, fmt.Errorf("skill collision %q", snapshot.Name)
		}
		for _, tool := range snapshot.RequiresTools {
			if !availableTools[tool] {
				return nil, fmt.Errorf("skill %s requires unavailable tool %s", snapshot.Name, tool)
			}
		}
		resolved[snapshot.Name] = snapshot
	}
	return resolved, nil
}

func containedPath(root, relative string) (string, error) {
	if filepath.IsAbs(relative) || relative == "" {
		return "", fmt.Errorf("%w: path must be relative", ErrUnsafeMarketplace)
	}
	clean := filepath.Clean(relative)
	if clean == ".." || strings.HasPrefix(clean, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("%w: path traversal", ErrUnsafeMarketplace)
	}
	joined := filepath.Join(root, clean)
	rel, err := filepath.Rel(root, joined)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("%w: path escapes root", ErrUnsafeMarketplace)
	}
	return joined, nil
}
