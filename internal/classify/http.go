package classify

import (
	"bytes"
	"cmp"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"
)

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

// A redirect is answered as the status it carries
// Following a 307 or a 308 would send the person's words or the anchor to an endpoint no point names
func NewHTTP(endpoint Endpoint, key string, timeout time.Duration) *HTTP {
	return &HTTP{endpoint: endpoint, key: key, client: &http.Client{
		Timeout: timeout, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}}
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
	// The model that answered when the endpoint names one
	Model   string `json:"model"`
	Answers map[string]struct {
		Noul *float64 `json:"noul"`
	} `json:"answers"`
}

func (h *HTTP) Classify(ctx context.Context, req Request) (Answers, error) {
	reply, err := h.Reply(ctx, req)
	return reply.Answers, err
}

// The answers with the model the endpoint answered with and else the model it was sent
func (h *HTTP) Reply(ctx context.Context, req Request) (Reply, error) {
	answers, model, err := h.answers(ctx, req)
	return Reply{Answers: answers, Model: cmp.Or(model, h.endpoint.Model)}, err
}

func (h *HTTP) answers(ctx context.Context, req Request) (Answers, string, error) {
	questions := make(map[string]wireQuestion, len(req.Questions))
	for name, instructions := range req.Questions {
		questions[name] = wireQuestion{Type: "noul", Instructions: instructions}
	}
	// Strings alone always encode
	body, _ := json.Marshal(wireRequest{Model: h.endpoint.Model, State: req.State, Questions: questions})
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, h.endpoint.URL, bytes.NewReader(body))
	if err != nil {
		return nil, "", err
	}
	httpReq.Header.Set("Content-Type", "application/json")
	if h.key != "" {
		httpReq.Header.Set("Authorization", "Bearer "+h.key)
	}
	res, err := h.client.Do(httpReq)
	if err != nil {
		return nil, "", err
	}
	// A body fully read leaves nothing for Close to report
	defer func() { _ = res.Body.Close() }()
	b, err := io.ReadAll(io.LimitReader(res.Body, bodyLimit))
	if err != nil {
		return nil, "", err
	}
	// The error names the status alone since the classify trace keeps it
	// and an endpoint that echoes its request would put the state in a field no export strips
	if res.StatusCode < 200 || res.StatusCode > 299 {
		return nil, "", fmt.Errorf("%w: %d", ErrStatus, res.StatusCode)
	}
	var wire wireResponse
	if err := json.Unmarshal(b, &wire); err != nil {
		return nil, "", fmt.Errorf("%w: %w", ErrResponseInvalid, err)
	}
	// Only the questions asked are kept so an answer to another never makes the endpoint unsure or reaches a record
	answers := Answers{}
	for name := range req.Questions {
		if a := wire.Answers[name]; a.Noul != nil {
			answers[name] = Answer{Yes: *a.Noul}
		}
	}
	if err := answers.check(req.Questions); err != nil {
		return nil, "", err
	}
	return answers, wire.Model, nil
}
