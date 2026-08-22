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

// The ASN database geo_update.go already downloads and stages, under both the
// name it is published with upstream and the canonical name the updater
// installs it as. Nothing else reads it -- mihomo uses it for ASN rules, this
// handler for the connections table's network column.
var asnDatabaseNames = []string{"GeoLite2-ASN.mmdb", "ASN.mmdb", "asn.mmdb"}

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
	asnDB := c.asnDatabase()
	// Addresses with no answer are simply absent from the map; the caller
	// remembers that as "asked, nothing there" and stops asking.
	countries := make(map[string]string, len(payload.IPs))
	asns := make(map[string]ASNRecord, len(payload.IPs))
	for _, raw := range payload.IPs {
		if db == nil && asnDB == nil {
			break
		}
		addr, err := netip.ParseAddr(strings.TrimSpace(raw))
		if err != nil {
			continue
		}
		if db != nil {
			if code, ok := db.lookupCountry(addr); ok {
				countries[raw] = code
			}
		}
		if asnDB != nil {
			if record, ok := asnDB.lookupASN(addr); ok {
				asns[raw] = record
			}
		}
	}
	// `available` keeps its original meaning (the country database) so the
	// field is not silently redefined; `asnAvailable` is its own flag. The
	// frontend stops asking only when both are false -- one database present
	// without the other is a normal deployment, not a reason to blank the
	// column that does work.
	writeJSON(w, map[string]any{
		"available":    db != nil,
		"countries":    countries,
		"asnAvailable": asnDB != nil,
		"asns":         asns,
	})
}

// geoDatabase returns the open country database, opening or reopening it when
// the file appears, changes, or goes away. A missing or malformed file is a
// normal state (the database is optional and user-supplied), so it yields nil
// rather than an error -- the geo column just stays empty.
func (c *Controller) geoDatabase() *geoDB {
	return c.openStagedDatabase(&c.geo, geoDatabaseNames)
}

// asnDatabase is geoDatabase for the ASN file. Separate handle, same lifecycle:
// a fleet with no ASN database staged simply answers no ASN for every address.
func (c *Controller) asnDatabase() *geoDB {
	return c.openStagedDatabase(&c.asn, asnDatabaseNames)
}

// openStagedDatabase holds the stat-and-reopen logic both lookups share. The
// interval check is per-handle, so one database going missing never resets the
// other's timer.
func (c *Controller) openStagedDatabase(lookup *geoLookup, names []string) *geoDB {
	lookup.mu.Lock()
	defer lookup.mu.Unlock()
	now := time.Now()
	if !lookup.checked.IsZero() && now.Sub(lookup.checked) < geoStatInterval {
		return lookup.db
	}
	lookup.checked = now
	path := findGeodataSource(c.manager.geodataSourceDirs(), names)
	if path == "" {
		lookup.db, lookup.path, lookup.modTime = nil, "", time.Time{}
		return nil
	}
	info, err := os.Stat(path)
	if err != nil {
		lookup.db, lookup.path, lookup.modTime = nil, "", time.Time{}
		return nil
	}
	if lookup.db != nil && path == lookup.path && info.ModTime().Equal(lookup.modTime) {
		return lookup.db
	}
	db, err := openGeoDB(path)
	if err != nil {
		log.Printf("geoip: %s unusable: %v", path, err)
		lookup.db, lookup.path, lookup.modTime = nil, "", time.Time{}
		return nil
	}
	lookup.db, lookup.path, lookup.modTime = db, path, info.ModTime()
	return db
}
