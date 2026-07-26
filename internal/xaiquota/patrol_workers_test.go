package xaiquota

import (
	"runtime"
	"testing"
)

func TestClampAndResolveWorkers(t *testing.T) {
	if clampPatrolUserMax(0) != 24 {
		t.Fatalf("default max want 24")
	}
	if clampPatrolUserMax(100) != 64 {
		t.Fatalf("cap 64")
	}
	w := resolvePatrolWorkers(2, 100)
	if w > 2 || w < 1 {
		t.Fatalf("workers=%d want <=2", w)
	}
	w = resolvePatrolWorkers(32, 1)
	if w != 1 {
		t.Fatalf("workers=%d want 1", w)
	}
	// with high max and many candidates, start aggressively (not stuck at 1–2)
	ncpu := runtime.NumCPU()
	if ncpu < 1 {
		ncpu = 1
	}
	w = resolvePatrolWorkers(16, 1000)
	if w < minInt(4, 16) {
		t.Fatalf("workers=%d want >=4 when max=16", w)
	}
	if w > 16 {
		t.Fatalf("workers=%d over max", w)
	}
}

func TestElasticClimbsWhenIdleHealthy(t *testing.T) {
	ncpu := runtime.NumCPU()
	if ncpu < 1 {
		ncpu = 1
	}
	// idle load, healthy probes, high max → should push toward max
	tgt, reason := elasticPatrolTarget(16, 1000, 4, 0.1, true, 0.0, 0.0)
	if tgt < 8 {
		t.Fatalf("target=%d reason=%s want >=8", tgt, reason)
	}
	if tgt > 16 {
		t.Fatalf("target=%d over max", tgt)
	}
	// critical load → shrink hard toward floor
	tgt2, r2 := elasticPatrolTarget(16, 1000, 12, float64(ncpu)*2, true, 0.0, 0.0)
	if tgt2 > ncpu {
		t.Fatalf("critical load target=%d reason=%s want <=cpu %d", tgt2, r2, ncpu)
	}
	if ncpu < 12 && tgt2 >= 12 {
		t.Fatalf("critical load target=%d reason=%s should shrink from 12", tgt2, r2)
	}
	// high timeout pressure → min-ish
	tgt3, r3 := elasticPatrolTarget(16, 1000, 12, 0.2, true, 0.5, 0.1)
	if tgt3 > ncpu+1 {
		t.Fatalf("timeout pressure target=%d reason=%s", tgt3, r3)
	}
}

func TestMaxMinInt(t *testing.T) {
	if maxInt(3, 7) != 7 || minInt(3, 7) != 3 {
		t.Fatal("max/min")
	}
}
