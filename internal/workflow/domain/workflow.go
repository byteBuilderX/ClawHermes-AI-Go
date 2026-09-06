package domain

import (
	"errors"
	"fmt"
	"math"
	"sort"
	"strconv"
	"strings"
	"time"
)

var (
	ErrRevisionConflict    = errors.New("workflow revision conflict")
	ErrInvalidSpec         = errors.New("invalid workflow specification")
	ErrInvalidInputSchema  = errors.New("invalid workflow input schema")
	ErrInvalidTransition   = errors.New("invalid workflow state transition")
	ErrIdempotencyConflict = errors.New("workflow idempotency conflict")
	ErrGenerationConflict  = errors.New("workflow generation conflict")
	ErrFenceConflict       = errors.New("workflow fence conflict")
	ErrDecisionConflict    = errors.New("workflow approval decision conflict")
	ErrApprovalRequired    = errors.New("workflow approval required")
	ErrNotFound            = errors.New("workflow not found")
	ErrForbidden           = errors.New("workflow action forbidden")
	ErrEditorNotEligible   = errors.New("workflow editor not eligible")
)

type NodeType string

const (
	MaxWorkflowNodes        = 100
	MaxWorkflowEdges        = 400
	MaxWorkflowInputFields  = 50
	MaxWorkflowConcurrency  = 16
	MaxTenantConcurrentRuns = 8
)

type InputFieldType string

const (
	InputFieldShortText    InputFieldType = "short_text"
	InputFieldLongText     InputFieldType = "long_text"
	InputFieldNumber       InputFieldType = "number"
	InputFieldSingleSelect InputFieldType = "single_select"
	InputFieldMultiSelect  InputFieldType = "multi_select"
	InputFieldBoolean      InputFieldType = "boolean"
	InputFieldDate         InputFieldType = "date"
)

type InputOption struct {
	Label string `json:"label"`
	Value string `json:"value"`
}

type InputField struct {
	Key         string         `json:"key"`
	Label       string         `json:"label"`
	Type        InputFieldType `json:"type"`
	Required    bool           `json:"required,omitempty"`
	Description string         `json:"description,omitempty"`
	Default     any            `json:"default,omitempty"`
	Options     []InputOption  `json:"options,omitempty"`
}

type InputSchema struct {
	TaskLabel       string       `json:"task_label"`
	TaskDescription string       `json:"task_description,omitempty"`
	Fields          []InputField `json:"fields,omitempty"`
}

type InputIssue struct {
	Field   string `json:"field"`
	Code    string `json:"code"`
	Message string `json:"message"`
}

type InputValidationError struct {
	Issues []InputIssue `json:"issues"`
}

type GraphIssue struct {
	Path    string `json:"path"`
	Code    string `json:"code"`
	Message string `json:"message"`
}

type GraphValidationError struct {
	Issues []GraphIssue `json:"issues"`
}

func (e *GraphValidationError) Error() string {
	return "workflow graph validation failed"
}

func (e *GraphValidationError) Unwrap() error {
	return ErrInvalidSpec
}

func (e *InputValidationError) Error() string {
	return "workflow input validation failed"
}

func (e *InputValidationError) Unwrap() error {
	return ErrInvalidInputSchema
}

const (
	NodeTypeAgent     NodeType = "agent"
	NodeTypeSkill     NodeType = "skill"
	NodeTypeMCPTool   NodeType = "mcp_tool"
	NodeTypeCondition NodeType = "condition"
	NodeTypeApproval  NodeType = "approval"
)

type EffectClass string

const (
	EffectClassPure          EffectClass = "pure"
	EffectClassIdempotent    EffectClass = "idempotent"
	EffectClassNonIdempotent EffectClass = "non_idempotent"
)

type RetryPolicy struct {
	MaxAttempts int `json:"max_attempts,omitempty"`
	BackoffMS   int `json:"backoff_ms,omitempty"`
}

// NodePosition 是节点在画布上的坐标（前端 React Flow 坐标系）。
// 指针 + omitempty：历史 spec 无此字段，响应省略，校验/回写不受影响。
type NodePosition struct {
	X float64 `json:"x"`
	Y float64 `json:"y"`
}

type Node struct {
	ID              string            `json:"id"`
	Name            string            `json:"name,omitempty"`
	Type            NodeType          `json:"type"`
	AgentID         string            `json:"agent_id"`
	SkillID         string            `json:"skill_id,omitempty"`
	SkillRevisionID string            `json:"skill_revision_id,omitempty"`
	MCPServerID     string            `json:"mcp_server_id,omitempty"`
	MCPToolName     string            `json:"mcp_tool_name,omitempty"`
	Condition       string            `json:"condition,omitempty"`
	EffectClass     EffectClass       `json:"effect_class,omitempty"`
	InputMapping    map[string]string `json:"input_mapping,omitempty"`
	OutputMapping   map[string]string `json:"output_mapping,omitempty"`
	Retry           RetryPolicy       `json:"retry,omitempty"`
	TimeoutMS       int               `json:"timeout_ms,omitempty"`
	Position        *NodePosition     `json:"position,omitempty"`
}

type Edge struct {
	ID             string `json:"id,omitempty"`
	From           string `json:"from"`
	To             string `json:"to"`
	ConditionValue *bool  `json:"condition_value,omitempty"`
	Default        bool   `json:"default,omitempty"`
}

type Spec struct {
	Nodes          []Node `json:"nodes"`
	Edges          []Edge `json:"edges"`
	MaxConcurrency int    `json:"max_concurrency,omitempty"`
}

