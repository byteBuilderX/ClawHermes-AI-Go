package skill

import (
	"testing"
	"time"
)

func TestCodeSkillCreation(t *testing.T) {
	cs := NewCodeSkill("test-1", "Test Code Skill", "A test code skill", "print('hello')", "python")

	if cs.GetID() != "test-1" {
		t.Errorf("expected ID test-1, got %s", cs.GetID())
	}

	if cs.GetName() != "Test Code Skill" {
		t.Errorf("expected name Test Code Skill, got %s", cs.GetName())
	}

	if cs.Language != "python" {
		t.Errorf("expected language python, got %s", cs.Language)
	}
}

func TestExecutor(t *testing.T) {
	registry := &mockRegistry{
		skills: make(map[string]Skill),
	}

	cs := NewCodeSkill("test-1", "Test", "Test", "code", "python")
	registry.skills["test-1"] = cs

	executor := NewExecutor(registry)

	ctx := ExecutionContext{
		SkillID: "test-1",
		Input:   "test input",
		Timeout: 5 * time.Second,
	}

	result := executor.Execute(ctx)

	if result.SkillID != "test-1" {
		t.Errorf("expected skill ID test-1, got %s", result.SkillID)
	}

	if result.Error != nil {
		t.Errorf("expected no error, got %v", result.Error)
	}
}

func TestExecutorTimeout(t *testing.T) {
	registry := &mockRegistry{
		skills: make(map[string]Skill),
	}

	cs := &slowSkill{
		BaseSkill: &BaseSkill{
			ID:   "slow-1",
			Name: "Slow Skill",
			Type: "code",
		},
	}
	registry.skills["slow-1"] = cs

	executor := NewExecutor(registry)

	ctx := ExecutionContext{
		SkillID: "slow-1",
		Input:   "test",
		Timeout: 100 * time.Millisecond,
	}

	result := executor.Execute(ctx)

	if result.Error == nil {
		t.Error("expected timeout error")
	}
}

type mockRegistry struct {
	skills map[string]Skill
}

func (m *mockRegistry) Get(id string) (Skill, bool) {
	s, ok := m.skills[id]
	return s, ok
}

type slowSkill struct {
	*BaseSkill
}

func (s *slowSkill) Execute(input interface{}) (interface{}, error) {
	time.Sleep(1 * time.Second)
	return nil, nil
}
