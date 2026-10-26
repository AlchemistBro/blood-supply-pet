package main

import (
	"strings"
	"testing"
)

func sample() manifest {
	m := manifest{Schema: 1, Version: "v1.0.0", Commit: strings.Repeat("a", 40), Repository: "AlchemistBro/blood-supply-pet", Platform: "linux/amd64", Images: make(map[string]string)}
	for _, n := range names {
		m.Images["ghcr.io/alchemistbro/blood-supply-"+n+":1.0.0"] = "sha256:" + strings.Repeat("b", 64)
	}
	return m
}

func TestManifestRejectsMissingImagesAndWrongSource(t *testing.T) {
	for _, kind := range []string{"valid", "missing", "digest", "source", "repository"} {
		t.Run(kind, func(t *testing.T) {
			m := sample()
			switch kind {
			case "missing":
				delete(m.Images, "ghcr.io/alchemistbro/blood-supply-minio:1.0.0")
			case "digest":
				m.Images["ghcr.io/alchemistbro/blood-supply-web:1.0.0"] = "edge"
			case "source":
				m.Commit = strings.Repeat("c", 40)
			case "repository":
				m.Repository = "someone/other"
			}
			_, err := validate(m, "AlchemistBro/blood-supply-pet", strings.Repeat("a", 40))
			if (err == nil) != (kind == "valid") {
				t.Fatalf("acceptance: %v", err)
			}
		})
	}
}

func TestPinCoversJobsAndLeavesPlatformImagesUntouched(t *testing.T) {
	images, err := validate(sample(), "AlchemistBro/blood-supply-pet", strings.Repeat("a", 40))
	if err != nil {
		t.Fatal(err)
	}
	minio := map[string]any{"image": "ghcr.io/alchemistbro/blood-supply-minio:source-2025-10-15"}
	public := map[string]any{"image": "postgres:16"}
	d := map[string]any{"spec": map[string]any{"template": map[string]any{"containers": []any{minio, public}}}}
	seen := map[string]bool{}
	if err := pin(d, images, seen); err != nil {
		t.Fatal(err)
	}
	if minio["image"] != images["ghcr.io/alchemistbro/blood-supply-minio"] || public["image"] != "postgres:16" || len(seen) != 1 {
		t.Fatal("incorrect image pinning")
	}
	if err := pin(map[string]any{"image": "ghcr.io/alchemistbro/blood-supply-unknown:edge"}, images, seen); err == nil {
		t.Fatal("untracked image accepted")
	}
}