type Definition struct {
	ID              string      `json:"id"`
	Name            string      `json:"name"`
	Description     string      `json:"description"`
	CreatedBy       string      `json:"created_by,omitempty"`
	Editors         []string    `json:"editors,omitempty"`
	Revision        int64       `json:"revision"`
	ActiveVersionID string      `json:"active_version_id,omitempty"`
	Spec            Spec        `json:"spec"`
	InputSchema     InputSchema `json:"input_schema"`
	CreatedAt       time.Time   `json:"created_at"`
	UpdatedAt       time.Time   `json:"updated_at"`
}

func NewDefinition(id, name, description string, spec Spec, schemas ...InputSchema) (*Definition, error) {
	if id == "" || name == "" {
		return nil, fmt.Errorf("%w: id and name are required", ErrInvalidSpec)
	}
	schema := defaultInputSchema()
	if len(schemas) > 0 {
		schema = schemas[0]
	}
	if err := ValidateInputSchema(schema); err != nil {
		return nil, err
	}
	return &Definition{ID: id, Name: name, Description: description, Revision: 1, Spec: cloneSpec(spec), InputSchema: cloneInputSchema(schema)}, nil
}

func (d *Definition) UpdateDraft(name, description string, spec Spec, expectedRevision int64, schemas ...InputSchema) error {
	if d.Revision != expectedRevision {
		return ErrRevisionConflict
	}
	if name == "" {
		return fmt.Errorf("%w: name is required", ErrInvalidSpec)
	}
	schema := d.InputSchema
	if len(schemas) > 0 {
		schema = schemas[0]
	}
	if err := ValidateInputSchema(schema); err != nil {
		return err
	}
	d.Name, d.Description, d.Spec, d.InputSchema = name, description, cloneSpec(spec), cloneInputSchema(schema)
	d.Revision++
	return nil
}

type Version struct {
	ID           string `json:"id"`
	DefinitionID string `json:"definition_id"`
	Number       int64  `json:"version"`
	Name         string `json:"name"`
	Description  string `json:"description"`
	// CreatedBy 是发布该版本的操作用户 id（版本历史「操作者」原始 id）。展示名由
	// application 经 ActorNameResolver 现算（CreatedByName），不进 domain。
	CreatedBy   string      `json:"created_by"`
	Spec        Spec        `json:"spec"`
	InputSchema InputSchema `json:"input_schema"`
	CreatedAt   time.Time   `json:"created_at"`
}

func (d *Definition) Publish(id string, number int64) (*Version, error) {
	if id == "" || number < 1 {
		return nil, fmt.Errorf("%w: version identity is required", ErrInvalidSpec)
	}
	if err := ValidateSpec(d.Spec); err != nil {
		return nil, err
	}
	if err := ValidateInputSchema(d.InputSchema); err != nil {
		return nil, err
	}
	return &Version{ID: id, DefinitionID: d.ID, Number: number, Name: d.Name, Description: d.Description, Spec: cloneSpec(d.Spec), InputSchema: cloneInputSchema(d.InputSchema)}, nil
}

func ValidateInputSchema(schema InputSchema) error {
	if strings.TrimSpace(schema.TaskLabel) == "" {
		return fmt.Errorf("%w: task label is required", ErrInvalidInputSchema)
	}
	if len(schema.Fields) > MaxWorkflowInputFields {
		return fmt.Errorf("%w: too many fields", ErrInvalidInputSchema)
	}
	keys := make(map[string]struct{}, len(schema.Fields))
	for _, field := range schema.Fields {
		if field.Key == "" || field.Key == "task" || strings.TrimSpace(field.Label) == "" {
			return fmt.Errorf("%w: field key and label are required", ErrInvalidInputSchema)
		}
		if _, exists := keys[field.Key]; exists {
			return fmt.Errorf("%w: duplicate field key", ErrInvalidInputSchema)
		}
		keys[field.Key] = struct{}{}
		if !validInputFieldType(field.Type) {
			return fmt.Errorf("%w: unsupported field type", ErrInvalidInputSchema)
		}
		if field.Type == InputFieldSingleSelect || field.Type == InputFieldMultiSelect {
			if err := validateOptions(field.Options); err != nil {
				return err
			}
		}
		if field.Default != nil && !validInputValue(field, field.Default) {
			return fmt.Errorf("%w: invalid field default", ErrInvalidInputSchema)
		}
	}
	return nil
}

func ValidateRunInput(schema InputSchema, input map[string]any) error {
	issues := make([]InputIssue, 0)
	if task, ok := input["task"].(string); !ok || strings.TrimSpace(task) == "" {
		issues = append(issues, InputIssue{Field: "task", Code: "required", Message: "请输入任务"})
	}
	fields := make(map[string]InputField, len(schema.Fields))
	for _, field := range schema.Fields {
		fields[field.Key] = field
		value, exists := input[field.Key]
		if !exists || value == nil || emptyInputValue(value) {
			if field.Required {
				issues = append(issues, InputIssue{Field: field.Key, Code: "required", Message: field.Label + "为必填项"})
			}
			continue
		}
		if !validInputValue(field, value) {
			issues = append(issues, InputIssue{Field: field.Key, Code: "invalid", Message: field.Label + "格式不正确"})
		}
	}
	unknown := make([]string, 0)
	for key := range input {
		if key != "task" {
			if _, exists := fields[key]; !exists {
				unknown = append(unknown, key)
			}
		}
	}
	sort.Strings(unknown)
	for _, key := range unknown {
		issues = append(issues, InputIssue{Field: key, Code: "unknown", Message: "字段未在工作流输入中定义"})
	}
	if len(issues) > 0 {
		return &InputValidationError{Issues: issues}
	}
	return nil
}

