package app

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"math"
	"net/netip"
	"os"
	"strings"
)

const (
	geoDBMarker = "\xab\xcd\xefMaxMind.com"

	// The metadata section is written at the very end of an mmdb file, so the
	// marker is found by scanning backwards. Bounding that scan keeps a blob
	// that merely happens to live at Country.mmdb from costing a full-file
	// search before it is rejected.
	geoDBMetadataMaxTail = 128 << 10

	// Country.mmdb is 6-10 MB in practice; the cap only exists so a wrong
	// (or hostile) file cannot be read into memory unbounded.
	geoDBMaxFileSize = 256 << 20

	// A 16-byte run of zeroes separates the search tree from the data section,
	// and the same 16 is subtracted when turning a record value into a data
	// section offset.
	geoDBSeparatorSize = 16

	// Real organisation names run well under this ("Google LLC",
	// "Cloudflare, Inc."); the cap only bounds what a hostile record can push
	// into a table cell.
	asnOrgMaxLen = 120

	// A pointer may legitimately point at another pointer. Real files do not
	// chain further than that, so a short cap turns a crafted cycle into an
	// error instead of a hang.
	geoDBMaxPointerHops = 4

	// Records nest a handful of levels deep (map -> map -> array of maps).
	// The cap bounds recursion in skipValue for a file that claims otherwise.
	geoDBMaxNestDepth = 32
)

const (
	mmdbPointer = 1
	mmdbString  = 2
	mmdbDouble  = 3
	mmdbBytes   = 4
	mmdbUint16  = 5
	mmdbUint32  = 6
	mmdbMap     = 7
	mmdbInt32   = 8
	mmdbUint64  = 9
	mmdbUint128 = 10
	mmdbArray   = 11
	mmdbBool    = 14
	mmdbFloat   = 15
)

// geoDB is a read-only MaxMind DB reader covering exactly what the fleet needs:
// turn an IP into an ISO 3166-1 alpha-2 country code. Bringing in
// oschwald/maxminddb-golang would be the obvious alternative, but this project
// deliberately ships with a single dependency (gopkg.in/yaml.v3), and the
// subset of the format needed for one string lookup is small.
//
// Every field is filled in by openGeoDB and never written again, which is what
// makes lookupCountry safe to call concurrently from HTTP handlers.
type geoDB struct {
	tree    []byte
	section mmdbSection

	nodeCount  uint32
	recordSize uint16
	nodeSize   int
	ipVersion  uint16

	// ipv4Start is the record value reached by walking 96 zero bits from the
	// root of an ip_version 6 database, i.e. where the ::/96 subtree that holds
	// the IPv4 data begins. Walking those bits once at open time saves 96 node
	// reads on every IPv4 lookup. It is 0 (the root) for an ip_version 4
	// database, and may legitimately be >= nodeCount for a degenerate database
	// whose entire IPv4 space is one record -- traverse handles that.
	ipv4Start uint32
}

// mmdbSection is one addressable region of the file (the data section, or the
// metadata section). Offsets inside a section -- including pointer targets --
// are relative to its start, so keeping the slice rather than the whole file
// makes every bounds check a comparison against len(buf).
type mmdbSection struct {
	buf []byte
}

// openGeoDB reads the whole database into memory and parses its metadata.
// Country.mmdb is a few MB and every lookup would otherwise be several seeks,
// so the file is not kept open.
func openGeoDB(path string) (*geoDB, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("open geoip database: %w", err)
	}
	defer f.Close()
	data, err := io.ReadAll(io.LimitReader(f, geoDBMaxFileSize+1))
	if err != nil {
		return nil, fmt.Errorf("read geoip database %s: %w", path, err)
	}
	if len(data) > geoDBMaxFileSize {
		return nil, fmt.Errorf("geoip database %s is larger than %d bytes", path, geoDBMaxFileSize)
	}
	db, err := parseGeoDB(data)
	if err != nil {
		return nil, fmt.Errorf("parse geoip database %s: %w", path, err)
	}
	return db, nil
}

