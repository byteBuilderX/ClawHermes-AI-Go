package wiring

import "testing"

func TestBuildResourceRollbackExecutorNilWhenUnwired(t *testing.T) {
	c := &Container{}
	if got := c.buildResourceRollbackExecutor(nil); got != nil {
		t.Fatalf("expected nil executor when no sibling service wired, got %#v", got)
	}
}
