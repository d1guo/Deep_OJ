package scheduler

import (
	"testing"
)

func TestGetNextWorker_RoundRobin(t *testing.T) {
	d := &WorkerDiscovery{
		workers: []workerEndpoint{
			{id: "worker-1", addr: "127.0.0.1:5001"},
			{id: "worker-2", addr: "127.0.0.1:5002"},
		},
	}

	id1, _, ok1 := d.GetNextWorker()
	id2, _, ok2 := d.GetNextWorker()

	if !ok1 || !ok2 {
		t.Fatalf("expected round robin to return available workers")
	}
	if id1 != "worker-1" || id2 != "worker-2" {
		t.Fatalf("unexpected round robin order: got %s then %s", id1, id2)
	}
}

func TestGetNextWorker_Empty(t *testing.T) {
	d := &WorkerDiscovery{}

	_, _, ok := d.GetNextWorker()
	if ok {
		t.Error("Expected false for empty workers")
	}
}