func parseGeoDB(data []byte) (*geoDB, error) {
	tail := len(data) - geoDBMetadataMaxTail
	if tail < 0 {
		tail = 0
	}
	rel := bytes.LastIndex(data[tail:], []byte(geoDBMarker))
	if rel < 0 {
		return nil, errors.New("metadata marker not found")
	}
	markerStart := tail + rel
	meta := mmdbSection{buf: data[markerStart+len(geoDBMarker):]}

	nodeCount, err := meta.metadataUint("node_count")
	if err != nil {
		return nil, err
	}
	recordSize, err := meta.metadataUint("record_size")
	if err != nil {
		return nil, err
	}
	ipVersion, err := meta.metadataUint("ip_version")
	if err != nil {
		return nil, err
	}
	if nodeCount == 0 || nodeCount > 0xffffffff {
		return nil, fmt.Errorf("unsupported node_count %d", nodeCount)
	}
	switch recordSize {
	case 24, 28, 32:
	default:
		return nil, fmt.Errorf("unsupported record_size %d", recordSize)
	}
	switch ipVersion {
	case 4, 6:
	default:
		return nil, fmt.Errorf("unsupported ip_version %d", ipVersion)
	}

	treeSize := nodeCount * recordSize / 4
	if treeSize+geoDBSeparatorSize > uint64(markerStart) {
		return nil, fmt.Errorf("search tree of %d bytes does not fit before the metadata", treeSize)
	}
	dataStart := int(treeSize) + geoDBSeparatorSize

	db := &geoDB{
		tree:       data[:treeSize],
		section:    mmdbSection{buf: data[dataStart:markerStart]},
		nodeCount:  uint32(nodeCount),
		recordSize: uint16(recordSize),
		nodeSize:   int(recordSize) / 4,
		ipVersion:  uint16(ipVersion),
	}
	if db.ipVersion == 6 {
		start, err := db.traverse(0, make([]byte, 12))
		if err != nil {
			return nil, fmt.Errorf("locate ipv4 subtree: %w", err)
		}
		db.ipv4Start = start
	}
	return db, nil
}

// lookupCountry returns the ISO 3166-1 alpha-2 code for addr, or ("", false)
// when the address is absent from the database or its record carries no usable
// code. It mutates nothing and is safe to call from many goroutines at once.
func (db *geoDB) lookupCountry(addr netip.Addr) (string, bool) {
	offset, ok := db.recordOffset(addr)
	if !ok {
		return "", false
	}
	return db.countryCode(offset)
}

// ASNRecord is one GeoLite2-ASN row: the autonomous system that announces the
// address, and the organisation that runs it.
type ASNRecord struct {
	Number uint32 `json:"asn"`
	Org    string `json:"org"`
}

// lookupASN reads autonomous_system_number/autonomous_system_organization out
// of an ASN database. Same file format as the country database and so the same
// reader -- only the record's field names differ, which is why the tree walk
// lives in recordOffset rather than in either lookup.
//
// A record carrying only one of the two fields still counts as a hit: the
// number alone is useful ("AS15169"), and so is the organisation name alone.
// Both missing is a miss.
func (db *geoDB) lookupASN(addr netip.Addr) (ASNRecord, bool) {
	offset, ok := db.recordOffset(addr)
	if !ok {
		return ASNRecord{}, false
	}
	var record ASNRecord
	if numOff, ok, err := db.section.lookupKey(offset, "autonomous_system_number"); err == nil && ok {
		if value, err := db.section.readUint(numOff); err == nil && value <= math.MaxUint32 {
			record.Number = uint32(value)
		}
	}
	if orgOff, ok, err := db.section.lookupKey(offset, "autonomous_system_organization"); err == nil && ok {
		if org, err := db.section.readString(orgOff); err == nil {
			record.Org = sanitizeASNOrg(org)
		}
	}
	if record.Number == 0 && record.Org == "" {
		return ASNRecord{}, false
	}
	return record, true
}

// sanitizeASNOrg bounds and cleans an organisation name. Same reasoning as
// normalizeCountryCode: the database is an arbitrary file the user dropped in
// and this string is rendered in a browser. Control characters are stripped
// rather than escaped, and the length cap keeps a hostile record from filling
// a table cell.
func sanitizeASNOrg(org string) string {
	org = strings.Map(func(r rune) rune {
		if r < 0x20 || r == 0x7f {
			return -1
		}
		return r
	}, org)
	org = strings.TrimSpace(org)
	if len(org) > asnOrgMaxLen {
		org = strings.ToValidUTF8(org[:asnOrgMaxLen], "")
	}
	return org
}

