package claude

import (
	"encoding/json"
	"maps"
	"slices"
	"strings"

	contracts "github.com/Herrscherd/herrscher-contracts"
)

const (
	wireUser        = "user"
	wireAssistant   = "assistant"
	wireSystem      = "system"
	subtypeInit     = "init"
	subtypeAPIRetry = "api_retry"
	blockText       = "text"
	blockToolUse    = "tool_use"
	blockToolResult = "tool_result"
	stateActive     = "active"

	textBlockSeparator = "\n\n"
)

type contentBlock struct {
	Type      string          `json:"type"`
	Text      string          `json:"text"`
	Thinking  string          `json:"thinking"`
	Name      string          `json:"name"`
	Input     json.RawMessage `json:"input"`
	ID        string          `json:"id"`
	ToolUseID string          `json:"tool_use_id"`
	Content   json.RawMessage `json:"content"`
	IsError   bool            `json:"is_error"`
}

type toolResultLine struct {
	Message struct {
		Content []contentBlock `json:"content"`
	} `json:"message"`
}

func parseTodos(input json.RawMessage) []contracts.TodoItem {
	var todoList struct {
		Todos []struct {
			Content string `json:"content"`
			Status  string `json:"status"`
		} `json:"todos"`
	}
	if json.Unmarshal(input, &todoList) != nil {
		return nil
	}
	out := make([]contracts.TodoItem, 0, len(todoList.Todos))
	for _, t := range todoList.Todos {
		out = append(out, contracts.TodoItem{Text: t.Content, State: todoState(t.Status)})
	}
	return out
}

func todoState(status string) string {
	switch status {
	case "completed":
		return "done"
	case "in_progress":
		return stateActive
	}
	return "pending"
}

func parseAgent(toolUseID string, input json.RawMessage) contracts.Subagent {
	var agentInput struct {
		Description  string `json:"description"`
		SubagentType string `json:"subagent_type"`
	}
	if !decodeOrSkip(input, &agentInput) {
		return contracts.Subagent{ID: toolUseID, State: stateActive}
	}
	name := agentInput.Description
	if name == "" {
		name = agentInput.SubagentType
	}
	return contracts.Subagent{ID: toolUseID, Name: name, Kind: agentInput.SubagentType, State: stateActive}
}

func toolDetail(input json.RawMessage) string {
	if len(input) == 0 {
		return ""
	}
	var inputFields map[string]any
	if json.Unmarshal(input, &inputFields) != nil {
		return ""
	}
	for _, k := range []string{"command", "file_path", "path", "pattern", "query", "url", "description", "prompt"} {
		if s, ok := inputFields[k].(string); ok && s != "" {
			return s
		}
	}
	return ""
}

type apiErrorFlag bool

func (f *apiErrorFlag) UnmarshalJSON(data []byte) error {
	*f = string(data) == "true"
	return nil
}

type streamEvent struct {
	TotalCostUSD float64      `json:"total_cost_usd"`
	IsAPIError   apiErrorFlag `json:"isApiErrorMessage"`
	Message      struct {
		Content []contentBlock `json:"content"`
		Usage   *usage         `json:"usage"`
	} `json:"message"`
	Usage      *usage                `json:"usage"`
	ModelUsage map[string]modelUsage `json:"modelUsage"`
}

type modelUsage struct {
	InputTokens              int     `json:"inputTokens"`
	OutputTokens             int     `json:"outputTokens"`
	CacheReadInputTokens     int     `json:"cacheReadInputTokens"`
	CacheCreationInputTokens int     `json:"cacheCreationInputTokens"`
	CostUSD                  float64 `json:"costUSD"`
}

type usage struct {
	InputTokens              int `json:"input_tokens"`
	OutputTokens             int `json:"output_tokens"`
	CacheReadInputTokens     int `json:"cache_read_input_tokens"`
	CacheCreationInputTokens int `json:"cache_creation_input_tokens"`
}

func decodeOrSkip(data []byte, into any) bool {
	return json.Unmarshal(data, into) == nil
}

type resultControl struct {
	Type      string          `json:"type"`
	Subtype   string          `json:"subtype"`
	IsError   bool            `json:"is_error"`
	SessionID string          `json:"session_id"`
	Result    string          `json:"result"`
	Errors    json.RawMessage `json:"errors"`
}

func joinedErrors(raw json.RawMessage) string {
	var messages []string
	if err := json.Unmarshal(raw, &messages); err != nil {
		return ""
	}
	var kept []string
	for _, message := range messages {
		if message = strings.TrimSpace(message); message != "" {
			kept = append(kept, message)
		}
	}
	return strings.Join(kept, "\n")
}

func turnModels(byModel map[string]modelUsage) []contracts.ModelUsage {
	if len(byModel) == 0 {
		return nil
	}
	models := make([]contracts.ModelUsage, 0, len(byModel))
	for _, model := range slices.Sorted(maps.Keys(byModel)) {
		spent := byModel[model]
		models = append(models, contracts.ModelUsage{
			Model:       model,
			In:          spent.InputTokens,
			Out:         spent.OutputTokens,
			CacheRead:   spent.CacheReadInputTokens,
			CacheCreate: spent.CacheCreationInputTokens,
			Cost:        spent.CostUSD,
		})
	}
	return models
}

func resultUsage(event streamEvent) usage {
	switch {
	case event.Usage != nil:
		return *event.Usage
	case event.Message.Usage != nil:
		return *event.Message.Usage
	}
	return usage{}
}
