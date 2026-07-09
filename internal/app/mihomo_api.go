package app

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

const (
	defaultLatencyURL       = "http://cp.cloudflare.com/generate_204"
	defaultLatencyTimeoutMS = 10000
	minLatencyTimeoutMS     = 500
	maxLatencyTimeoutMS     = 15000
)

// mihomoAPIClient is shared by putMihomoProxy and doMihomoDelayRequest, the
// two call sites that previously built a fresh http.Client (and therefore a
// fresh Transport, with no connection reuse) on every single call (see M3 in
// REVIEW-2026-07-04.md). Both always talk to a local mihomo controller
// (127.0.0.1:<port>), so pooling connections in a shared Transport is a
// straightforward win, especially for batches of delay-test requests.
//
// No Client.Timeout is set here: each call site instead bounds its own
// request via context.WithTimeout (see below), preserving the exact per-call
// timeout semantics the old per-call http.Client{Timeout: ...} values gave --
// a client-level Timeout would apply globally to every caller and could not
// express putMihomoProxy's fixed 2s budget alongside doMihomoDelayRequest's
// caller-supplied budget.
var mihomoAPIClient = &http.Client{}

func putMihomoProxy(item *Instance, group, proxy string) error {
	endpoint := "http://127.0.0.1:" + strconv.Itoa(item.ControllerPort) + "/proxies/" + url.PathEscape(group)
	body, err := json.Marshal(map[string]string{"name": proxy})
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPut, endpoint, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+item.Secret)
	req.Header.Set("Content-Type", "application/json")
	res, err := mihomoAPIClient.Do(req)
	if err != nil {
		return err
	}
	defer res.Body.Close()
	if res.StatusCode < 200 || res.StatusCode >= 300 {
		body, _ := io.ReadAll(io.LimitReader(res.Body, 4096))
		if len(body) == 0 {
			return fmt.Errorf("mihomo returned %s", res.Status)
		}
		return fmt.Errorf("mihomo returned %s: %s", res.Status, bytes.TrimSpace(body))
	}
	return nil
}

func mihomoProxyDelay(ctx context.Context, item *Instance, proxyName, testURL string, timeoutMS int) (int, error) {
	values := url.Values{}
	values.Set("timeout", strconv.Itoa(timeoutMS))
	values.Set("url", testURL)
	endpoint := "http://127.0.0.1:" + strconv.Itoa(item.ControllerPort) + "/proxies/" + url.PathEscape(proxyName) + "/delay?" + values.Encode()

	var payload struct {
		Delay int `json:"delay"`
	}
	if err := doMihomoDelayRequest(ctx, item, endpoint, timeoutMS+2000, &payload); err != nil {
		if errors.Is(err, errMihomoDelayTestFailure) {
			return 0, nil
		}
		return 0, err
	}
	return payload.Delay, nil
}

func mihomoRealProxyDelay(ctx context.Context, item *Instance, proxyName, testURL string, timeoutMS int) (int, error) {
	best := 0
	for i := 0; i < 2; i++ {
		delay, err := mihomoProxyDelay(ctx, item, proxyName, testURL, timeoutMS)
		if err != nil {
			return 0, err
		}
		if delay <= 0 {
			return 0, nil
		}
		if best == 0 || delay < best {
			best = delay
		}
		if i == 0 {
			if err := sleepWithContext(ctx, 100*time.Millisecond); err != nil {
				return 0, err
			}
		}
	}
	return best, nil
}

func mihomoGroupDelay(ctx context.Context, item *Instance, groupName, testURL string, timeoutMS int) (map[string]int, error) {
	values := url.Values{}
	values.Set("timeout", strconv.Itoa(timeoutMS))
	values.Set("url", testURL)
	endpoint := "http://127.0.0.1:" + strconv.Itoa(item.ControllerPort) + "/group/" + url.PathEscape(groupName) + "/delay?" + values.Encode()

	var delays map[string]int
	if err := doMihomoDelayRequest(ctx, item, endpoint, latencyRequestBudgetMS("url", timeoutMS), &delays); err != nil {
		if errors.Is(err, errMihomoDelayTestFailure) {
			return map[string]int{}, nil
		}
		return nil, err
	}
	return delays, nil
}

func doMihomoDelayRequest(ctx context.Context, item *Instance, endpoint string, requestTimeoutMS int, out any) error {
	// Deriving a child context from the caller-supplied ctx reproduces the
	// previous http.Client{Timeout: requestTimeoutMS} exactly: that Timeout
	// raced the request against ctx's own deadline and enforced whichever was
	// shorter, which is exactly what context.WithTimeout on top of ctx does
	// here.
	ctx, cancel := context.WithTimeout(ctx, time.Duration(requestTimeoutMS)*time.Millisecond)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+item.Secret)

	res, err := mihomoAPIClient.Do(req)
	if err != nil {
		return err
	}
	defer res.Body.Close()
	if res.StatusCode < 200 || res.StatusCode >= 300 {
		body, _ := io.ReadAll(io.LimitReader(res.Body, 4096))
		if isMihomoDelayTestFailure(res.StatusCode, body) {
			return errMihomoDelayTestFailure
		}
		if len(body) == 0 {
			return fmt.Errorf("mihomo returned %s", res.Status)
		}
		return fmt.Errorf("mihomo returned %s: %s", res.Status, bytes.TrimSpace(body))
	}
	if err := json.NewDecoder(io.LimitReader(res.Body, 1<<20)).Decode(out); err != nil {
		return err
	}
	return nil
}

var errMihomoDelayTestFailure = errors.New("mihomo delay test failed")

func isMihomoDelayTestFailure(statusCode int, body []byte) bool {
	if statusCode != http.StatusServiceUnavailable && statusCode != http.StatusGatewayTimeout {
		return false
	}
	if len(bytes.TrimSpace(body)) == 0 {
		return true
	}
	var payload struct {
		Message string `json:"message"`
	}
	if err := json.Unmarshal(body, &payload); err != nil {
		return false
	}
	message := strings.ToLower(strings.TrimSpace(payload.Message))
	return strings.Contains(message, "delay test") || (statusCode == http.StatusGatewayTimeout && message == "timeout")
}

func normalizeLatencyRequestURL(raw string) (string, error) {
	if raw == "" {
		return defaultLatencyURL, nil
	}
	parsed, err := url.Parse(raw)
	if err != nil || parsed.Host == "" || (parsed.Scheme != "http" && parsed.Scheme != "https") {
		return "", fmt.Errorf("latency test URL must start with http:// or https://")
	}
	return raw, nil
}

func clampLatencyTimeoutMS(timeoutMS int) int {
	if timeoutMS <= 0 {
		return defaultLatencyTimeoutMS
	}
	if timeoutMS < minLatencyTimeoutMS {
		return minLatencyTimeoutMS
	}
	if timeoutMS > maxLatencyTimeoutMS {
		return maxLatencyTimeoutMS
	}
	return timeoutMS
}

func latencyRequestBudgetMS(kind string, timeoutMS int) int {
	if kind == "real" {
		return timeoutMS*2 + 5000
	}
	return timeoutMS + 5000
}

func sleepWithContext(ctx context.Context, duration time.Duration) error {
	timer := time.NewTimer(duration)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}