// recordOffset walks the search tree for addr and returns the offset of its
// record inside the data section. Shared by lookupCountry and lookupASN, which
// differ only in which keys they then read. It mutates nothing and is safe to
// call from many goroutines at once.
func (db *geoDB) recordOffset(addr netip.Addr) (int, bool) {
	if db == nil {
		return 0, false
	}
	// ::ffff:a.b.c.d has to be looked up as a.b.c.d: the IPv4 data lives under
	// ::/96, and while real databases also alias ::ffff:0:0/96, relying on that
	// alias would make the answer depend on how the caller happened to parse
	// the address.
	addr = addr.Unmap()
	if !addr.IsValid() {
		return 0, false
	}
	var (
		ip    []byte
		start uint32
	)
	if addr.Is4() {
		octets := addr.As4()
		ip = octets[:]
		start = db.ipv4Start
	} else {
		if db.ipVersion == 4 {
			return 0, false
		}
		octets := addr.As16()
		ip = octets[:]
	}
	value, err := db.traverse(start, ip)
	if err != nil {
		return 0, false
	}
	// value == nodeCount is the format's "no data" record. Below it means the
	// walk ran out of address bits while still inside the tree, which only a
	// malformed database does.
	if value <= db.nodeCount {
		return 0, false
	}
	offset := int64(value) - int64(db.nodeCount) - geoDBSeparatorSize
	if offset < 0 || offset >= int64(len(db.section.buf)) {
		return 0, false
	}
	return int(offset), true
}

// countryCode reads country.iso_code, falling back to
// registered_country.iso_code. The fallback also covers a present-but-unusable
// country map, not just a missing one: some records carry a country entry with
// only a geoname_id.
func (db *geoDB) countryCode(offset int) (string, bool) {
	for _, key := range [2]string{"country", "registered_country"} {
		countryOff, ok, err := db.section.lookupKey(offset, key)
		if err != nil || !ok {
			continue
		}
		isoOff, ok, err := db.section.lookupKey(countryOff, "iso_code")
		if err != nil || !ok {
			continue
		}
		code, err := db.section.readString(isoOff)
		if err != nil {
			continue
		}
		if code, ok := normalizeCountryCode(code); ok {
			return code, true
		}
	}
	return "", false
}

// normalizeCountryCode upper-cases code and rejects anything that is not two
// ASCII letters. The database is an arbitrary file the user dropped into the
// data directory and this string ends up rendered in a browser: the repo's own
// country.mmdb, for instance, answers "GOOGLE" for 8.8.8.8 and "CLOUDFLARE"
// for 1.1.1.1, so the field cannot be trusted to hold an ISO code.
func normalizeCountryCode(code string) (string, bool) {
	if len(code) != 2 {
		return "", false
	}
	out := []byte(code)
	for i, c := range out {
		if c >= 'a' && c <= 'z' {
			c -= 'a' - 'A'
		}
		if c < 'A' || c > 'Z' {
			return "", false
		}
		out[i] = c
	}
	return string(out), true
}

// traverse walks the search tree from start, consuming the bits of ip most
// significant first, and returns the first record value that is not an internal
// node index. Checking that at the top of the loop (rather than after each
// step) is what lets start itself already be a terminal value.
func (db *geoDB) traverse(start uint32, ip []byte) (uint32, error) {
	value := start
	for _, octet := range ip {
		for shift := 7; shift >= 0; shift-- {
			if value >= db.nodeCount {
				return value, nil
			}
			next, err := db.record(value, (octet>>shift)&1)
			if err != nil {
				return 0, err
			}
			value = next
		}
	}
	return value, nil
}

// record returns one of the two record values held by node. The 28-bit layout
// is the odd one: the two records share the middle byte, high nibble for the
// left record and low nibble for the right.
func (db *geoDB) record(node uint32, bit byte) (uint32, error) {
	if node >= db.nodeCount {
		return 0, fmt.Errorf("search tree node %d out of range", node)
	}
	base := int(node) * db.nodeSize
	if base+db.nodeSize > len(db.tree) {
		return 0, fmt.Errorf("search tree node %d is truncated", node)
	}
	b := db.tree[base : base+db.nodeSize]
	switch db.recordSize {
	case 24:
		if bit == 0 {
			return be24(b[0:3]), nil
		}
		return be24(b[3:6]), nil
	case 28:
		if bit == 0 {
			return uint32(b[3]&0xf0)<<20 | be24(b[0:3]), nil
		}
		return uint32(b[3]&0x0f)<<24 | be24(b[4:7]), nil
	default:
		if bit == 0 {
			return binary.BigEndian.Uint32(b[0:4]), nil
		}
		return binary.BigEndian.Uint32(b[4:8]), nil
	}
}

