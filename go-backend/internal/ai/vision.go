package ai

import (
	"context"
	"fmt"
	"strings"
)

// DescribeImage asks a vision-capable model to describe a single image.
// imageDataURI is a data URI ("data:image/png;base64,...") or an http(s)
// URL. prompt is the instruction; pass "" to use a default.
func DescribeImage(ctx context.Context, c *Client, model, imageDataURI, prompt string) (string, error) {
	if strings.TrimSpace(model) == "" {
		return "", fmt.Errorf("ai.DescribeImage: no model configured")
	}
	if strings.TrimSpace(imageDataURI) == "" {
		return "", fmt.Errorf("ai.DescribeImage: empty image")
	}
	if strings.TrimSpace(prompt) == "" {
		prompt = "Describe this image in detail, including any visible text, tables, charts, and figures."
	}
	req := &ChatRequest{
		Model: model,
		Messages: []ChatMessage{{
			Role:         "user",
			MultiContent: []ChatContentPart{TextPart(prompt), ImageURLPart(imageDataURI)},
		}},
		MaxTokens: 1024,
	}
	resp, err := c.ChatCompletion(ctx, req)
	if err != nil {
		return "", fmt.Errorf("ai.DescribeImage: %w", err)
	}
	if resp == nil || len(resp.Choices) == 0 {
		return "", fmt.Errorf("ai.DescribeImage: empty response")
	}
	return strings.TrimSpace(resp.Choices[0].Message.Content), nil
}
