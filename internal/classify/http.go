package classify

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"
)

// Bytes of an error body a message quotes
const bodyQuote = 200

// Bytes of an answer read at most
// A few questions answer in well under a kilobyte so a larger body is a wrong endpoint and is cut before it fills memory
const bodyLimit = 1 << 20

// A classifier behind the Jev wire format
// laya-serve and OpenRouter decisions and TypeSafe answer the same body so one implementation serves them all
type HTTP struct {
	endpoint Endpoint
	// Sent as a bearer token when not empty
	key    string
	client *http.Client
}

func NewHTTP(endpoint Endpoint, key string, timeout time.Duration) *HTTP {
	return &HTTP{endpoint: endpoint, key: key, client: &http.Client{Timeout: timeout}}
}

type wireQuestion struct {
	Type         string `json:"type"`
	Instructions string `json:"instructions"`
}

type wireRequest struct {
	Model     string                  `json:"model,omitempty"`
	State     string                  `json:"state"`
	Questions map[string]wireQuestion `json:"questions"`
}

type wireResponse struct {
	Answers map[string]struct {
		Noul *float64 `json:"noul"`
	} `json:"answers"`
}

func (h *HTTP) Classify(ctx context.Context, req Request) (Answers, error) {
	questions := make(map[string]wireQuestion, len(req.Questions))
	for name, instructions := range req.Questions {
		questions[name] = wireQuestion{Type: "noul", Instructions: instructions}
	}
	body, err := json.Marshal(wireRequest{Model: h.endpoint.Model, State: req.State, Questions: questions})
	if err != nil {
		return nil, err
	}
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, h.endpoint.URL, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	httpReq.Header.Set("Content-Type", "application/json")
	if h.key != "" {
		httpReq.Header.Set("Authorization", "Bearer "+h.key)
	}
	res, err := h.client.Do(httpReq)
	if err != nil {
		return nil, err
	}
	// A body fully read leaves nothing for Close to report
	defer func() { _ = res.Body.Close() }()
	b, err := io.ReadAll(io.LimitReader(res.Body, bodyLimit))
	if err != nil {
		return nil, err
	}
	if res.StatusCode < 200 || res.StatusCode > 299 {
		return nil, fmt.Errorf("%w: %d %s", ErrStatus, res.StatusCode, b[:min(len(b), bodyQuote)])
	}
	var wire wireResponse
	if err := json.Unmarshal(b, &wire); err != nil {
		return nil, fmt.Errorf("%w: %w", ErrResponseInvalid, err)
	}
	answers := Answers{}
	for name, a := range wire.Answers {
		if a.Noul != nil {
			answers[name] = Answer{Yes: *a.Noul}
		}
	}
	if err := answers.Check(req.Questions); err != nil {
		return nil, err
	}
	return answers, nil
}
