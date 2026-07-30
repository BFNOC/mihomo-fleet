package app

// HTTP handlers for the geodata update and proxy-instance endpoints,
// extracted from controller.go. The update logic itself lives in
// geo_update.go; this file is the HTTP/SSE plumbing only.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strings"
	"time"
)

// handleGeoUpdate serves feature #3's geodata (GeoIP.dat/GeoSite.dat/
// Country.mmdb/ASN.mmdb) check/update. See geo_update.go. POST streams
// download progress as Server-Sent Events (docs/geo-update-enhancements.md
// P1) rather than blocking for the whole multi-file update and returning
// one JSON body -- see streamGeoUpdate.
func (c *Controller) handleGeoUpdate(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		ctx, cancel := context.WithTimeout(r.Context(), 20*time.Second)
		defer cancel()
		writeJSON(w, c.GeoUpdateStatus(ctx))
	case http.MethodPost:
		// Single-flight, mirroring handleCoreUpdate's POST case above --
		// its own mutex, since a core update and a geo update touch
		// disjoint files and have no reason to block each other. Checked
		// (and, on success, released) before any SSE header is written, so
		// a conflict is still reported as a plain 409 JSON error body, not
		// a one-frame event stream.
		if !c.geoUpdateMu.TryLock() {
			writeError(w, http.StatusConflict, errors.New("a geodata update is already in progress"))
			return
		}
		defer c.geoUpdateMu.Unlock()
		c.streamGeoUpdate(w, r)
	default:
		methodNotAllowed(w)
	}
}

// streamGeoUpdate writes ApplyGeoUpdateSSE's events onto the response as
// Server-Sent Events: "event: <type>\ndata: <json>\n\n" per frame, flushed
// immediately after each one so the browser sees progress as it happens
// instead of buffering behind the handler's return. Requires the
// ResponseWriter to implement http.Flusher, true for every response this
// server actually produces (net/http.Server always supports it for a plain
// HTTP/1.1 or HTTP/2 connection) -- the type assertion exists to fail
// loudly rather than silently, not because failure is expected in
// practice.
//
// The request body is optional (P1's original no-proxy shape sends none at
// all) and, when present, may carry a "proxyInstanceId" (P2, docs/
// geo-update-enhancements.md section 3): a managed instance's id to route
// the actual asset downloads through instead of dialing GitHub/its CDN
// directly. That instance is resolved and validated (exists, currently
// running) BEFORE any SSE header is written, so an unknown/stopped instance
// still comes back as a plain 400 JSON error body -- once the stream starts
// there is no way to report an error except as another SSE frame, so this
// ordering matters.
func (c *Controller) streamGeoUpdate(w http.ResponseWriter, r *http.Request) {
	var req struct {
		ProxyInstanceID string `json:"proxyInstanceId"`
	}
	// An empty body (P1's original no-proxy request, and every request from
	// before this feature existed) is not malformed -- readJSON's
	// json.Decoder.Decode returns a bare io.EOF for it, same reasoning as
	// handleClone's all-defaults empty body above.
	if err := readJSON(r, &req); err != nil && !errors.Is(err, io.EOF) {
		writeError(w, http.StatusBadRequest, err)
		return
	}

	var downloadClient *http.Client
	if id := strings.TrimSpace(req.ProxyInstanceID); id != "" {
		client, err := c.proxyClientForInstance(id)
		if err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
		}
		downloadClient = client
	}

	flusher, ok := w.(http.Flusher)
	if !ok {
		writeError(w, http.StatusInternalServerError, errors.New("streaming not supported"))
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.WriteHeader(http.StatusOK)
	flusher.Flush()

	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Minute)
	defer cancel()

	writeSSEEvent := func(evt GeoDownloadEvent) {
		data, err := json.Marshal(evt)
		if err != nil {
			return
		}
		fmt.Fprintf(w, "event: %s\ndata: %s\n\n", evt.Event, data)
		flusher.Flush()
	}

	if err := c.ApplyGeoUpdateSSE(ctx, downloadClient, writeSSEEvent); err != nil {
		// The release list itself could not even be fetched -- no per-file
		// events were ever sent (see ApplyGeoUpdateSSE's doc comment), so
		// this IS the only frame the client gets. Reported through the same
		// "complete" event shape (Errors populated, Updated absent) rather
		// than inventing a distinct wire event type just for this case.
		writeSSEEvent(GeoDownloadEvent{Event: "complete", Errors: []string{err.Error()}})
	}
}

// handleProxyInstances serves P2's proxy-instance picker (docs/
// geo-update-enhancements.md section 3): the frontend's download-source
// dropdown only needs to know which instances are currently eligible to
// proxy through (running ones -- proxyClientForInstance rejects anything
// else) and their mixed-port, so this deliberately returns a narrower shape
// than GET /api/instances rather than making the frontend re-derive
// "running" from the full InstanceView list.
func (c *Controller) handleProxyInstances(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		methodNotAllowed(w)
		return
	}
	views := c.manager.Views()
	instances := make([]ProxyInstanceOption, 0, len(views))
	for _, view := range views {
		if view.Status != "running" {
			continue
		}
		instances = append(instances, ProxyInstanceOption{
			ID:        view.ID,
			Name:      view.Name,
			MixedPort: view.MixedPort,
		})
	}
	sort.Slice(instances, func(i, j int) bool {
		return instances[i].Name < instances[j].Name
	})
	writeJSON(w, map[string]any{"instances": instances})
}