func ValidateSpec(spec Spec) error {
	err := validateSpec(spec)
	if err == nil {
		return nil
	}
	message := strings.TrimSpace(strings.TrimPrefix(err.Error(), ErrInvalidSpec.Error()+":"))
	return &GraphValidationError{Issues: []GraphIssue{{Path: "graph", Code: "invalid", Message: message}}}
}

// ValidateSpecGraph 校验工作流的图结构（节点/边结构、无环），用于草稿保存。
// 允许空图、字段未填全的半成品节点、孤立节点与 condition 未连 default 边
// （画一半先保存，与前端连线守卫只挡环对齐）。弱连通、condition default、
// 输入引用可达等发布级完整性约束由 ValidateSpec 在校验/发布时强制。
func ValidateSpecGraph(spec Spec) error {
	err := validateGraphDraft(spec)
	if err == nil {
		return nil
	}
	message := strings.TrimSpace(strings.TrimPrefix(err.Error(), ErrInvalidSpec.Error()+":"))
	return &GraphValidationError{Issues: []GraphIssue{{Path: "graph", Code: "invalid", Message: message}}}
}

// validateGraphDraft 是草稿保存的图校验：只强制图结构与无环，与前端 hasCycle
// 对齐。Kahn 环检测对断连但各分量无环的图能访问全部节点（每个无环分量都有
// 零入度根），不会误报；弱连通与 condition default 边是发布级约束，画一半时
// 不应阻塞保存。
func validateGraphDraft(spec Spec) error {
	if len(spec.Nodes) == 0 {
		return nil
	}
	if err := validateGraphLimits(spec); err != nil {
		return err
	}
	nodes, err := indexNodes(spec)
	if err != nil {
		return err
	}
	adj, in, _, err := validateEdges(spec, nodes)
	if err != nil {
		return err
	}
	if err := validateAcyclic(nodes, adj, in); err != nil {
		return err
	}
	return nil
}

func validateGraph(spec Spec) error {
	if len(spec.Nodes) == 0 {
		return nil
	}
	if err := validateGraphLimits(spec); err != nil {
		return err
	}
	nodes, err := indexNodes(spec)
	if err != nil {
		return err
	}
	adj, in, conditionDefaults, err := validateEdges(spec, nodes)
	if err != nil {
		return err
	}
	if err := validateConditionDefaults(nodes, conditionDefaults); err != nil {
		return err
	}
	if err := validateAcyclic(nodes, adj, in); err != nil {
		return err
	}
	if err := validateConnectivity(spec, nodes, in); err != nil {
		return err
	}
	for _, node := range spec.Nodes {
		if err := validateNodeConnections(node, nodes, adj); err != nil {
			return err
		}
	}
	return nil
}

func validateGraphLimits(spec Spec) error {
	if len(spec.Nodes) > MaxWorkflowNodes || len(spec.Edges) > MaxWorkflowEdges {
		return fmt.Errorf("%w: graph exceeds node or edge limit", ErrInvalidSpec)
	}
	if spec.MaxConcurrency < 0 || spec.MaxConcurrency > MaxWorkflowConcurrency {
		return fmt.Errorf("%w: graph concurrency exceeds limit", ErrInvalidSpec)
	}
	return nil
}

func indexNodes(spec Spec) (map[string]Node, error) {
	nodes := make(map[string]Node, len(spec.Nodes))
	for _, node := range spec.Nodes {
		if node.ID == "" {
			return nil, fmt.Errorf("%w: every node must have an id", ErrInvalidSpec)
		}
		if _, exists := nodes[node.ID]; exists {
			return nil, fmt.Errorf("%w: duplicate node %q", ErrInvalidSpec, node.ID)
		}
		nodes[node.ID] = node
	}
	return nodes, nil
}

func validateEdges(spec Spec, nodes map[string]Node) (adj map[string][]string, in map[string]int, conditionDefaults map[string]int, err error) {
	adj = make(map[string][]string, len(nodes))
	in = make(map[string]int, len(nodes))
	conditionDefaults = map[string]int{}
	edgeIDs := make(map[string]struct{}, len(spec.Edges))
	for _, edge := range spec.Edges {
		if edge.From == edge.To {
			return nil, nil, nil, fmt.Errorf("%w: self edge %q", ErrInvalidSpec, edge.From)
		}
		if _, ok := nodes[edge.From]; !ok {
			return nil, nil, nil, fmt.Errorf("%w: unknown source %q", ErrInvalidSpec, edge.From)
		}
		if _, ok := nodes[edge.To]; !ok {
			return nil, nil, nil, fmt.Errorf("%w: unknown target %q", ErrInvalidSpec, edge.To)
		}
		if edge.ID != "" {
			if _, exists := edgeIDs[edge.ID]; exists {
				return nil, nil, nil, fmt.Errorf("%w: duplicate edge %q", ErrInvalidSpec, edge.ID)
			}
			edgeIDs[edge.ID] = struct{}{}
		}
		in[edge.To]++
		if edge.Default {
			conditionDefaults[edge.From]++
		}
		adj[edge.From] = append(adj[edge.From], edge.To)
	}
	return adj, in, conditionDefaults, nil
}

func validateConditionDefaults(nodes map[string]Node, conditionDefaults map[string]int) error {
	for _, node := range nodes {
		if node.Type == NodeTypeCondition && conditionDefaults[node.ID] != 1 {
			return fmt.Errorf("%w: condition %q requires exactly one default edge", ErrInvalidSpec, node.ID)
		}
	}
	return nil
}