func be24(b []byte) uint32 {
	return uint32(b[0])<<16 | uint32(b[1])<<8 | uint32(b[2])
}

// readHeader decodes the control byte at off. For every type but pointer, size
// is the payload length in bytes -- except for maps, where it counts key/value
// pairs, arrays, where it counts elements, and booleans, which have no payload
// at all -- and payload is the offset of the first payload byte. For a pointer,
// size is the resolved target offset and payload is the offset of whatever
// follows the pointer, which is not the same thing.
func (s mmdbSection) readHeader(off int) (typ, size, payload int, err error) {
	ctrl, err := s.byteAt(off)
	if err != nil {
		return 0, 0, 0, err
	}
	off++
	typ = int(ctrl >> 5)
	if typ == 0 {
		ext, err := s.byteAt(off)
		if err != nil {
			return 0, 0, 0, err
		}
		typ = 7 + int(ext)
		off++
	}
	size = int(ctrl & 0x1f)
	if typ == mmdbPointer {
		return s.readPointer(size, off)
	}
	switch size {
	case 29:
		b, err := s.byteAt(off)
		if err != nil {
			return 0, 0, 0, err
		}
		size, off = 29+int(b), off+1
	case 30:
		if off+2 > len(s.buf) {
			return 0, 0, 0, fmt.Errorf("size at %d is truncated", off)
		}
		size, off = 285+int(binary.BigEndian.Uint16(s.buf[off:off+2])), off+2
	case 31:
		if off+3 > len(s.buf) {
			return 0, 0, 0, fmt.Errorf("size at %d is truncated", off)
		}
		size, off = 65821+int(be24(s.buf[off:off+3])), off+3
	}
	return typ, size, off, nil
}

// readPointer decodes the pointer whose control-byte size field is raw and
// whose extra bytes start at off. The size field is repurposed here: bits 3-4
// give the width and the low 3 bits are the target's high bits (except for the
// 4-byte form, which ignores them). The constants added for the 2- and 3-byte
// forms are the format's way of not wasting the shorter encodings' range.
func (s mmdbSection) readPointer(raw, off int) (typ, size, payload int, err error) {
	width := ((raw >> 3) & 0x3) + 1
	if off+width > len(s.buf) {
		return 0, 0, 0, fmt.Errorf("pointer at %d is truncated", off)
	}
	value := uint64(raw & 0x7)
	if width == 4 {
		value = 0
	}
	for _, b := range s.buf[off : off+width] {
		value = value<<8 | uint64(b)
	}
	switch width {
	case 2:
		value += 2048
	case 3:
		value += 526336
	}
	if value >= uint64(len(s.buf)) {
		return 0, 0, 0, fmt.Errorf("pointer target %d outside section of %d bytes", value, len(s.buf))
	}
	return mmdbPointer, int(value), off + width, nil
}

// resolve reads the header at off, following pointers until it reaches a real
// value.
func (s mmdbSection) resolve(off int) (typ, size, payload int, err error) {
	for hops := 0; ; hops++ {
		typ, size, payload, err = s.readHeader(off)
		if err != nil {
			return 0, 0, 0, err
		}
		if typ != mmdbPointer {
			return typ, size, payload, nil
		}
		if hops >= geoDBMaxPointerHops {
			return 0, 0, 0, fmt.Errorf("pointer chain at %d is longer than %d hops", off, geoDBMaxPointerHops)
		}
		off = size
	}
}

