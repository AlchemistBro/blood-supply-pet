package main

import "testing"

func TestApplicationPhaseLeavesInfrastructureFirst(t *testing.T) {
	for _, sample := range []struct {
		kind, runtime string
		want          bool
	}{
		{"Deployment", "go-service", true}, {"Deployment", "", false}, {"StatefulSet", "", false}, {"Secret", "", false}, {"HorizontalPodAutoscaler", "", true},
	} {
		object := map[string]any{"kind": sample.kind, "metadata": map[string]any{"namespace": "bs-app", "labels": map[string]any{"blood-supply.io/runtime": sample.runtime}}}
		if application(object) != sample.want {
			t.Fatalf("неверная фаза %s/%s", sample.kind, sample.runtime)
		}
	}
	if application(map[string]any{"kind": "Deployment", "metadata": map[string]any{"namespace": "bs-sims", "labels": map[string]any{"blood-supply.io/runtime": "go-service"}}}) {
		t.Fatal("имитатор отложен до запуска приложения")
	}
}