// validateAcyclic 用 Kahn 拓扑排序检测环；队列耗尽前未访问全部节点即存在环或断连。
// 零入度种子必须遍历全部节点（无入边节点不在 in map 中），否则起点会被漏掉误报。
func validateAcyclic(nodes map[string]Node, adj map[string][]string, in map[string]int) error {
	indegree := make(map[string]int, len(nodes))
	queue := make([]string, 0, len(nodes))
	for id := range nodes {
		indegree[id] = in[id]
		if in[id] == 0 {
			queue = append(queue, id)
		}
	}
	visited := 0
	for len(queue) > 0 {
		current := queue[0]
		queue = queue[1:]
		visited++
		for _, next := range adj[current] {
			indegree[next]--
			if indegree[next] == 0 {
				queue = append(queue, next)
			}
		}
	}
	if visited != len(nodes) {
		return fmt.Errorf("%w: disconnected or cyclic graph", ErrInvalidSpec)
	}
	return nil
}

func validateConnectivity(spec Spec, nodes map[string]Node, in map[string]int) error {
	root := ""
	for id := range nodes {
		if in[id] == 0 {
			root = id
			break
		}
	}
	if root == "" {
		return fmt.Errorf("%w: graph has no entry", ErrInvalidSpec)
	}
	if !weaklyConnected(spec, root) {
		return fmt.Errorf("%w: disconnected graph", ErrInvalidSpec)
	}
	return nil
}

func validateSpec(spec Spec) error {
	if len(spec.Nodes) == 0 {
		return fmt.Errorf("%w: at least one node is required", ErrInvalidSpec)
	}
	// 先逐节点字段校验再图校验：与历史一致，节点字段错误（如缺 condition 表达式）
	// 优先于图结构错误（如缺 default 边）返回。
	for _, node := range spec.Nodes {
		if err := validateNode(node); err != nil {
			return err
		}
	}
	if err := validateGraph(spec); err != nil {
		return err
	}
	return nil
}

func validateNodeConnections(node Node, nodes map[string]Node, adj map[string][]string) error {
	for _, reference := range node.InputMapping {
		upstreamID, ok := referencedNode(reference)
		if !ok {
			continue
		}
		if upstreamID == node.ID || !reachable(adj, upstreamID, node.ID) {
			return fmt.Errorf("%w: node %q input references non-upstream node %q", ErrInvalidSpec, node.ID, upstreamID)
		}
	}
	if node.Type == NodeTypeCondition {
		if upstreamID, ok := conditionReferencedNode(node.Condition); ok {
			if _, exists := nodes[upstreamID]; !exists || upstreamID == node.ID || !reachable(adj, upstreamID, node.ID) {
				return fmt.Errorf("%w: condition %q references non-upstream node %q", ErrInvalidSpec, node.ID, upstreamID)
			}
		}
	}
	return nil
}

func weaklyConnected(spec Spec, start string) bool {
	adj := make(map[string][]string, len(spec.Nodes))
	for _, edge := range spec.Edges {
		adj[edge.From] = append(adj[edge.From], edge.To)
		adj[edge.To] = append(adj[edge.To], edge.From)
	}
	seen, queue := map[string]bool{start: true}, []string{start}
	for len(queue) > 0 {
		current := queue[0]
		queue = queue[1:]
		for _, next := range adj[current] {
			if !seen[next] {
				seen[next] = true
				queue = append(queue, next)
			}
		}
	}
	return len(seen) == len(spec.Nodes)
}

func referencedNode(reference string) (string, bool) {
	const prefix = "nodes."
	if !strings.HasPrefix(reference, prefix) {
		return "", false
	}
	rest := strings.TrimPrefix(reference, prefix)
	parts := strings.Split(rest, ".")
	if len(parts) < 2 || parts[0] == "" || parts[1] != "output" {
		return "", false
	}
	return parts[0], true
}

func reachable(adj map[string][]string, from, to string) bool {
	seen, queue := map[string]bool{from: true}, []string{from}
	for len(queue) > 0 {
		current := queue[0]
		queue = queue[1:]
		for _, next := range adj[current] {
			if next == to {
				return true
			}
			if !seen[next] {
				seen[next] = true
				queue = append(queue, next)
			}
		}
	}
	return false
}

// maxNodePositionAbs 是节点坐标的绝对值上限，防止极端值进入渲染端。
const maxNodePositionAbs = 1e6

func validateNode(node Node) error {
	if err := validateNodeType(node); err != nil {
		return err
	}
	if err := validateExecutionPolicy(node); err != nil {
		return err
	}
	if err := validateNodePosition(node.Position); err != nil {
		return err
	}
	return validateOutputMappings(node.OutputMapping)
}

func validateNodePosition(position *NodePosition) error {
	if position == nil {
		return nil
	}
	if math.IsNaN(position.X) || math.IsInf(position.X, 0) ||
		math.IsNaN(position.Y) || math.IsInf(position.Y, 0) ||
		math.Abs(position.X) > maxNodePositionAbs || math.Abs(position.Y) > maxNodePositionAbs {
		return fmt.Errorf("%w: node position must be finite and within ±%g", ErrInvalidSpec, maxNodePositionAbs)
	}
	return nil
}

