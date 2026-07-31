package tools

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/telemetryos/tos-tag/core/marketplace"
)

type Catalog struct {
	ID      string         `json:"id"`
	Version string         `json:"version"`
	Tools   []CatalogEntry `json:"tools"`
}

type CatalogEntry struct {
	Name string `json:"name"`
	Path string `json:"path"`
}

type Snapshot struct {
	MarketplaceID  string      `json:"marketplace_id"`
	CatalogVersion string      `json:"catalog_version"`
	Name           string      `json:"name"`
	ToolID         string      `json:"tool_id"`
	ToolVersion    string      `json:"tool_version"`
	ContentHash    string      `json:"content_hash"`
	Operations     []Operation `json:"operations"`
}

type Registry struct {
	bundles   map[string]Bundle
	snapshots []Snapshot
	skills    map[string]marketplace.SkillSnapshot
}

func LoadMarketplace(root, catalogPath string) (*Registry, error) {
	absolute, err := filepath.Abs(root)
	if err != nil {
		return nil, err
	}
	data, err := readRootFile(absolute, catalogPath, 1<<20)
	if err != nil {
		return nil, err
	}
	decoder := json.NewDecoder(strings.NewReader(string(data)))
	decoder.DisallowUnknownFields()
	var catalog Catalog
	if err := decoder.Decode(&catalog); err != nil {
		return nil, err
	}
	if catalog.ID == "" || catalog.Version == "" || len(catalog.Tools) == 0 {
		return nil, errors.New("tool marketplace ID, version, and tools are required")
	}
	registry := &Registry{bundles: make(map[string]Bundle), skills: make(map[string]marketplace.SkillSnapshot)}
	for _, entry := range catalog.Tools {
		if entry.Name == "" || entry.Path == "" || filepath.IsAbs(entry.Path) || strings.HasPrefix(filepath.Clean(entry.Path), "..") {
			return nil, errors.New("invalid tool marketplace entry")
		}
		bundleRoot, err := safeChild(absolute, entry.Path)
		if err != nil {
			return nil, err
		}
		skill, err := os.OpenRoot(bundleRoot)
		if err != nil {
			return nil, err
		}
		file, err := skill.Open("SKILL.md")
		if err != nil {
			_ = skill.Close()
			return nil, errors.New("tool bundle requires SKILL.md")
		}
		skillBytes, readErr := io.ReadAll(io.LimitReader(file, (1<<20)+1))
		_ = file.Close()
		_ = skill.Close()
		if readErr != nil {
			return nil, readErr
		}
		if len(skillBytes) > 1<<20 {
			return nil, errors.New("tool SKILL.md exceeds size limit")
		}
		bundle, err := LoadBundle(bundleRoot, "tool.json")
		if err != nil {
			return nil, err
		}
		if _, duplicate := registry.bundles[bundle.Manifest.ID]; duplicate {
			return nil, errors.New("duplicate tool ID")
		}
		registry.bundles[bundle.Manifest.ID] = bundle
		hash := sha256.New()
		_, _ = hash.Write([]byte("SKILL.md"))
		_, _ = hash.Write([]byte{0})
		_, _ = hash.Write(skillBytes)
		_, _ = hash.Write([]byte{0})
		registry.skills[bundle.Manifest.ID] = marketplace.SkillSnapshot{MarketplaceID: catalog.ID, Version: catalog.Version, Name: "tool-" + entry.Name, Root: bundleRoot, Files: []string{"SKILL.md"}, RequiresTools: []string{bundle.Manifest.ID}, Hash: "sha256:" + hex.EncodeToString(hash.Sum(nil))}
		registry.snapshots = append(registry.snapshots, Snapshot{MarketplaceID: catalog.ID, CatalogVersion: catalog.Version, Name: entry.Name, ToolID: bundle.Manifest.ID, ToolVersion: bundle.Manifest.Version, ContentHash: contentHash([]byte(bundle.ContentHash), skillBytes), Operations: append([]Operation(nil), bundle.Manifest.Operations...)})
	}
	sort.Slice(registry.snapshots, func(i, j int) bool { return registry.snapshots[i].ToolID < registry.snapshots[j].ToolID })
	return registry, nil
}

func (r *Registry) Select(toolIDs []string) ([]marketplace.SkillSnapshot, []string, error) {
	selected := make([]marketplace.SkillSnapshot, 0, len(toolIDs))
	ids := make([]string, 0, len(toolIDs))
	seen := make(map[string]bool)
	for _, id := range toolIDs {
		if seen[id] {
			continue
		}
		skill, ok := r.skills[id]
		if !ok {
			return nil, nil, errors.New("injected tool is not present in the configured marketplace")
		}
		seen[id] = true
		selected, ids = append(selected, skill), append(ids, id)
	}
	return selected, ids, nil
}

func (r *Registry) List() []Snapshot { return append([]Snapshot(nil), r.snapshots...) }

func (r *Registry) Resolve(id string) (Bundle, bool) {
	bundle, ok := r.bundles[id]
	return bundle, ok
}
