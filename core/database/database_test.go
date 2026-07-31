package database

import (
	"fmt"
	"testing"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo/options"
)

func resolvedIndexOptions(t *testing.T, builder *options.IndexOptionsBuilder) options.IndexOptions {
	t.Helper()
	var resolved options.IndexOptions
	for _, apply := range builder.List() {
		if err := apply(&resolved); err != nil {
			t.Fatalf("resolve index options: %v", err)
		}
	}
	return resolved
}

func TestRequiredIndexesHaveUniqueNamesPerCollection(t *testing.T) {
	seen := map[string]struct{}{}
	for _, spec := range RequiredIndexes() {
		if spec.Model.Options == nil {
			t.Fatalf("index for %s has no name", spec.Collection)
		}
		resolved := resolvedIndexOptions(t, spec.Model.Options)
		if resolved.Name == nil {
			t.Fatalf("index for %s has no name", spec.Collection)
		}
		key := fmt.Sprintf("%s/%s", spec.Collection, *resolved.Name)
		if _, exists := seen[key]; exists {
			t.Fatalf("duplicate index name %s", key)
		}
		seen[key] = struct{}{}
	}
}

func TestIDIndexesDoNotSetRedundantUniqueOption(t *testing.T) {
	for _, spec := range RequiredIndexes() {
		keys, ok := spec.Model.Keys.(bson.D)
		if !ok || len(keys) != 1 || keys[0].Key != "_id" {
			continue
		}
		if spec.Model.Options != nil && resolvedIndexOptions(t, spec.Model.Options).Unique != nil {
			t.Fatalf("%s sets unique on intrinsic _id index", spec.Collection)
		}
	}
}

func TestRequiredTTLIndexesAreAbsolute(t *testing.T) {
	ttlCount := 0
	for _, spec := range RequiredIndexes() {
		if spec.Model.Options == nil {
			continue
		}
		resolved := resolvedIndexOptions(t, spec.Model.Options)
		if resolved.ExpireAfterSeconds != nil {
			ttlCount++
			if got := *resolved.ExpireAfterSeconds; got != 0 {
				t.Fatalf("TTL index %s is relative: %d", spec.Collection, got)
			}
		}
	}
	if ttlCount < 3 {
		t.Fatalf("expected observation, message, and context TTL indexes, got %d", ttlCount)
	}
}