func validateNodeType(node Node) error {
	switch node.Type {
	case NodeTypeAgent:
		return validateAgentNode(node)
	case NodeTypeSkill:
		return validateSkillNode(node)
	case NodeTypeMCPTool:
		return validateMCPToolNode(node)
	case NodeTypeCondition:
		return validateConditionNode(node)
	case NodeTypeApproval:
		// Approval nodes are durable control points and need no executor identity.
		return nil
	default:
		return fmt.Errorf("%w: unsupported node type %q", ErrInvalidSpec, node.Type)
	}
}

func validateAgentNode(node Node) error {
	if node.AgentID == "" {
		return fmt.Errorf("%w: agent node %q requires agent_id", ErrInvalidSpec, node.ID)
	}
	return nil
}

func validateSkillNode(node Node) error {
	if node.AgentID == "" || node.SkillID == "" || node.SkillRevisionID == "" {
		return fmt.Errorf("%w: skill node %q requires agent and pinned revision", ErrInvalidSpec, node.ID)
	}
	return nil
}

// ValidateSkillBinding 校验 skill 节点绑定关系：skillID 必须出现在 agent 的
// allowedSkills 中。空列表表示该 agent 未挂载任何技能，任何引用都被拒绝。
func ValidateSkillBinding(allowed []string, skillID string) error {
	if len(allowed) == 0 {
		return fmt.Errorf("%w: agent enables no skills, cannot reference skill %q", ErrInvalidSpec, skillID)
	}
	for _, id := range allowed {
		if id == skillID {
			return nil
		}
	}
	return fmt.Errorf("%w: agent does not enable skill %q", ErrInvalidSpec, skillID)
}

func validateMCPToolNode(node Node) error {
	if node.MCPServerID == "" || node.MCPToolName == "" || !validEffectClass(node.EffectClass) {
		return fmt.Errorf("%w: mcp node %q requires server, tool and effect class", ErrInvalidSpec, node.ID)
	}
	return nil
}

func validateConditionNode(node Node) error {
	if !validConditionExpression(node.Condition) {
		return fmt.Errorf("%w: condition node %q requires expression", ErrInvalidSpec, node.ID)
	}
	return nil
}

func validateExecutionPolicy(node Node) error {
	if node.Retry.MaxAttempts < 0 || node.TimeoutMS < 0 {
		return fmt.Errorf("%w: invalid execution policy", ErrInvalidSpec)
	}
	return nil
}

func validateOutputMappings(outputMapping map[string]string) error {
	for _, selector := range outputMapping {
		if selector != "$" && (!strings.HasPrefix(selector, "$.") || len(strings.TrimPrefix(selector, "$.")) == 0) {
			return fmt.Errorf("%w: invalid output selector", ErrInvalidSpec)
		}
	}
	return nil
}

func validEffectClass(class EffectClass) bool {
	return class == EffectClassPure || class == EffectClassIdempotent || class == EffectClassNonIdempotent
}

func conditionReferencedNode(expression string) (string, bool) {
	parts := strings.Split(expression, "==")
	if len(parts) != 2 {
		return "", false
	}
	left := strings.TrimSpace(parts[0])
	if !strings.HasPrefix(left, "nodes.") || !strings.HasSuffix(left, ".output") {
		return "", false
	}
	id := strings.TrimSuffix(strings.TrimPrefix(left, "nodes."), ".output")
	return id, id != ""
}

func validConditionExpression(expression string) bool {
	parts := strings.Split(expression, "==")
	if len(parts) != 2 {
		return false
	}
	left, right := strings.TrimSpace(parts[0]), strings.TrimSpace(parts[1])
	validLeft := strings.HasPrefix(left, "input.") && len(strings.TrimPrefix(left, "input.")) > 0
	if strings.HasPrefix(left, "nodes.") && strings.HasSuffix(left, ".output") {
		validLeft = len(strings.TrimSuffix(strings.TrimPrefix(left, "nodes."), ".output")) > 0
	}
	if !validLeft {
		return false
	}
	if right == "true" || right == "false" {
		return true
	}
	if len(right) >= 2 && ((right[0] == '\'' && right[len(right)-1] == '\'') || (right[0] == '"' && right[len(right)-1] == '"')) {
		return true
	}
	_, err := strconv.ParseFloat(right, 64)
	return err == nil
}

func cloneSpec(spec Spec) Spec {
	nodes := append([]Node(nil), spec.Nodes...)
	for i := range nodes {
		if nodes[i].InputMapping != nil {
			nodes[i].InputMapping = make(map[string]string, len(nodes[i].InputMapping))
			for key, value := range spec.Nodes[i].InputMapping {
				nodes[i].InputMapping[key] = value
			}
		}
		if nodes[i].OutputMapping != nil {
			nodes[i].OutputMapping = make(map[string]string, len(nodes[i].OutputMapping))
			for key, value := range spec.Nodes[i].OutputMapping {
				nodes[i].OutputMapping[key] = value
			}
		}
		if nodes[i].Position != nil {
			pos := *nodes[i].Position
			nodes[i].Position = &pos
		}
	}
	return Spec{Nodes: nodes, Edges: append([]Edge(nil), spec.Edges...), MaxConcurrency: spec.MaxConcurrency}
}

func defaultInputSchema() InputSchema {
	return InputSchema{TaskLabel: "任务", Fields: []InputField{}}
}

func cloneInputSchema(schema InputSchema) InputSchema {
	fields := append([]InputField(nil), schema.Fields...)
	for i := range fields {
		fields[i].Options = append([]InputOption(nil), schema.Fields[i].Options...)
		switch value := schema.Fields[i].Default.(type) {
		case []string:
			fields[i].Default = append([]string(nil), value...)
		case []any:
			fields[i].Default = append([]any(nil), value...)
		}
	}
	return InputSchema{TaskLabel: schema.TaskLabel, TaskDescription: schema.TaskDescription, Fields: fields}
}

