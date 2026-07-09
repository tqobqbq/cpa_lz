package downstreamtext

import (
	"strings"

	sdktranslator "github.com/router-for-me/CLIProxyAPI/v7/sdk/translator"
	"github.com/tidwall/gjson"
)

// Extract returns assistant-visible text from a downstream response payload.
func Extract(format sdktranslator.Format, output []byte) (string, bool) {
	trimmed := strings.TrimSpace(string(output))
	if trimmed == "" || !gjson.Valid(trimmed) {
		return "", false
	}

	root := gjson.Parse(trimmed)
	collector := textCollector{}
	switch format {
	case sdktranslator.FormatOpenAI:
		collector.collectOpenAIChatText(root)
	case sdktranslator.FormatOpenAIResponse, sdktranslator.FormatCodex:
		collector.collectOpenAIResponsesText(root)
	case sdktranslator.FormatClaude:
		collector.collectClaudeText(root)
	case sdktranslator.FormatGemini, sdktranslator.FormatAntigravity:
		collector.collectGeminiText(root)
	default:
		return "", false
	}
	return collector.text()
}

type textCollector struct {
	parts []string
}

func (c *textCollector) add(value gjson.Result) {
	if !value.Exists() || value.Type != gjson.String {
		return
	}
	text := value.String()
	if text == "" {
		return
	}
	c.parts = append(c.parts, text)
}

func (c *textCollector) text() (string, bool) {
	if len(c.parts) == 0 {
		return "", false
	}
	return strings.Join(c.parts, ""), true
}

func (c *textCollector) collectOpenAIChatText(root gjson.Result) {
	choices := root.Get("choices")
	if !choices.IsArray() {
		return
	}
	choices.ForEach(func(_, choice gjson.Result) bool {
		c.add(choice.Get("message.content"))
		c.add(choice.Get("message.reasoning_content"))
		c.add(choice.Get("message.reasoning"))
		c.add(choice.Get("delta.content"))
		c.add(choice.Get("delta.reasoning_content"))
		c.add(choice.Get("delta.reasoning"))
		c.add(choice.Get("text"))
		return true
	})
}

func (c *textCollector) collectOpenAIResponsesText(root gjson.Result) {
	response := root.Get("response")
	if response.Exists() {
		c.collectOpenAIResponsesText(response)
	}

	eventType := root.Get("type").String()
	switch eventType {
	case "response.output_text.delta":
		c.add(root.Get("delta"))
	case "response.output_text.done":
		c.add(root.Get("text"))
	}

	c.add(root.Get("output_text"))
	c.collectOpenAIResponseItems(root.Get("output"))
	c.collectOpenAIResponseItem(root.Get("item"))
	part := root.Get("part")
	if part.Exists() {
		c.add(part.Get("text"))
	}
}

func (c *textCollector) collectOpenAIResponseItems(items gjson.Result) {
	if !items.IsArray() {
		return
	}
	items.ForEach(func(_, item gjson.Result) bool {
		c.collectOpenAIResponseItem(item)
		return true
	})
}

func (c *textCollector) collectOpenAIResponseItem(item gjson.Result) {
	if !item.Exists() {
		return
	}
	content := item.Get("content")
	if content.IsArray() {
		content.ForEach(func(_, part gjson.Result) bool {
			c.add(part.Get("text"))
			return true
		})
	}
	summary := item.Get("summary")
	if summary.IsArray() {
		summary.ForEach(func(_, part gjson.Result) bool {
			c.add(part.Get("text"))
			return true
		})
	}
}

func (c *textCollector) collectClaudeText(root gjson.Result) {
	content := root.Get("content")
	if content.IsArray() {
		content.ForEach(func(_, block gjson.Result) bool {
			c.add(block.Get("text"))
			c.add(block.Get("thinking"))
			return true
		})
	}
	contentBlock := root.Get("content_block")
	if contentBlock.Exists() {
		c.add(contentBlock.Get("text"))
		c.add(contentBlock.Get("thinking"))
	}
	delta := root.Get("delta")
	if delta.Exists() {
		c.add(delta.Get("text"))
		c.add(delta.Get("thinking"))
	}
}

func (c *textCollector) collectGeminiText(root gjson.Result) {
	response := root.Get("response")
	if response.Exists() {
		c.collectGeminiText(response)
	}
	candidates := root.Get("candidates")
	if !candidates.IsArray() {
		return
	}
	candidates.ForEach(func(_, candidate gjson.Result) bool {
		parts := candidate.Get("content.parts")
		if parts.IsArray() {
			parts.ForEach(func(_, part gjson.Result) bool {
				c.add(part.Get("text"))
				return true
			})
		}
		return true
	})
}
