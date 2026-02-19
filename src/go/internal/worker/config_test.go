package worker

import "testing"

func TestNormalizeWorkerPoolSize_DefaultsToCPU(t *testing.T) {
	got := normalizeWorkerPoolSize(0, false, 6)
	if got != 6 {
		t.Fatalf("expected default pool size to match cpu=6, got %d", got)
	}
}

func TestNormalizeWorkerPoolSize_ClampsToCPU(t *testing.T) {
	got := normalizeWorkerPoolSize(12, true, 4)
	if got != 4 {
		t.Fatalf("expected pool size to clamp to cpu=4, got %d", got)
	}
}

func TestNormalizeWorkerPoolSize_NonPositiveConfiguredFallsBackToCPU(t *testing.T) {
	got := normalizeWorkerPoolSize(0, true, 8)
	if got != 8 {
		t.Fatalf("expected configured=0 to fallback to cpu=8, got %d", got)
	}

	got = normalizeWorkerPoolSize(-3, true, 8)
	if got != 8 {
		t.Fatalf("expected configured=-3 to fallback to cpu=8, got %d", got)
	}
}

func TestLoadConfiguredPoolSize_UsesEnvOverride(t *testing.T) {
	t.Setenv("WORKER_POOL_SIZE", "3")
	yamlPool := 2
	got, ok := loadConfiguredPoolSize(&yamlPool)
	if !ok {
		t.Fatalf("expected configured pool from env")
	}
	if got != 3 {
		t.Fatalf("expected env pool=3, got %d", got)
	}
}

func TestLoadConfiguredPoolSize_InvalidEnvFallsBackToYAML(t *testing.T) {
	t.Setenv("WORKER_POOL_SIZE", "not-a-number")
	yamlPool := 5
	got, ok := loadConfiguredPoolSize(&yamlPool)
	if !ok {
		t.Fatalf("expected configured pool from yaml fallback")
	}
	if got != 5 {
		t.Fatalf("expected yaml fallback pool=5, got %d", got)
	}
}