func validInputFieldType(fieldType InputFieldType) bool {
	switch fieldType {
	case InputFieldShortText, InputFieldLongText, InputFieldNumber, InputFieldSingleSelect,
		InputFieldMultiSelect, InputFieldBoolean, InputFieldDate:
		return true
	default:
		return false
	}
}

func validateOptions(options []InputOption) error {
	if len(options) == 0 {
		return fmt.Errorf("%w: select fields require options", ErrInvalidInputSchema)
	}
	values := make(map[string]struct{}, len(options))
	for _, option := range options {
		if strings.TrimSpace(option.Label) == "" || option.Value == "" {
			return fmt.Errorf("%w: option label and value are required", ErrInvalidInputSchema)
		}
		if _, exists := values[option.Value]; exists {
			return fmt.Errorf("%w: duplicate option value", ErrInvalidInputSchema)
		}
		values[option.Value] = struct{}{}
	}
	return nil
}

func validInputValue(field InputField, value any) bool {
	switch field.Type {
	case InputFieldShortText, InputFieldLongText:
		_, ok := value.(string)
		return ok
	case InputFieldNumber:
		return isNumber(value)
	case InputFieldSingleSelect:
		return validSingleSelectInputValue(field, value)
	case InputFieldMultiSelect:
		return validMultiSelectInputValue(field, value)
	case InputFieldBoolean:
		_, ok := value.(bool)
		return ok
	case InputFieldDate:
		return validDateInputValue(value)
	default:
		return false
	}
}

func validSingleSelectInputValue(field InputField, value any) bool {
	selected, ok := value.(string)
	return ok && optionExists(field.Options, selected)
}

func validMultiSelectInputValue(field InputField, value any) bool {
	values, ok := stringSlice(value)
	if !ok {
		return false
	}
	for _, selected := range values {
		if !optionExists(field.Options, selected) {
			return false
		}
	}
	return true
}

func validDateInputValue(value any) bool {
	date, ok := value.(string)
	if !ok {
		return false
	}
	_, err := time.Parse("2006-01-02", date)
	return err == nil
}

func isNumber(value any) bool {
	switch value.(type) {
	case int, int8, int16, int32, int64, uint, uint8, uint16, uint32, uint64, float32, float64:
		return true
	default:
		return false
	}
}

func stringSlice(value any) ([]string, bool) {
	switch values := value.(type) {
	case []string:
		return values, true
	case []any:
		result := make([]string, len(values))
		for i, value := range values {
			text, ok := value.(string)
			if !ok {
				return nil, false
			}
			result[i] = text
		}
		return result, true
	default:
		return nil, false
	}
}

func optionExists(options []InputOption, selected string) bool {
	for _, option := range options {
		if option.Value == selected {
			return true
		}
	}
	return false
}

func emptyInputValue(value any) bool {
	switch typed := value.(type) {
	case string:
		return strings.TrimSpace(typed) == ""
	case []string:
		return len(typed) == 0
	case []any:
		return len(typed) == 0
	default:
		return false
	}
}

type RunStatus string

const (
	RunStatusQueued             RunStatus = "queued"
	RunStatusRunning            RunStatus = "running"
	RunStatusCompleted          RunStatus = "completed"
	RunStatusFailed             RunStatus = "failed"
	RunStatusPaused             RunStatus = "paused"
	RunStatusPauseRequested     RunStatus = "pause_requested"
	RunStatusCancelRequested    RunStatus = "cancel_requested"
	RunStatusCanceled           RunStatus = "canceled"
	RunStatusManualIntervention RunStatus = "manual_intervention"
)

type Run struct {
	ID             string         `json:"id"`
	DefinitionID   string         `json:"definition_id"`
	Name           string         `json:"name"`
	VersionID      string         `json:"version_id"`
	VersionNumber  int64          `json:"version"`
	Status         RunStatus      `json:"status"`
	Snapshot       Spec           `json:"snapshot"`
	Input          map[string]any `json:"input"`
	Output         string         `json:"output"`
	ErrorMessage   string         `json:"error_message,omitempty"`
	IdempotencyKey string         `json:"-"`
	RequestHash    string         `json:"-"`
	Generation     int64          `json:"generation"`
	PauseReason    string         `json:"pause_reason,omitempty"`
	CancelReason   string         `json:"cancel_reason,omitempty"`
	ManualReason   string         `json:"manual_reason,omitempty"`
	CreatedBy      string         `json:"created_by"`
	SchedulerOwner string         `json:"scheduler_owner,omitempty"`
	LeaseExpiresAt *time.Time     `json:"lease_expires_at,omitempty"`
	CreatedAt      time.Time      `json:"created_at"`
	UpdatedAt      time.Time      `json:"updated_at"`
	StartedAt      *time.Time     `json:"started_at,omitempty"`
	FinishedAt     *time.Time     `json:"finished_at,omitempty"`
}

func NewRun(id string, version *Version, input map[string]any, idempotencyKey, requestHash string) (*Run, error) {
	if id == "" || version == nil || idempotencyKey == "" || requestHash == "" {
		return nil, fmt.Errorf("%w: run identity, version and idempotency are required", ErrInvalidSpec)
	}
	if err := ValidateRunInput(version.InputSchema, input); err != nil {
		return nil, err
	}
	return &Run{ID: id, DefinitionID: version.DefinitionID, VersionID: version.ID, VersionNumber: version.Number, Status: RunStatusQueued, Snapshot: cloneSpec(version.Spec), Input: cloneMap(input), IdempotencyKey: idempotencyKey, RequestHash: requestHash, Generation: 1}, nil
}