// skipValue returns the offset just past the value at off, which is how map
// traversal steps over the values it does not care about. Pointers are not
// followed: the target is somewhere else in the section and is not part of the
// enclosing value.
func (s mmdbSection) skipValue(off, depth int) (int, error) {
	if depth > geoDBMaxNestDepth {
		return 0, fmt.Errorf("value at %d nests deeper than %d levels", off, geoDBMaxNestDepth)
	}
	typ, size, payload, err := s.readHeader(off)
	if err != nil {
		return 0, err
	}
	switch typ {
	case mmdbPointer, mmdbBool:
		// A boolean stores its value in the size bits and has no payload.
		return payload, nil
	case mmdbMap, mmdbArray:
		count := size
		if typ == mmdbMap {
			count *= 2
		}
		next := payload
		for i := 0; i < count; i++ {
			if next, err = s.skipValue(next, depth+1); err != nil {
				return 0, err
			}
		}
		return next, nil
	case mmdbString, mmdbDouble, mmdbBytes, mmdbUint16, mmdbUint32, mmdbInt32, mmdbUint64, mmdbUint128, mmdbFloat:
		end := payload + size
		if end > len(s.buf) {
			return 0, fmt.Errorf("value at %d is truncated", off)
		}
		return end, nil
	default:
		return 0, fmt.Errorf("unsupported data type %d at %d", typ, off)
	}
}

// lookupKey returns the offset of the value stored under key in the map at off.
func (s mmdbSection) lookupKey(off int, key string) (int, bool, error) {
	typ, size, payload, err := s.resolve(off)
	if err != nil {
		return 0, false, err
	}
	if typ != mmdbMap {
		return 0, false, fmt.Errorf("value at %d is type %d, not a map", off, typ)
	}
	cursor := payload
	for i := 0; i < size; i++ {
		name, valueOff, err := s.readKey(cursor)
		if err != nil {
			return 0, false, err
		}
		if name == key {
			return valueOff, true, nil
		}
		if cursor, err = s.skipValue(valueOff, 0); err != nil {
			return 0, false, err
		}
	}
	return 0, false, nil
}

// readKey reads a map key and returns it together with the offset of its value.
// A key may itself be a pointer, in which case the value follows the pointer's
// own bytes rather than its target.
func (s mmdbSection) readKey(off int) (string, int, error) {
	typ, size, payload, err := s.readHeader(off)
	if err != nil {
		return "", 0, err
	}
	if typ == mmdbPointer {
		name, err := s.readString(size)
		if err != nil {
			return "", 0, err
		}
		return name, payload, nil
	}
	if typ != mmdbString {
		return "", 0, fmt.Errorf("map key at %d is type %d, not a string", off, typ)
	}
	end := payload + size
	if end > len(s.buf) {
		return "", 0, fmt.Errorf("map key at %d is truncated", off)
	}
	return string(s.buf[payload:end]), end, nil
}

func (s mmdbSection) readString(off int) (string, error) {
	typ, size, payload, err := s.resolve(off)
	if err != nil {
		return "", err
	}
	if typ != mmdbString {
		return "", fmt.Errorf("value at %d is type %d, not a string", off, typ)
	}
	end := payload + size
	if end > len(s.buf) {
		return "", fmt.Errorf("string at %d is truncated", off)
	}
	return string(s.buf[payload:end]), nil
}

func (s mmdbSection) readUint(off int) (uint64, error) {
	typ, size, payload, err := s.resolve(off)
	if err != nil {
		return 0, err
	}
	switch typ {
	case mmdbUint16, mmdbUint32, mmdbUint64:
	default:
		return 0, fmt.Errorf("value at %d is type %d, not an unsigned integer", off, typ)
	}
	// Unsigned integers are stored with leading zero bytes trimmed, so a
	// uint32 field may well arrive as one or two bytes.
	if size > 8 {
		return 0, fmt.Errorf("unsigned integer at %d claims %d bytes", off, size)
	}
	end := payload + size
	if end > len(s.buf) {
		return 0, fmt.Errorf("unsigned integer at %d is truncated", off)
	}
	var value uint64
	for _, b := range s.buf[payload:end] {
		value = value<<8 | uint64(b)
	}
	return value, nil
}

func (s mmdbSection) metadataUint(key string) (uint64, error) {
	off, ok, err := s.lookupKey(0, key)
	if err != nil {
		return 0, fmt.Errorf("read metadata %s: %w", key, err)
	}
	if !ok {
		return 0, fmt.Errorf("metadata has no %s", key)
	}
	value, err := s.readUint(off)
	if err != nil {
		return 0, fmt.Errorf("read metadata %s: %w", key, err)
	}
	return value, nil
}

func (s mmdbSection) byteAt(off int) (byte, error) {
	if off < 0 || off >= len(s.buf) {
		return 0, fmt.Errorf("offset %d outside section of %d bytes", off, len(s.buf))
	}
	return s.buf[off], nil
}
