// Package embed provides an OpenAI-compatible embedding client used when an
// index is created with AI categorization enabled. The same /embeddings
// endpoint is exposed by OpenAI and by Ollama's OpenAI-compatible API (base URL
// typically ".../v1").
package embed

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"net/http"
	"os"
	"time"
)

// EmbeddingClient calls an OpenAI-compatible /embeddings endpoint.
type EmbeddingClient struct {
	baseURL string
	apiKey  string
	model   string
	http    *http.Client
}

func NewEmbeddingClient(baseURL, apiKey, model string) *EmbeddingClient {
	return &EmbeddingClient{
		baseURL: baseURL,
		apiKey:  apiKey,
		model:   model,
		http:    &http.Client{Timeout: 60 * time.Second},
	}
}

// NewEmbeddingClientFromEnv builds a client from environment variables:
// SEARCH52_EMBEDDING_BASE_URL and SEARCH52_EMBEDDING_MODEL are required,
// SEARCH52_EMBEDDING_API_KEY is optional (e.g. local Ollama).
func NewEmbeddingClientFromEnv() (*EmbeddingClient, error) {
	baseURL := os.Getenv("SEARCH52_EMBEDDING_BASE_URL")
	model := os.Getenv("SEARCH52_EMBEDDING_MODEL")
	if baseURL == "" || model == "" {
		return nil, fmt.Errorf("SEARCH52_EMBEDDING_BASE_URL and SEARCH52_EMBEDDING_MODEL must be set to enable AI categorization")
	}
	return NewEmbeddingClient(baseURL, os.Getenv("SEARCH52_EMBEDDING_API_KEY"), model), nil
}

type embeddingRequest struct {
	Input string `json:"input"`
	Model string `json:"model"`
}

type embeddingResponse struct {
	Data []struct {
		Embedding []float32 `json:"embedding"`
	} `json:"data"`
}

// Embed returns the embedding vector for text, retrying transient failures with
// exponential backoff. Vectors are float32 end-to-end: embedding similarities
// don't need float64 precision and the smaller footprint halves scan bandwidth.
func (c *EmbeddingClient) Embed(ctx context.Context, text string) ([]float32, error) {
	req := embeddingRequest{Input: text, Model: c.model}
	var resp embeddingResponse
	err := retry(ctx, 3, func() error {
		return postJSON(ctx, c.http, c.baseURL+"/embeddings", c.apiKey, req, &resp)
	})
	if err != nil {
		return nil, err
	}
	if len(resp.Data) == 0 {
		return nil, fmt.Errorf("empty data in embedding response")
	}
	return resp.Data[0].Embedding, nil
}

func postJSON(ctx context.Context, client *http.Client, url, apiKey string, body, out any) error {
	data, err := json.Marshal(body)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(data))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	if apiKey != "" {
		req.Header.Set("Authorization", "Bearer "+apiKey)
	}

	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 400 {
		b, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("API error %d: %s", resp.StatusCode, string(b))
	}
	return json.NewDecoder(resp.Body).Decode(out)
}

func retry(ctx context.Context, attempts int, fn func() error) error {
	var err error
	for i := 0; i < attempts; i++ {
		if err = fn(); err == nil {
			return nil
		}
		if i < attempts-1 {
			wait := time.Duration(math.Pow(2, float64(i))) * time.Second
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(wait):
			}
		}
	}
	return err
}
