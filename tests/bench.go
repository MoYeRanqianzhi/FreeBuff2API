package main

import (
	"bufio"
	"bytes"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

type BenchResult struct {
	Label        string        `json:"label"`
	Model        string        `json:"model"`
	Stream       bool          `json:"stream"`
	PromptLen    int           `json:"prompt_tokens_est"`
	HTTPStatus   int           `json:"http_status"`
	TTFT         time.Duration `json:"ttft_ms"`
	TotalTime    time.Duration `json:"total_ms"`
	ChunkCount   int           `json:"chunk_count"`
	OutputTokens int           `json:"completion_tokens"`
	PromptTokens int           `json:"prompt_tokens"`
	ErrorBody    string        `json:"error_body,omitempty"`
}

type ChatRequest struct {
	Model     string        `json:"model"`
	Messages  []ChatMessage `json:"messages"`
	Stream    bool          `json:"stream"`
	MaxTokens int           `json:"max_tokens"`
}

type ChatMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type SSEChunk struct {
	Choices []struct {
		Delta struct {
			Content string `json:"content"`
		} `json:"delta"`
	} `json:"choices"`
	Usage *struct {
		PromptTokens     int `json:"prompt_tokens"`
		CompletionTokens int `json:"completion_tokens"`
	} `json:"usage,omitempty"`
}

type NonStreamResp struct {
	Choices []struct {
		Message struct {
			Content string `json:"content"`
		} `json:"message"`
	} `json:"choices"`
	Usage struct {
		PromptTokens     int `json:"prompt_tokens"`
		CompletionTokens int `json:"completion_tokens"`
	} `json:"usage"`
}

func main() {
	endpoint := flag.String("url", "http://localhost:8789/v1/chat/completions", "proxy url")
	flag.Parse()

	cases := []struct {
		label     string
		model     string
		stream    bool
		prompt    string
		maxTokens int
	}{
		{"nonstream-short-gemini", "google/gemini-2.5-flash", false, "用一句话介绍 Go 语言", 80},
		{"nonstream-short-haiku", "anthropic/claude-3.5-haiku-20241022", false, "用一句话介绍 Go 语言", 80},
		{"stream-short-gemini", "google/gemini-2.5-flash", true, "用一句话介绍 Go 语言", 80},
		{"stream-short-haiku", "anthropic/claude-3.5-haiku-20241022", true, "用一句话介绍 Go 语言", 80},
		{"stream-short-sonnet", "anthropic/claude-sonnet-4", true, "用一句话介绍 Go 语言", 80},
		{"stream-medium-gemini", "google/gemini-2.5-flash", true, strings.Repeat("Go 是一门由 Google 开发的静态类型编译型语言。", 10) + "\n\n请简要总结上述内容。", 150},
		{"stream-medium-haiku", "anthropic/claude-3.5-haiku-20241022", true, strings.Repeat("Go 是一门由 Google 开发的静态类型编译型语言。", 10) + "\n\n请简要总结上述内容。", 150},
	}

	results := make([]BenchResult, 0, len(cases))
	for _, c := range cases {
		fmt.Printf("[RUN] %s ... ", c.label)
		r := runOne(*endpoint, c.label, c.model, c.stream, c.prompt, c.maxTokens)
		results = append(results, r)
		if r.HTTPStatus == 200 {
			fmt.Printf("OK ttft=%v total=%v chunks=%d tokens=%d/%d\n",
				r.TTFT.Round(time.Millisecond), r.TotalTime.Round(time.Millisecond),
				r.ChunkCount, r.PromptTokens, r.OutputTokens)
		} else {
			fmt.Printf("FAIL http=%d err=%.120s\n", r.HTTPStatus, r.ErrorBody)
		}
		time.Sleep(500 * time.Millisecond)
	}

	out, _ := json.MarshalIndent(results, "", "  ")
	fmt.Println("\n=== RESULTS JSON ===")
	fmt.Println(string(out))
}

func runOne(endpoint, label, model string, stream bool, prompt string, maxTokens int) BenchResult {
	reqBody := ChatRequest{
		Model:     model,
		Messages:  []ChatMessage{{Role: "user", Content: prompt}},
		Stream:    stream,
		MaxTokens: maxTokens,
	}
	payload, _ := json.Marshal(reqBody)

	result := BenchResult{
		Label:     label,
		Model:     model,
		Stream:    stream,
		PromptLen: len(prompt),
	}

	httpReq, _ := http.NewRequest(http.MethodPost, endpoint, bytes.NewReader(payload))
	httpReq.Header.Set("Content-Type", "application/json")

	t0 := time.Now()
	client := &http.Client{Timeout: 120 * time.Second}
	resp, err := client.Do(httpReq)
	if err != nil {
		result.ErrorBody = err.Error()
		result.TotalTime = time.Since(t0)
		return result
	}
	defer resp.Body.Close()
	result.HTTPStatus = resp.StatusCode

	if resp.StatusCode != 200 {
		body, _ := io.ReadAll(resp.Body)
		result.ErrorBody = string(body)
		result.TotalTime = time.Since(t0)
		return result
	}

	if !stream {
		body, _ := io.ReadAll(resp.Body)
		result.TotalTime = time.Since(t0)
		result.TTFT = result.TotalTime
		var r NonStreamResp
		if err := json.Unmarshal(body, &r); err == nil {
			result.PromptTokens = r.Usage.PromptTokens
			result.OutputTokens = r.Usage.CompletionTokens
		}
		return result
	}

	scanner := bufio.NewScanner(resp.Body)
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)

	firstContent := false
	for scanner.Scan() {
		line := scanner.Text()
		if !strings.HasPrefix(line, "data: ") {
			continue
		}
		data := strings.TrimPrefix(line, "data: ")
		if strings.TrimSpace(data) == "[DONE]" {
			break
		}
		result.ChunkCount++
		var ch SSEChunk
		if err := json.Unmarshal([]byte(data), &ch); err != nil {
			continue
		}
		if !firstContent {
			for _, c := range ch.Choices {
				if c.Delta.Content != "" {
					result.TTFT = time.Since(t0)
					firstContent = true
					break
				}
			}
		}
		if ch.Usage != nil {
			result.PromptTokens = ch.Usage.PromptTokens
			result.OutputTokens = ch.Usage.CompletionTokens
		}
	}
	result.TotalTime = time.Since(t0)
	return result
}
