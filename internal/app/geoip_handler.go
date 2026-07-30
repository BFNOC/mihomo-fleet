package app

import (
	"fmt"
	"log"
	"net/http"
	"net/netip"
	"os"
	"strings"
	"sync"
	"time"
)

// geoLookup holds the lazily-opened country database behind /api/geoip. The
// file is the same Country.mmdb the instances already get staged (geodata.go),
// it is several megabytes, and the dashboard asks for a batch on every poll --
// so it is opened once and kept. Re-stat'ing on an interval is what lets a user
// drop in a fresher database without restarting the fleet.
type geoLookup struct {
	mu      sync.Mutex
	db      *geoDB
	path    string
	modTime time.Time
	checked time.Time
}

const geoStatInterval = 30 * time.Second

// The dashboard only asks about the addresses it is currently showing, so the
// cap exists to bound a hand-rolled request, not the UI.
const geoBatchLimit = 256

// Country.mmdb is whatever the user gave mihomo, and the popular mihomo builds
// of it answer with vendor tags ("GOOGLE", "CLOUDFLARE") where an ISO code
// belongs -- fine for GEOIP rules, useless as a country column. A stock
// GeoLite2-Country.mmdb dropped in beside it therefore wins the lookup here,
// without changing which file gets staged into instance directories and so
// without changing how any GEOIP rule resolves.
var geoDatabaseNames = []string{"GeoLite2-Country.mmdb", "Country.mmdb", "country.mmdb"}

// handleGeoIP resolves a batch of destination addresses to ISO country codes.
// Everything is answered from the local database -- no address a user is
// connecting to ever leaves the machine, which rules out the obvious
// alternative of proxying a public geolocation API.
func (c *Controller) handleGeoIP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		methodNotAllowed(w)
		return
	}
	var payload struct {
		IPs []string `json:"ips"`
	}
	if err := readJSON(r, &payload); err != nil {
		writeError(w, http.StatusBadRequest, fmt.Errorf("invalid geoip request: %w", err))
		return
	}
	if len(payload.IPs) > geoBatchLimit {
		payload.IPs = payload.IPs[:geoBatchLimit]
	}
	db := c.geoDatabase()
	// Addresses with no answer are simply absent from the map; the caller
	// remembers that as "asked, nothing there" and stops asking.
	countries := make(map[string]string, len(payload.IPs))
	if db != nil {
		for _, raw := range payload.IPs {
			addr, err := netip.ParseAddr(strings.TrimSpace(raw))
			if err != nil {
				continue
			}
			if code, ok := db.lookupCountry(addr); ok {
				countries[raw] = code
			}
		}
	}
	writeJSON(w, map[string]any{"available": db != nil, "countries": countries})
}

// geoDatabase returns the open country database, opening or reopening it when
// the file appears, changes, or goes away. A missing or malformed file is a
// normal state (the database is optional and user-supplied), so it yields nil
// rather than an error -- the geo column just stays empty.
func (c *Controller) geoDatabase() *geoDB {
	c.geo.mu.Lock()
	defer c.geo.mu.Unlock()
	now := time.Now()
	if !c.geo.checked.IsZero() && now.Sub(c.geo.checked) < geoStatInterval {
		return c.geo.db
	}
	c.geo.checked = now
	path := findGeodataSource(c.manager.geodataSourceDirs(), geoDatabaseNames)
	if path == "" {
		c.geo.db, c.geo.path, c.geo.modTime = nil, "", time.Time{}
		return nil
	}
	info, err := os.Stat(path)
	if err != nil {
		c.geo.db, c.geo.path, c.geo.modTime = nil, "", time.Time{}
		return nil
	}
	if c.geo.db != nil && path == c.geo.path && info.ModTime().Equal(c.geo.modTime) {
		return c.geo.db
	}
	db, err := openGeoDB(path)
	if err != nil {
		log.Printf("geoip: %s unusable: %v", path, err)
		c.geo.db, c.geo.path, c.geo.modTime = nil, "", time.Time{}
		return nil
	}
	c.geo.db, c.geo.path, c.geo.modTime = db, path, info.ModTime()
	return db
}
