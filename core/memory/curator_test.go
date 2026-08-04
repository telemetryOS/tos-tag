package memory

import "testing"

func TestForgottenOperatorMemoryCanBeRelearnedFromChangedSources(t *testing.T) {
	forgotten := Record{Status: StatusForgotten, Origin: "operator", SourceHash: "old"}
	if skipGeneratedUpdate(forgotten, "changed") {
		t.Fatal("forgotten operator memory blocked relearning from changed source content")
	}
	if !skipGeneratedUpdate(forgotten, "old") {
		t.Fatal("unchanged forgotten source should retain the tombstone")
	}
	active := Record{Status: StatusActive, Origin: "operator", SourceHash: "old"}
	if !skipGeneratedUpdate(active, "changed") {
		t.Fatal("active operator correction was not protected")
	}
}
