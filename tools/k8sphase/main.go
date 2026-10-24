// k8sphase разделяет собранный overlay для запуска платформы раньше сервисов (ADR-0035).
package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"os"

	"gopkg.in/yaml.v3"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
func run() error {
	phase := flag.String("phase", "foundation", "foundation или application")
	flag.Parse()
	if *phase != "foundation" && *phase != "application" {
		return errors.New("неизвестная фаза")
	}
	decoder := yaml.NewDecoder(os.Stdin)
	encoder := yaml.NewEncoder(os.Stdout)
	encoder.SetIndent(2)
	defer func() { _ = encoder.Close() }()
	for {
		var document map[string]any
		if err := decoder.Decode(&document); errors.Is(err, io.EOF) {
			return nil
		} else if err != nil {
			return err
		}
		if len(document) == 0 {
			continue
		}
		if application(document) == (*phase == "application") {
			if err := encoder.Encode(document); err != nil {
				return err
			}
		}
	}
}
func application(document map[string]any) bool {
	if document["kind"] == "HorizontalPodAutoscaler" {
		return true
	}
	if document["kind"] != "Deployment" {
		return false
	}
	metadata, _ := document["metadata"].(map[string]any)
	if metadata["namespace"] != "bs-app" {
		return false
	}
	labels, _ := metadata["labels"].(map[string]any)
	return labels["blood-supply.io/runtime"] == "go-service"
}
