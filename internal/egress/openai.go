package egress

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"time"
)

// maxResponseBytes: trần body response — batch 10×3072 float JSON vượt 1MB nên để 8MB.
const maxResponseBytes = 8 << 20

// openaiClient là client chung (OpenAI-compatible) cho mọi endpoint gateway.
type openaiClient struct {
	base  string
	key   string
	httpc *http.Client
}

// doJSON POST JSON tới base+path; non-2xx → error chứa status + tối đa 500
// ký tự body. Trả kèm số đo (bytes, status, độ trễ, usage) cho meter — kể cả
// khi lỗi.
func (c *openaiClient) doJSON(ctx context.Context, path string, in, out any) (callInfo, error) {
	var ci callInfo
	ci.cached = -1
	if c.base == "" {
		return ci, errors.New("gateway base_url chưa cấu hình ([gateway].base_url hoặc MIND_RUNNER_GATEWAY_BASE_URL)")
	}
	body, err := json.Marshal(in)
	if err != nil {
		return ci, fmt.Errorf("marshal %s: %w", path, err)
	}
	ci.reqBytes = len(body)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.base+path, bytes.NewReader(body))
	if err != nil {
		return ci, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+c.key)
	start := time.Now()
	resp, err := c.httpc.Do(req)
	if err != nil {
		ci.latency = time.Since(start)
		return ci, fmt.Errorf("POST %s: %w", path, err)
	}
	defer resp.Body.Close()
	ci.status = resp.StatusCode
	data, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseBytes+1))
	ci.latency = time.Since(start)
	ci.respBytes = len(data)
	if err != nil {
		return ci, fmt.Errorf("đọc response %s: %w", path, err)
	}
	if len(data) > maxResponseBytes {
		// không cắt lặng lẽ (sẽ thành "unexpected end of JSON" khó hiểu)
		return ci, fmt.Errorf("POST %s: response quá lớn (> %d bytes)", path, maxResponseBytes)
	}
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		if len(data) > 500 {
			data = data[:500]
		}
		return ci, fmt.Errorf("POST %s: HTTP %d: %s", path, resp.StatusCode, data)
	}
	ci.usage, ci.cached, ci.hasUsage = parseUsage(data)
	if out == nil {
		return ci, nil
	}
	if err := json.Unmarshal(data, out); err != nil {
		return ci, fmt.Errorf("parse response %s: %w", path, err)
	}
	return ci, nil
}
