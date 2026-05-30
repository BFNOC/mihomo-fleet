package app

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"time"
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
