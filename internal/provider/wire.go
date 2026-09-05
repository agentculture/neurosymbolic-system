package provider

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
)

// callError is a classified failure from one HTTP round trip. reason is
// always one of the drop reasons this package names: "timeout", "http-<n>" or
// "malformed".
type callError struct {
	reason string
	detail string
}

func (e *callError) Error() string { return fmt.Sprintf("%s: %s", e.reason, e.detail) }

func timeoutErr(detail string) error   { return &callError{reason: "timeout", detail: detail} }
func malformedErr(detail string) error { return &callError{reason: "malformed", detail: detail} }
func httpStatusErr(status int, detail string) error {
	return &callError{reason: fmt.Sprintf("http-%d", status), detail: detail}
}

// reasonOf reduces any error from this package's HTTP calls to its drop
// reason token. A non-callError (should not happen given doJSON's contract)
// is conservatively reported as "timeout" — every transport-level failure
// this package can produce that is not a bad status or a bad body is a
// reachability problem, and "timeout" is the one reason token the contract
// names for that class.
func reasonOf(err error) string {
	var ce *callError
	if e, ok := err.(*callError); ok {
		ce = e
	}
	if ce != nil {
		return ce.reason
	}
	return "timeout"
}

// doJSON POSTs body as JSON to url, decodes the response into out, and
// classifies any failure as timeout / http-<status> / malformed. It never
// panics and never leaks a raw transport error past a *callError.
func doJSON(ctx context.Context, client *http.Client, url string, apiKey string, body, out any) error {
	encoded, err := json.Marshal(body)
	if err != nil {
		return malformedErr("encoding request: " + err.Error())
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(encoded))
	if err != nil {
		return timeoutErr("building request: " + err.Error())
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	if apiKey != "" && apiKey != "EMPTY" {
		req.Header.Set("Authorization", "Bearer "+apiKey)
	}

	resp, err := client.Do(req)
	if err != nil {
		return timeoutErr(err.Error())
	}
	defer func() { _, _ = io.Copy(io.Discard, resp.Body); _ = resp.Body.Close() }()

	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		return timeoutErr("reading response: " + err.Error())
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return httpStatusErr(resp.StatusCode, string(raw))
	}
	if err := json.Unmarshal(raw, out); err != nil {
		return malformedErr("decoding response: " + err.Error())
	}
	return nil
}

// -- embeddings wire shape --------------------------------------------------

type embeddingRequest struct {
	Model string `json:"model"`
	Input string `json:"input"`
}

type embeddingResponse struct {
	Data []struct {
		Embedding []float64 `json:"embedding"`
	} `json:"data"`
}

func fetchEmbedding(
	ctx context.Context, client *http.Client, baseURL, apiKey, model, input string,
) ([]float64, error) {
	var resp embeddingResponse
	err := doJSON(ctx, client, baseURL+"/v1/embeddings", apiKey,
		embeddingRequest{Model: model, Input: input}, &resp)
	if err != nil {
		return nil, err
	}
	if len(resp.Data) == 0 || len(resp.Data[0].Embedding) == 0 {
		return nil, malformedErr("response carries no data[0].embedding")
	}
	return resp.Data[0].Embedding, nil
}

// -- chat completions wire shape ---------------------------------------------

type chatMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type completionRequest struct {
	Model     string        `json:"model"`
	Messages  []chatMessage `json:"messages"`
	MaxTokens int           `json:"max_tokens,omitempty"`
}

type completionResponse struct {
	Choices []struct {
		Message chatMessage `json:"message"`
	} `json:"choices"`
}

func fetchCompletion(
	ctx context.Context, client *http.Client, baseURL, apiKey, model, system, user string, maxTokens int,
) (string, error) {
	var resp completionResponse
	err := doJSON(ctx, client, baseURL+"/v1/chat/completions", apiKey, completionRequest{
		Model: model,
		Messages: []chatMessage{
			{Role: "system", Content: system},
			{Role: "user", Content: user},
		},
		MaxTokens: maxTokens,
	}, &resp)
	if err != nil {
		return "", err
	}
	if len(resp.Choices) == 0 {
		return "", malformedErr("response carries no choices[0]")
	}
	content := resp.Choices[0].Message.Content
	if content == "" {
		return "", malformedErr("response carries an empty choices[0].message.content")
	}
	return content, nil
}
