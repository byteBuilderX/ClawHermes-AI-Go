package orchestrator

import (
	"testing"

	"github.com/byteBuilderX/ClawHermes-AI-Go/internal/skill"
)

func TestRegistry(t *testing.T) {
	registry := NewRegistry()

	s := &skill.BaseSkill{
		ID:          "test-1",
		Name:        "Test Skill",
		Description: "A test skill",
		Type:        "builtin",
	}

	registry.Register(s.ID, s)

	retrieved, ok := registry.Get(s.ID)
	if !ok {
		t.Fatal("skill not found")
	}

	if retrieved.GetID() != s.ID {
		t.Errorf("expected ID %s, got %s", s.ID, retrieved.GetID())
	}

	if retrieved.GetName() != s.Name {
		t.Errorf("expected name %s, got %s", s.Name, retrieved.GetName())
	}
}

func TestRegistryNotFound(t *testing.T) {
	registry := NewRegistry()

	_, ok := registry.Get("non-existent")
	if ok {
		t.Fatal("expected skill not found")
	}
}
