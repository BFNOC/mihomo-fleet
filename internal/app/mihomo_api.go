package app

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"time"
)

const (
	defaultLatencyURL       = "https://www.gstatic.com/generate_204"
	defaultLatencyTimeoutMS = 5000
	minLatencyTimeoutMS     = 500
	maxLatencyTimeoutMS     = 15000
)

func putMihomoProxy(item *Instance, group, proxy string) error {
	endpoint := "http://127.0.0.1:" + strconv.Itoa(item.ControllerPort) + "/proxies/" + url.PathEscape(group)
	body, err := json.Marshal(map[string]string{"name": proxy})
	if err != nil {
		return err
	}
	req, err := http.NewRequest(http.MethodPut, endpoint, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+item.Secret)
	req.Header.Set("Content-Type", "application/json")
	client := http.Client{Timeout: 2 * time.Second}
	res, err := client.Do(req)
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
		return nil, err
	}
	return delays, nil
}

func doMihomoDelayRequest(ctx context.Context, item *Instance, endpoint string, requestTimeoutMS int, out any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+item.Secret)

	client := http.Client{Timeout: time.Duration(requestTimeoutMS) * time.Millisecond}
	res, err := client.Do(req)
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
	if err := json.NewDecoder(io.LimitReader(res.Body, 1<<20)).Decode(out); err != nil {
		return err
	}
	return nil
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
