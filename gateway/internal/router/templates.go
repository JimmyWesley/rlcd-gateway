package router

import "net/http"

// RuleTemplate is a ready-made decision rule the dashboard offers. Its
// targets are left empty: Slots say what each one should be, and the rule
// validates once they are filled.
type RuleTemplate struct {
	ID          string `json:"id"`
	Title       string `json:"title"`
	Description string `json:"description"`
	Rule        Rule   `json:"rule"`
	Slots       []Slot `json:"slots"`
}

// Slot is a target to fill: branch is the branch index, or -1 for else.
type Slot struct {
	Branch int    `json:"branch"`
	Label  string `json:"label"`
	Hint   string `json:"hint"`
}

func fp(v float64) *float64 { return &v }

// RuleTemplates are the three presets. Their wording was checked against
// open-rlcd (Open-RLCD-text) and Jev (jev-latest) on small prompt sets
// (2026-09): task type 12/12 on both; complexity, on the strong/fast
// split, 9/10 (Jev) and 10/10 (open-rlcd); sensitive data 20/20 (Jev) and
// 17/20 (open-rlcd), whose misses are sensitive messages it scored low.
// Longer lists of examples in the noul question made open-rlcd worse.
func RuleTemplates() []RuleTemplate {
	return []RuleTemplate{
		{
			ID:    "task-type",
			Title: "Route by task type",
			Description: "Asks what kind of task the user's message is, and sends coding work, everyday chat " +
				"and legal questions to different models. Decided at conversation start and pinned.",
			Rule: Rule{
				Name: "task type", Enabled: true, Kind: KindDecision,
				Question: &DecisionQuestion{Type: QuestionChoice,
					Instructions: taskTypeInstructions,
					Criteria: &Criteria{Choices: map[string]string{
						"code":  "writing, reviewing, debugging or explaining source code, scripts, SQL, shell commands or software configuration",
						"chat":  "everyday conversation, simple factual questions, recommendations, rewriting, translating or summarizing ordinary text",
						"legal": "questions about laws, contracts, terms of service, liability, compliance, regulations or legal disputes",
					}}},
				Inputs:  &DecisionInputs{Facts: []string{InputLatestUserText}},
				Backend: "economy", TimeoutMs: 2000, MinConfidence: 0.6,
				Branches: []Branch{
					{Label: "code", When: BranchWhen{Equals: "code"}},
					{Label: "chat", When: BranchWhen{Equals: "chat"}},
					{Label: "legal", When: BranchWhen{Equals: "legal"}},
				},
				Else:     &Target{NextRule: true},
				Evaluate: EvalConversationStart,
			},
			Slots: []Slot{
				{Branch: 0, Label: "code", Hint: "a strong coding model, e.g. Claude Sonnet"},
				{Branch: 1, Label: "chat", Hint: "a cheap, fast model, e.g. Qwen through OpenRouter"},
				{Branch: 2, Label: "legal", Hint: "your strongest model"},
			},
		},
		{
			ID:    "complexity",
			Title: "Route by complexity",
			Description: "Scores how much reasoning the request needs from 0 to 3: 2 or more goes to a strong model, " +
				"the rest to a fast one. Decided at conversation start, and once more when the conversation grows " +
				"past 100k tokens.",
			Rule: Rule{
				Name: "complexity", Enabled: true, Kind: KindDecision,
				Question: &DecisionQuestion{Type: QuestionScore,
					Instructions: complexityInstructions,
					Criteria: &Criteria{Legend: []string{
						"trivial: a greeting, a one-line fact or a yes/no answer",
						"simple: a short, well-defined task with an obvious approach",
						"moderate: several steps, some design choices or careful reading",
						"hard: deep multi-step reasoning, tricky debugging, system design or high-stakes analysis",
					}}},
				Inputs:  &DecisionInputs{Facts: []string{InputLatestUserText}},
				Backend: "economy", TimeoutMs: 2000,
				Branches: []Branch{
					{Label: "hard", When: BranchWhen{Op: ">=", Value: fp(2)}},
					{Label: "easy", When: BranchWhen{Op: "<=", Value: fp(1)}},
				},
				Else:     &Target{NextRule: true},
				Evaluate: EvalWhenContextOver, ContextOverTokens: 100000,
			},
			Slots: []Slot{
				{Branch: 0, Label: "hard", Hint: "a strong model, e.g. Claude Opus"},
				{Branch: 1, Label: "easy", Hint: "a fast, cheap model, e.g. Claude Haiku"},
			},
		},
		{
			ID:    "sensitive-local",
			Title: "Keep sensitive data local",
			Description: "Asks whether the message contains sensitive personal data. Likely yes (p ≥ 0.7) goes to a " +
				"local model; otherwise the cloud. If the check fails or times out, the request stays local. " +
				"Only the start of a conversation is checked: data pasted later in a pinned conversation " +
				"is not, so use every_request when that matters.",
			Rule: Rule{
				Name: "sensitive data", Enabled: true, Kind: KindDecision,
				Question: &DecisionQuestion{Type: QuestionNoul,
					Instructions: sensitiveInstructions},
				Inputs:  &DecisionInputs{Facts: []string{InputLatestUserText}},
				Backend: "economy", TimeoutMs: 2000,
				Branches: []Branch{
					{Label: "sensitive", When: BranchWhen{Op: ">=", Value: fp(0.7)}},
					{Label: "not sensitive", When: BranchWhen{Op: "<", Value: fp(0.7)}},
				},
				Else:     &Target{},
				Evaluate: EvalConversationStart,
			},
			Slots: []Slot{
				{Branch: 0, Label: "sensitive", Hint: "a local route, e.g. Ollama at http://127.0.0.1:11434/v1"},
				{Branch: 1, Label: "not sensitive", Hint: "your usual cloud route"},
				{Branch: -1, Label: "else", Hint: "the same local route: when the check fails, keep the data local"},
			},
		},
	}
}

const (
	taskTypeInstructions = "What kind of task is the user asking for in this message? " +
		"Judge the subject of the request, not its length or tone."
	complexityInstructions = "How much reasoning does it take to answer the user's request well? " +
		"Judge the difficulty of the task itself, not the length of the message."
	sensitiveInstructions = "Does the message contain private personal data (an ID, card or account number, " +
		"a password, someone's health information or home address)?"
)

func (r *Router) getTemplates(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, RuleTemplates())
}
