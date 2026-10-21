package otel

import (
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/big"
	"slices"
	"strconv"
	"strings"
)

type delivery struct {
	terminal bool
	rejected int
}

func (e *Exporter) Checkpoint(done []string, save func([]string) error) *Exporter {
	copy := &Exporter{cfg: e.cfg, done: make(map[string]bool, len(done)), save: save}
	for _, key := range done {
		copy.done[key] = true
	}
	return copy
}

func receipt(endpoint, kind, id string) string {
	b, _ := json.Marshal([]string{endpoint, kind, id})
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

func (e *Exporter) spanKey(s span) string {
	return receipt(e.cfg.Traces, "span", s.TraceID+":"+s.SpanID)
}

func (e *Exporter) logKey(ev Evaluation) string {
	return receipt(e.cfg.Logs, "log", ev.Span.uid())
}

func (e *Exporter) complete(keys []string) error {
	if e.save == nil {
		return nil
	}
	for _, key := range keys {
		e.done[key] = true
	}
	done := make([]string, 0, len(e.done))
	for key := range e.done {
		done = append(done, key)
	}
	slices.Sort(done)
	return e.save(done)
}

func response(body io.Reader, protocol Protocol, logs bool, count int) (delivery, error) {
	const limit = 1 << 20
	b, err := io.ReadAll(io.LimitReader(body, limit+1))
	if len(b) > limit {
		return delivery{terminal: true, rejected: count}, errors.Join(ErrRejected, ErrResponse)
	}
	if err != nil {
		return delivery{}, errors.Join(ErrResponse, err)
	}
	if len(b) == 0 {
		return delivery{}, nil
	}
	var rejected int64
	var message bool
	if protocol == ProtocolJSON {
		rejected, message, err = jsonResponse(b, logs)
	} else {
		rejected, message, err = protoResponse(b)
	}
	if err != nil || rejected < 0 || rejected > int64(count) {
		return delivery{}, ErrResponse
	}
	r := delivery{terminal: rejected > 0 || message, rejected: int(rejected)}
	if r.terminal {
		return r, fmt.Errorf("%w: %d rejected", ErrRejected, rejected)
	}
	return r, nil
}

func jsonResponse(b []byte, logs bool) (int64, bool, error) {
	var root map[string]json.RawMessage
	if err := json.Unmarshal(b, &root); err != nil || root == nil {
		return 0, false, ErrResponse
	}
	part := root["partialSuccess"]
	if part == nil {
		part = root["partial_success"]
	}
	if part == nil {
		return 0, false, nil
	}
	var p struct {
		Spans        json.Number `json:"rejectedSpans"`
		Logs         json.Number `json:"rejectedLogRecords"`
		SpansAlias   json.Number `json:"rejected_spans"`
		LogsAlias    json.Number `json:"rejected_log_records"`
		Message      string      `json:"errorMessage"`
		MessageAlias string      `json:"error_message"`
	}
	if err := json.Unmarshal(part, &p); err != nil {
		return 0, false, err
	}
	n := p.Spans
	if n == "" {
		n = p.SpansAlias
	}
	if logs {
		n = p.Logs
		if n == "" {
			n = p.LogsAlias
		}
	}
	var rejected int64
	var err error
	if n != "" {
		rejected, err = integerResponse(n)
	}
	return rejected, p.Message != "" || p.MessageAlias != "", err
}

func integerResponse(n json.Number) (int64, error) {
	if v, err := n.Int64(); err == nil {
		return v, nil
	}
	s := n.String()
	if len(s) > 128 {
		return 0, ErrResponse
	}
	if i := strings.IndexAny(s, "eE"); i >= 0 {
		if strings.Trim(s[:i], "-0.") == "" {
			return 0, nil
		}
		exponent, err := strconv.Atoi(s[i+1:])
		if err != nil || exponent < -128 || exponent > 128 {
			return 0, ErrResponse
		}
	}
	v, ok := new(big.Rat).SetString(s)
	if !ok || !v.IsInt() || !v.Num().IsInt64() {
		return 0, ErrResponse
	}
	return v.Num().Int64(), nil
}

func protoResponse(b []byte) (int64, bool, error) {
	var rejected int64
	var message bool
	err := fields(b, func(field, wire uint64, value []byte, number uint64) error {
		if field != 1 {
			return nil
		}
		if wire != 2 {
			return ErrResponse
		}
		return fields(value, func(field, wire uint64, value []byte, number uint64) error {
			switch field {
			case 1:
				if wire != 0 {
					return ErrResponse
				}
				rejected = int64(number)
			case 2:
				if wire != 2 {
					return ErrResponse
				}
				message = len(value) > 0
			}
			return nil
		})
	})
	return rejected, message, err
}

func fields(b []byte, visit func(uint64, uint64, []byte, uint64) error) error {
	for len(b) > 0 {
		key, n := binary.Uvarint(b)
		if n <= 0 || key>>3 == 0 || key>>3 > (1<<29)-1 {
			return ErrResponse
		}
		b = b[n:]
		wire := key & 7
		var value []byte
		var number uint64
		switch wire {
		case 0:
			number, n = binary.Uvarint(b)
			if n <= 0 {
				return ErrResponse
			}
			b = b[n:]
		case 1, 5:
			n := 8
			if wire == 5 {
				n = 4
			}
			if len(b) < n {
				return ErrResponse
			}
			value, b = b[:n], b[n:]
		case 2:
			size, n := binary.Uvarint(b)
			if n <= 0 || size > uint64(len(b)-n) {
				return ErrResponse
			}
			value, b = b[n:n+int(size)], b[n+int(size):]
		default:
			return ErrResponse
		}
		if err := visit(key>>3, wire, value, number); err != nil {
			return err
		}
	}
	return nil
}
