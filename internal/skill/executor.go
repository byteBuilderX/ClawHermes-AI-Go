package skill

import (
	"fmt"
	"sync"
	"time"
)

type ExecutionContext struct {
	SkillID   string
	Input     interface{}
	StartTime time.Time
	Timeout   time.Duration
}

type ExecutionResult struct {
	SkillID   string
	Output    interface{}
	Error     error
	Duration  time.Duration
	Timestamp time.Time
}

type SkillRegistry interface {
	Get(id string) (Skill, bool)
}

type Executor struct {
	registry SkillRegistry
	mu       sync.RWMutex
}

func NewExecutor(registry SkillRegistry) *Executor {
	return &Executor{
		registry: registry,
	}
}

func (e *Executor) Execute(ctx ExecutionContext) *ExecutionResult {
	start := time.Now()
	result := &ExecutionResult{
		SkillID:   ctx.SkillID,
		Timestamp: start,
	}

	skill, ok := e.registry.Get(ctx.SkillID)
	if !ok {
		result.Error = fmt.Errorf("skill not found: %s", ctx.SkillID)
		result.Duration = time.Since(start)
		return result
	}

	executor, ok := skill.(SkillExecutor)
	if !ok {
		result.Error = fmt.Errorf("skill is not executable: %s", ctx.SkillID)
		result.Duration = time.Since(start)
		return result
	}

	done := make(chan interface{}, 1)
	errChan := make(chan error, 1)

	go func() {
		output, err := executor.Execute(ctx.Input)
		if err != nil {
			errChan <- err
		} else {
			done <- output
		}
	}()

	timeout := ctx.Timeout
	if timeout == 0 {
		timeout = 30 * time.Second
	}

	select {
	case output := <-done:
		result.Output = output
	case err := <-errChan:
		result.Error = err
	case <-time.After(timeout):
		result.Error = fmt.Errorf("skill execution timeout: %s", ctx.SkillID)
	}

	result.Duration = time.Since(start)
	return result
}
