// k8simages pins demo manifests to the exact candidate release image inventory.
package main

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"regexp"
	"strings"

	"gopkg.in/yaml.v3"
)

type manifest struct {
	Schema     int               `json:"schema_version"`
	Version    string            `json:"version"`
	Commit     string            `json:"source_commit"`
	Repository string            `json:"source_repository"`
	Platform   string            `json:"platform"`
	Images     map[string]string `json:"images"`
}

var names = []string{"audit", "demand", "inventory", "logistics-gateway", "matching", "notification", "planning", "sim-aist", "sim-emergency", "sim-logistics", "transfer", "web", "minio"}
var sha = regexp.MustCompile(`^[a-f0-9]{40}$`)
var digest = regexp.MustCompile(`^sha256:[a-f0-9]{64}$`)
var version = regexp.MustCompile(`^v(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)$`)

func validate(m manifest, repository, commit string) (map[string]string, error) {
	if m.Schema != 1 || !sha.MatchString(commit) || m.Commit != commit || m.Repository != repository || m.Platform != "linux/amd64" || !version.MatchString(m.Version) || len(m.Images) != len(names) {
		return nil, errors.New("release manifest does not match this source commit, repository, platform or schema")
	}
	images := make(map[string]string)
	for _, name := range names {
		base := "ghcr.io/" + strings.ToLower(strings.Split(repository, "/")[0]) + "/blood-supply-" + name
		d := m.Images[base+":"+strings.TrimPrefix(m.Version, "v")]
		if !digest.MatchString(d) {
			return nil, fmt.Errorf("missing immutable release image: %s", name)
		}
		images[base] = base + "@" + d
	}
	return images, nil
}

func pin(value any, images map[string]string, seen map[string]bool) error {
	switch x := value.(type) {
	case map[string]any:
		for key, child := range x {
			if key == "image" {
				if image, ok := child.(string); ok && strings.Contains(image, "/blood-supply-") {
					base := strings.SplitN(strings.SplitN(image, "@", 2)[0], ":", 2)[0]
					replacement, exists := images[base]
					if !exists {
						return fmt.Errorf("image not covered by release manifest: %s", base)
					}
					x[key] = replacement
					seen[base] = true
				}
			}
			if err := pin(child, images, seen); err != nil {
				return err
			}
		}
	case []any:
		for _, child := range x {
			if err := pin(child, images, seen); err != nil {
				return err
			}
		}
	}
	return nil
}

func run() error {
	path := flag.String("manifest", "", "candidate images.json")
	commit := flag.String("commit", "", "expected source commit")
	repository := flag.String("repository", "AlchemistBro/blood-supply-pet", "expected source repository")
	flag.Parse()
	raw, err := os.ReadFile(*path) //nolint:gosec // explicit local manifest path
	if err != nil {
		return err
	}
	var m manifest
	if err := json.Unmarshal(raw, &m); err != nil {
		return err
	}
	images, err := validate(m, *repository, *commit)
	if err != nil {
		return err
	}
	decoder := yaml.NewDecoder(os.Stdin)
	var documents []map[string]any
	seen := make(map[string]bool)
	for {
		var d map[string]any
		if err := decoder.Decode(&d); errors.Is(err, io.EOF) {
			break
		} else if err != nil {
			return err
		}
		if len(d) == 0 {
			continue
		}
		if err := pin(d, images, seen); err != nil {
			return err
		}
		documents = append(documents, d)
	}
	if len(seen) != len(images) {
		return errors.New("rendered overlay does not use all 13 release images")
	}
	encoder := yaml.NewEncoder(os.Stdout)
	encoder.SetIndent(2)
	defer func() { _ = encoder.Close() }()
	for _, d := range documents {
		if err := encoder.Encode(d); err != nil {
			return err
		}
	}
	return nil
}

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "k8simages:", err)
		os.Exit(1)
	}
}
