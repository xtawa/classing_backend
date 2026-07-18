package aicost

import (
	"strings"
	"unicode/utf8"
)

const (
	DefaultModel        = "deepseek-v4-flash"
	DefaultMonthlyLimit = 10000
)

// Model describes the public model catalog and its retail compute-point rates.
// Rates are points per one million tokens. One point represents about CNY 0.0001.
type Model struct {
	ID                  string `json:"id"`
	Name                string `json:"name"`
	Description         string `json:"description"`
	CachedInputPoints   int64  `json:"cachedInputPointsPerMillion"`
	UncachedInputPoints int64  `json:"inputPointsPerMillion"`
	OutputPoints        int64  `json:"outputPointsPerMillion"`
}

type TokenUsage struct {
	InputTokens       int
	CachedInputTokens int
	OutputTokens      int
}

var models = []Model{
	{ID: "deepseek-v4-flash", Name: "DeepSeek V4 Flash", Description: "响应更快，适合日常课表问答", CachedInputPoints: 260, UncachedInputPoints: 13000, OutputPoints: 26000},
	{ID: "deepseek-v4-pro", Name: "DeepSeek V4 Pro", Description: "推理能力更强，复杂问题消耗更多额度", CachedInputPoints: 325, UncachedInputPoints: 39000, OutputPoints: 78000},
}

func Catalog() []Model {
	result := make([]Model, len(models))
	copy(result, models)
	return result
}

func Resolve(id string) (Model, bool) {
	id = strings.TrimSpace(id)
	if id == "" || id == "deepseek-chat" || id == "deepseek-reasoner" {
		id = DefaultModel
	}
	for _, item := range models {
		if item.ID == id {
			return item, true
		}
	}
	return Model{}, false
}

func Points(modelID string, usage TokenUsage) int {
	model, ok := Resolve(modelID)
	if !ok {
		return 0
	}
	input := max(0, usage.InputTokens)
	cached := min(input, max(0, usage.CachedInputTokens))
	uncached := input - cached
	numerator := int64(cached)*model.CachedInputPoints + int64(uncached)*model.UncachedInputPoints + int64(max(0, usage.OutputTokens))*model.OutputPoints
	if numerator <= 0 {
		return 1
	}
	return int((numerator + 999999) / 1000000)
}

func ReservationPoints(modelID string, estimatedInputTokens, maxOutputTokens int) int {
	return Points(modelID, TokenUsage{InputTokens: max(1, estimatedInputTokens), OutputTokens: max(1, maxOutputTokens)})
}

// EstimateTokens intentionally errs slightly high for mixed Chinese/JSON input.
func EstimateTokens(values ...string) int {
	total := 0
	for _, value := range values {
		runes := utf8.RuneCountInString(value)
		ascii := 0
		for _, r := range value {
			if r < 128 {
				ascii++
			}
		}
		total += (ascii+3)/4 + (runes - ascii)
	}
	return max(1, total)
}
