package skill

type CodeSkill struct {
	*BaseSkill
	Code     string
	Language string
}

func NewCodeSkill(id, name, description, code, language string) *CodeSkill {
	return &CodeSkill{
		BaseSkill: &BaseSkill{
			ID:          id,
			Name:        name,
			Description: description,
			Type:        "code",
		},
		Code:     code,
		Language: language,
	}
}

func (cs *CodeSkill) Execute(input interface{}) (interface{}, error) {
	// TODO: Implement code execution
	return nil, nil
}