func (r *Run) Pause(reason string) error {
	if r.Status != RunStatusRunning || reason == "" {
		return ErrInvalidTransition
	}
	r.Status, r.PauseReason = RunStatusPaused, reason
	r.Generation++
	return nil
}

func (r *Run) RequestPause(reason string, expectedGeneration int64) error {
	if r.Generation != expectedGeneration {
		return ErrGenerationConflict
	}
	if r.Status == RunStatusPauseRequested || r.Status == RunStatusPaused {
		return nil
	}
	if r.Status != RunStatusQueued && r.Status != RunStatusRunning {
		return ErrInvalidTransition
	}
	r.Status, r.PauseReason = RunStatusPauseRequested, reason
	r.Generation++
	return nil
}

func (r *Run) MarkPaused(expectedGeneration int64) error {
	if r.Generation != expectedGeneration {
		return ErrGenerationConflict
	}
	if r.Status != RunStatusPauseRequested {
		return ErrInvalidTransition
	}
	r.Status = RunStatusPaused
	return nil
}

func (r *Run) Resume(expectedGeneration int64) error {
	if r.Generation != expectedGeneration {
		return ErrGenerationConflict
	}
	if r.Status != RunStatusPaused && r.Status != RunStatusManualIntervention {
		return ErrInvalidTransition
	}
	r.Status, r.PauseReason, r.ManualReason = RunStatusQueued, "", ""
	r.Generation++
	return nil
}

func (r *Run) RequestCancel(expectedGeneration int64) error {
	if r.Generation != expectedGeneration {
		return ErrGenerationConflict
	}
	if r.Status == RunStatusCancelRequested || r.Status == RunStatusCanceled {
		return nil
	}
	if r.terminal() {
		return ErrInvalidTransition
	}
	r.Status = RunStatusCancelRequested
	r.Generation++
	return nil
}

func (r *Run) MarkCanceled(expectedGeneration int64) error {
	if r.Generation != expectedGeneration {
		return ErrGenerationConflict
	}
	if r.Status != RunStatusCancelRequested {
		return ErrInvalidTransition
	}
	r.Status = RunStatusCanceled
	return nil
}

func (r *Run) AvailableActions(pendingApproval, manual bool) []string {
	switch r.Status {
	case RunStatusQueued, RunStatusRunning:
		return []string{"pause", "cancel"}
	case RunStatusPauseRequested, RunStatusCancelRequested:
		return []string{"cancel"}
	case RunStatusPaused:
		if pendingApproval {
			return []string{"cancel"}
		}
		return []string{"resume", "cancel"}
	case RunStatusManualIntervention:
		if manual {
			return []string{"mark_succeeded", "retry", "terminate"}
		}
	}
	return nil
}

func (r *Run) terminal() bool {
	return r.Status == RunStatusCompleted || r.Status == RunStatusFailed || r.Status == RunStatusCanceled
}

func (r *Run) Start() error {
	if r.Status != RunStatusQueued {
		return ErrInvalidTransition
	}
	r.Status = RunStatusRunning
	return nil
}

func (r *Run) Complete(output string) error {
	if r.Status != RunStatusRunning || output == "" {
		return ErrInvalidTransition
	}
	r.Status, r.Output = RunStatusCompleted, output
	return nil
}

func (r *Run) Fail(message string) error {
	if r.Status != RunStatusRunning || message == "" {
		return ErrInvalidTransition
	}
	r.Status, r.ErrorMessage = RunStatusFailed, message
	return nil
}

type AttemptStatus string

const (
	AttemptStatusPending            AttemptStatus = "pending"
	AttemptStatusRunning            AttemptStatus = "running"
	AttemptStatusReady              AttemptStatus = "ready"
	AttemptStatusClaimed            AttemptStatus = "claimed"
	AttemptStatusSucceeded          AttemptStatus = "succeeded"
	AttemptStatusFailed             AttemptStatus = "failed"
	AttemptStatusRetryWait          AttemptStatus = "retry_wait"
	AttemptStatusSkipped            AttemptStatus = "skipped"
	AttemptStatusPaused             AttemptStatus = "paused"
	AttemptStatusCanceled           AttemptStatus = "canceled"
	AttemptStatusManualIntervention AttemptStatus = "manual_intervention"
)

type NodeAttempt struct {
	ID            string        `json:"id"`
	RunID         string        `json:"run_id"`
	NodeID        string        `json:"node_id"`
	AttemptNo     int           `json:"attempt_no"`
	Status        AttemptStatus `json:"status"`
	Input         string        `json:"input"`
	OutputSummary string        `json:"output_summary"`
	ErrorMessage  string        `json:"error_message,omitempty"`
	TraceID       string        `json:"trace_id,omitempty"`
	FenceToken    int64         `json:"fence_token"`
	RunGeneration int64         `json:"run_generation"`
	ErrorCode     string        `json:"error_code,omitempty"`
	RetryAt       *time.Time    `json:"retry_at,omitempty"`
	EffectClass   EffectClass   `json:"effect_class,omitempty"`
	SelectedEdges []string      `json:"selected_edges,omitempty"`
}

func (a *NodeAttempt) StartClaimed(fence int64) error {
	if a.Status != AttemptStatusClaimed || a.FenceToken != fence {
		return ErrFenceConflict
	}
	a.Status = AttemptStatusRunning
	return nil
}

