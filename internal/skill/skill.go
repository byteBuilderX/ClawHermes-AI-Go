package skill

type Skill interface {
	GetID() string
	GetName() string
	GetDescription() string
	GetType() string
}

type BaseSkill struct {
	ID          string
	Name        string
	Description string
	Type        string
}

func (s *BaseSkill) GetID() string {
	return s.ID
}

func (s *BaseSkill) GetName() string {
	return s.Name
}

func (s *BaseSkill) GetDescription() string {
	return s.Description
}

func (s *BaseSkill) GetType() string {
	return s.Type
}

type SkillExecutor interface {
	Skill
	Execute(input interface{}) (interface{}, error)
}
