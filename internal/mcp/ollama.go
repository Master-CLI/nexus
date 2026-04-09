package mcp

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"
)

// OllamaChat provides chat completion via a local Ollama instance.
type OllamaChat struct {
	endpoint   string
	model      string
	httpClient *http.Client
}

// NewOllamaChat creates an Ollama chat client.
func NewOllamaChat(endpoint, model string) *OllamaChat {
	if endpoint == "" {
		endpoint = "http://localhost:11434"
	}
	if model == "" {
		model = "gemma4"
	}
	return &OllamaChat{
		endpoint:   endpoint,
		model:      model,
		httpClient: &http.Client{Timeout: 15 * time.Second},
	}
}

// Available checks if Ollama is reachable.
func (o *OllamaChat) Available(ctx context.Context) bool {
	req, err := http.NewRequestWithContext(ctx, "GET", o.endpoint+"/api/tags", nil)
	if err != nil {
		return false
	}
	resp, err := o.httpClient.Do(req)
	if err != nil {
		return false
	}
	defer resp.Body.Close()
	var result struct {
		Models []struct {
			Name string `json:"name"`
		} `json:"models"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return false
	}
	for _, m := range result.Models {
		if m.Name == o.model || m.Name == o.model+":latest" {
			return true
		}
	}
	return false
}

type ollamaChatMsg struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type ollamaChatStreamChunk struct {
	Message struct {
		Content  string `json:"content"`
		Thinking string `json:"thinking"`
	} `json:"message"`
	Done bool `json:"done"`
}

// QuickClassify runs a fast classification with think=false.
func (o *OllamaChat) QuickClassify(ctx context.Context, systemPrompt, input string) (string, error) {
	think := false
	payload := map[string]any{
		"model": o.model,
		"messages": []ollamaChatMsg{
			{Role: "system", Content: systemPrompt},
			{Role: "user", Content: input},
		},
		"stream":  true,
		"think":   &think,
		"options": map[string]any{"num_predict": 128},
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return "", err
	}

	req, err := http.NewRequestWithContext(ctx, "POST", o.endpoint+"/api/chat", bytes.NewReader(body))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := o.httpClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("ollama: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		b, _ := io.ReadAll(resp.Body)
		return "", fmt.Errorf("ollama %d: %s", resp.StatusCode, string(b))
	}

	var content string
	scanner := bufio.NewScanner(resp.Body)
	scanner.Buffer(make([]byte, 64*1024), 256*1024)
	for scanner.Scan() {
		var chunk ollamaChatStreamChunk
		if err := json.Unmarshal(scanner.Bytes(), &chunk); err != nil {
			continue
		}
		content += chunk.Message.Content
		if chunk.Done {
			break
		}
	}
	return content, nil
}