func (a *NodeAttempt) SucceedFenced(output, traceID string, fence int64) error {
	if a.FenceToken != fence {
		return ErrFenceConflict
	}
	return a.Succeed(output, traceID)
}

func (a *NodeAttempt) Start() error {
	if a.Status != AttemptStatusPending {
		return ErrInvalidTransition
	}
	a.Status = AttemptStatusRunning
	return nil
}

func (a *NodeAttempt) Succeed(output, traceID string) error {
	if a.Status != AttemptStatusRunning || output == "" {
		return ErrInvalidTransition
	}
	a.Status, a.OutputSummary, a.TraceID = AttemptStatusSucceeded, output, traceID
	return nil
}

func (a *NodeAttempt) Fail(message string) error {
	if a.Status != AttemptStatusRunning || message == "" {
		return ErrInvalidTransition
	}
	a.Status, a.ErrorMessage = AttemptStatusFailed, message
	return nil
}

func cloneMap(in map[string]any) map[string]any {
	out := make(map[string]any, len(in))
	for key, value := range in {
		out[key] = value
	}
	return out
}

type Event struct {
	ID         string         `json:"id"`
	RunID      string         `json:"run_id"`
	SequenceNo int64          `json:"sequence_no"`
	Type       string         `json:"event_type"`
	Status     string         `json:"status,omitempty"`
	NodeID     string         `json:"node_id,omitempty"`
	AttemptNo  int            `json:"attempt_no,omitempty"`
	Summary    string         `json:"summary,omitempty"`
	ActorType  string         `json:"actor_type,omitempty"`
	ActorID    string         `json:"actor_id,omitempty"`
	Payload    map[string]any `json:"data,omitempty"`
	OccurredAt time.Time      `json:"occurred_at"`
}

type ApprovalStatus string
type ApprovalDecision string

const (
	ApprovalStatusPending   ApprovalStatus   = "pending"
	ApprovalStatusApproved  ApprovalStatus   = "approved"
	ApprovalStatusRejected  ApprovalStatus   = "rejected"
	ApprovalDecisionApprove ApprovalDecision = "approve"
	ApprovalDecisionReject  ApprovalDecision = "reject"
)

type Approval struct {
	ID, RunID, NodeID, AttemptID   string
	RunGeneration                  int64
	Reason, Risk, RequestSummary   string
	Status                         ApprovalStatus
	DecisionActor, DecisionComment string
	DecidedAt                      *time.Time
}

func NewApproval(id, runID, nodeID, attemptID string, generation int64, reason, risk, summary string) *Approval {
	return &Approval{ID: id, RunID: runID, NodeID: nodeID, AttemptID: attemptID, RunGeneration: generation, Reason: reason, Risk: risk, RequestSummary: summary, Status: ApprovalStatusPending}
}

func (a *Approval) Decide(decision ApprovalDecision, actor, comment string, generation int64, attemptID string) error {
	if a.RunGeneration != generation {
		return ErrGenerationConflict
	}
	if a.AttemptID != attemptID {
		return ErrFenceConflict
	}
	if a.Status != ApprovalStatusPending {
		return ErrDecisionConflict
	}
	if decision != ApprovalDecisionApprove && decision != ApprovalDecisionReject {
		return ErrInvalidTransition
	}
	if decision == ApprovalDecisionApprove {
		a.Status = ApprovalStatusApproved
	} else {
		a.Status = ApprovalStatusRejected
	}
	now := time.Now().UTC()
	a.DecisionActor, a.DecisionComment, a.DecidedAt = actor, comment, &now
	return nil
}

type EffectIntentStatus string
type ManualAction string

const (
	EffectIntentStatusPrepared  EffectIntentStatus = "prepared"
	EffectIntentStatusStarted   EffectIntentStatus = "started"
	EffectIntentStatusSucceeded EffectIntentStatus = "succeeded"
	EffectIntentStatusFailed    EffectIntentStatus = "failed"
	EffectIntentStatusUnknown   EffectIntentStatus = "unknown"
)

const (
	ManualActionMarkSucceeded ManualAction = "mark_succeeded"
	ManualActionRetry         ManualAction = "retry"
	ManualActionTerminate     ManualAction = "terminate"
)

type EffectIntent struct {
	ID, RunID, NodeID, AttemptID string
	RunGeneration                int64
	EffectClass                  EffectClass
	IdempotencyKey               string
	Status                       EffectIntentStatus
	Reason, OutputSummary        string
}

func NewEffectIntent(id, runID, nodeID, attemptID string, generation int64, class EffectClass, key string) *EffectIntent {
	return &EffectIntent{ID: id, RunID: runID, NodeID: nodeID, AttemptID: attemptID, RunGeneration: generation, EffectClass: class, IdempotencyKey: key, Status: EffectIntentStatusPrepared}
}

func (i *EffectIntent) Start(generation int64) error {
	if i.RunGeneration != generation {
		return ErrGenerationConflict
	}
	if i.Status != EffectIntentStatusPrepared {
		return ErrInvalidTransition
	}
	i.Status = EffectIntentStatusStarted
	return nil
}

func (i *EffectIntent) MarkUnknown(reason string, generation int64) error {
	if i.RunGeneration != generation {
		return ErrGenerationConflict
	}
	if i.Status != EffectIntentStatusStarted {
		return ErrInvalidTransition
	}
	i.Status, i.Reason = EffectIntentStatusUnknown, reason
	return nil
}

func (i EffectIntent) RequiresManualIntervention() bool {
	return i.EffectClass == EffectClassNonIdempotent && i.Status == EffectIntentStatusUnknown
}
