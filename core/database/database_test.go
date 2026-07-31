package database

import (
	"fmt"
	"testing"

	"go.mongodb.org/mongo-driver/bson"
)

func TestRequiredIndexesHaveUniqueNamesPerCollection(t *testing.T) {
	seen := map[string]struct{}{}
	for _, spec := range RequiredIndexes() {
		if spec.Model.Options == nil || spec.Model.Options.Name == nil {
			t.Fatalf("index for %s has no name", spec.Collection)
		}
		key := fmt.Sprintf("%s/%s", spec.Collection, *spec.Model.Options.Name)
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
		if spec.Model.Options != nil && spec.Model.Options.Unique != nil {
			t.Fatalf("%s sets unique on intrinsic _id index", spec.Collection)
		}
	}
}

func TestRequiredTTLIndexesAreAbsolute(t *testing.T) {
	ttlCount := 0
	for _, spec := range RequiredIndexes() {
		if spec.Model.Options != nil && spec.Model.Options.ExpireAfterSeconds != nil {
			ttlCount++
			if got := *spec.Model.Options.ExpireAfterSeconds; got != 0 {
				t.Fatalf("TTL index %s is relative: %d", spec.Collection, got)
			}
		}
	}
	if ttlCount < 3 {
		t.Fatalf("expected observation, message, and context TTL indexes, got %d", ttlCount)
	}
}
