package app

import (
	"net/http/httptest"
	"strings"
	"testing"
)

func TestReadJSONAcceptsSingleValueAndTrailingWhitespace(t *testing.T) {
	var payload struct {
		Name string `json:"name"`
	}
	req := httptest.NewRequest("POST", "/", strings.NewReader("{\"name\":\"fleet\"} \n\t"))
	if err := readJSON(req, &payload); err != nil {
		t.Fatalf("readJSON() error = %v", err)
	}
	if payload.Name != "fleet" {
		t.Fatalf("Name = %q, want fleet", payload.Name)
	}
}

func TestReadJSONRejectsMultipleValues(t *testing.T) {
	var payload map[string]any
	req := httptest.NewRequest("POST", "/", strings.NewReader(`{"name":"fleet"}{"name":"other"}`))
	if err := readJSON(req, &payload); err == nil || !strings.Contains(err.Error(), "exactly one JSON value") {
		t.Fatalf("readJSON() error = %v, want multiple-value rejection", err)
	}
}

func TestReadJSONRejectsTrailingGarbage(t *testing.T) {
	var payload map[string]any
	req := httptest.NewRequest("POST", "/", strings.NewReader(`{"name":"fleet"} trailing`))
	if err := readJSON(req, &payload); err == nil || !strings.Contains(err.Error(), "invalid trailing data") {
		t.Fatalf("readJSON() error = %v, want trailing-data rejection", err)
	}
}

func TestReadJSONAcceptsBodyAtSizeLimit(t *testing.T) {
	want := strings.Repeat("a", maxJSONBodyBytes-2)
	var payload string
	req := httptest.NewRequest("POST", "/", strings.NewReader(`"`+want+`"`))
	if err := readJSON(req, &payload); err != nil {
		t.Fatalf("readJSON() error = %v", err)
	}
	if payload != want {
		t.Fatalf("decoded payload length = %d, want %d", len(payload), len(want))
	}
}

func TestReadJSONRejectsOversizedBody(t *testing.T) {
	var payload map[string]any
	body := `"` + strings.Repeat("a", maxJSONBodyBytes) + `"`
	req := httptest.NewRequest("POST", "/", strings.NewReader(body))
	if err := readJSON(req, &payload); err == nil || !strings.Contains(err.Error(), "exceeds") {
		t.Fatalf("readJSON() error = %v, want size-limit rejection", err)
	}
}
