package app

import (
	"encoding/binary"
	"net/netip"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

// The tests build their own databases rather than checking in a Country.mmdb:
// a fixture would be several MB of binary in git, and a builder can produce the
// malformed shapes (dangling pointers, impossible node counts) that a real file
// never contains.

const (
	mmdbTestEmpty = iota
	mmdbTestNode
	mmdbTestData
)

type mmdbTestTrieNode struct {
	kind  [2]int
	value [2]uint32
}

type mmdbTestBuilder struct {
	recordSize uint16
	ipVersion  uint16
	nodes      []mmdbTestTrieNode
	data       []byte
}

func newMmdbTestBuilder(recordSize, ipVersion uint16) *mmdbTestBuilder {
	return &mmdbTestBuilder{
		recordSize: recordSize,
		ipVersion:  ipVersion,
		nodes:      []mmdbTestTrieNode{{}},
	}
}

// addData appends an encoded value to the data section and returns its offset.
func (b *mmdbTestBuilder) addData(value []byte) uint32 {
	offset := uint32(len(b.data))
	b.data = append(b.data, value...)
	return offset
}

// insert points the network prefix/bits at the data section offset, creating
// intermediate nodes as needed.
func (b *mmdbTestBuilder) insert(prefix []byte, bits int, offset uint32) {
	cur := 0
	for i := 0; i < bits; i++ {
		bit := (prefix[i/8] >> (7 - i%8)) & 1
		if i == bits-1 {
			b.nodes[cur].kind[bit] = mmdbTestData
			b.nodes[cur].value[bit] = offset
			return
		}
		if b.nodes[cur].kind[bit] != mmdbTestNode {
			b.nodes = append(b.nodes, mmdbTestTrieNode{})
			b.nodes[cur].kind[bit] = mmdbTestNode
			b.nodes[cur].value[bit] = uint32(len(b.nodes) - 1)
		}
		cur = int(b.nodes[cur].value[bit])
	}
}

// insertV4 inserts an IPv4 prefix, prepending the 96 zero bits of the ::/96
// subtree when the database is an ip_version 6 one.
func (b *mmdbTestBuilder) insertV4(a, c, d, e byte, bits int, offset uint32) {
	if b.ipVersion == 6 {
		b.insert([]byte{0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, a, c, d, e}, 96+bits, offset)
		return
	}
	b.insert([]byte{a, c, d, e}, bits, offset)
}

func (b *mmdbTestBuilder) build() []byte {
	return b.buildWithMetadata(uint64(len(b.nodes)), uint64(b.recordSize), uint64(b.ipVersion))
}

// buildWithMetadata emits the file with the declared metadata values, which the
// malformed-file tests override to describe a database the bytes do not match.
func (b *mmdbTestBuilder) buildWithMetadata(nodeCount, recordSize, ipVersion uint64) []byte {
	realCount := uint32(len(b.nodes))
	nodeSize := int(b.recordSize) / 4
	out := make([]byte, len(b.nodes)*nodeSize)
	for i, node := range b.nodes {
		var records [2]uint32
		for side := 0; side < 2; side++ {
			switch node.kind[side] {
			case mmdbTestData:
				records[side] = realCount + geoDBSeparatorSize + node.value[side]
			case mmdbTestNode:
				records[side] = node.value[side]
			default:
				records[side] = realCount
			}
		}
		mmdbTestPutNode(out[i*nodeSize:(i+1)*nodeSize], b.recordSize, records[0], records[1])
	}
	out = append(out, make([]byte, geoDBSeparatorSize)...)
	out = append(out, b.data...)
	out = append(out, geoDBMarker...)
	return append(out, mmdbTestMetadata(nodeCount, recordSize, ipVersion)...)
}

func mmdbTestMetadata(nodeCount, recordSize, ipVersion uint64) []byte {
	return mmdbTestMap(
		mmdbTestString("binary_format_major_version"), mmdbTestUint(mmdbUint16, 2),
		mmdbTestString("database_type"), mmdbTestString("Test-Country"),
		mmdbTestString("node_count"), mmdbTestUint(mmdbUint32, nodeCount),
		mmdbTestString("record_size"), mmdbTestUint(mmdbUint16, recordSize),
		mmdbTestString("ip_version"), mmdbTestUint(mmdbUint16, ipVersion),
	)
}

func mmdbTestPutNode(dst []byte, recordSize uint16, left, right uint32) {
	switch recordSize {
	case 24:
		mmdbTestPut24(dst[0:3], left)
		mmdbTestPut24(dst[3:6], right)
	case 28:
		mmdbTestPut24(dst[0:3], left)
		dst[3] = byte((left>>24)&0x0f)<<4 | byte((right>>24)&0x0f)
		mmdbTestPut24(dst[4:7], right)
	default:
		binary.BigEndian.PutUint32(dst[0:4], left)
		binary.BigEndian.PutUint32(dst[4:8], right)
	}
}

func mmdbTestPut24(dst []byte, value uint32) {
	dst[0] = byte(value >> 16)
	dst[1] = byte(value >> 8)
	dst[2] = byte(value)
}

func mmdbTestHeader(typ, size int) []byte {
	var head []byte
	var ctrl byte
	if typ < 8 {
		ctrl = byte(typ) << 5
	}
	switch {
	case size < 29:
		head = []byte{ctrl | byte(size)}
	case size < 285:
		head = []byte{ctrl | 29, byte(size - 29)}
	case size < 65821:
		head = []byte{ctrl | 30, byte((size - 285) >> 8), byte(size - 285)}
	default:
		v := size - 65821
		head = []byte{ctrl | 31, byte(v >> 16), byte(v >> 8), byte(v)}
	}
	if typ >= 8 {
		head = append([]byte{head[0], byte(typ - 7)}, head[1:]...)
	}
	return head
}

func mmdbTestString(s string) []byte {
	return append(mmdbTestHeader(mmdbString, len(s)), s...)
}

func mmdbTestUint(typ int, value uint64) []byte {
	var raw []byte
	for shift := 56; shift >= 0; shift -= 8 {
		b := byte(value >> shift)
		if len(raw) == 0 && b == 0 {
			continue
		}
		raw = append(raw, b)
	}
	return append(mmdbTestHeader(typ, len(raw)), raw...)
}

func mmdbTestBlob(typ, size int) []byte {
	return append(mmdbTestHeader(typ, size), make([]byte, size)...)
}

func mmdbTestBool(value bool) []byte {
	size := 0
	if value {
		size = 1
	}
	return mmdbTestHeader(mmdbBool, size)
}

func mmdbTestMap(pairs ...[]byte) []byte {
	out := mmdbTestHeader(mmdbMap, len(pairs)/2)
	for _, pair := range pairs {
		out = append(out, pair...)
	}
	return out
}

func mmdbTestArray(items ...[]byte) []byte {
	out := mmdbTestHeader(mmdbArray, len(items))
	for _, item := range items {
		out = append(out, item...)
	}
	return out
}

// mmdbTestPointer encodes the shortest pointer form that fits target, so the
// tests exercise the 1-byte form real files are full of.
func mmdbTestPointer(target uint32) []byte {
	switch {
	case target < 1<<11:
		return []byte{0x20 | byte(target>>8), byte(target)}
	case target < (1<<19)+2048:
		v := target - 2048
		return []byte{0x28 | byte(v>>16), byte(v >> 8), byte(v)}
	default:
		v := target - 526336
		return []byte{0x30 | byte(v>>24), byte(v >> 16), byte(v >> 8), byte(v)}
	}
}

func mmdbTestPointer32(target uint32) []byte {
	return []byte{0x38, byte(target >> 24), byte(target >> 16), byte(target >> 8), byte(target)}
}

func mmdbTestCountry(iso string) []byte {
	return mmdbTestMap(
		mmdbTestString("country"), mmdbTestMap(mmdbTestString("iso_code"), mmdbTestString(iso)),
	)
}

// mmdbTestNoisyCountry wraps iso in the kind of record a real database holds:
// unrelated keys of every skippable type, including the extended ones, sitting
// in front of iso_code.
func mmdbTestNoisyCountry(iso string) []byte {
	return mmdbTestMap(
		mmdbTestString("continent"), mmdbTestMap(
			mmdbTestString("code"), mmdbTestString("NA"),
			mmdbTestString("names"), mmdbTestMap(mmdbTestString("en"), mmdbTestString("North America")),
		),
		mmdbTestString("country"), mmdbTestMap(
			mmdbTestString("geoname_id"), mmdbTestUint(mmdbUint32, 6252001),
			mmdbTestString("is_in_european_union"), mmdbTestBool(false),
			mmdbTestString("population"), mmdbTestUint(mmdbUint64, 331002651),
			mmdbTestString("score"), mmdbTestBlob(mmdbDouble, 8),
			mmdbTestString("accuracy"), mmdbTestBlob(mmdbFloat, 4),
			mmdbTestString("delta"), mmdbTestBlob(mmdbInt32, 4),
			mmdbTestString("hash"), mmdbTestBlob(mmdbUint128, 16),
			mmdbTestString("blob"), mmdbTestBlob(mmdbBytes, 3),
			mmdbTestString("tags"), mmdbTestArray(mmdbTestString("a"), mmdbTestString("b")),
			mmdbTestString("iso_code"), mmdbTestString(iso),
		),
	)
}

func mmdbTestOpen(t *testing.T, raw []byte) (*geoDB, error) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "Country.mmdb")
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		t.Fatalf("write database: %v", err)
	}
	return openGeoDB(path)
}

func mmdbTestMustOpen(t *testing.T, raw []byte) *geoDB {
	t.Helper()
	db, err := mmdbTestOpen(t, raw)
	if err != nil {
		t.Fatalf("openGeoDB: %v", err)
	}
	return db
}

func mmdbTestWant(t *testing.T, db *geoDB, ip, want string) {
	t.Helper()
	addr, err := netip.ParseAddr(ip)
	if err != nil {
		t.Fatalf("parse %s: %v", ip, err)
	}
	code, ok := db.lookupCountry(addr)
	if want == "" {
		if ok || code != "" {
			t.Fatalf("lookupCountry(%s) = %q, %v; want not found", ip, code, ok)
		}
		return
	}
	if !ok || code != want {
		t.Fatalf("lookupCountry(%s) = %q, %v; want %q, true", ip, code, ok, want)
	}
}

// mmdbTestFleetDB is the shape real Country.mmdb files have: 28-bit records in
// an ip_version 6 database whose IPv4 data hangs off ::/96.
func mmdbTestFleetDB(t *testing.T) *geoDB {
	t.Helper()
	b := newMmdbTestBuilder(28, 6)
	// A copy of the metadata marker inside the data section: the reader has to
	// find the last one, not the first.
	b.addData(mmdbTestString(geoDBMarker))
	us := b.addData(mmdbTestNoisyCountry("US"))
	b.insertV4(8, 8, 8, 0, 24, us)
	b.insertV4(2, 2, 2, 0, 24, b.addData(mmdbTestPointer(us)))
	b.insertV4(5, 5, 5, 0, 24, b.addData(mmdbTestPointer32(us)))

	jp := b.addData(mmdbTestMap(mmdbTestString("iso_code"), mmdbTestString("jp")))
	b.insertV4(3, 3, 3, 0, 24, b.addData(mmdbTestMap(
		mmdbTestString("country"), mmdbTestPointer(jp),
	)))
	b.insertV4(1, 1, 1, 0, 24, b.addData(mmdbTestMap(
		mmdbTestString("registered_country"), mmdbTestMap(mmdbTestString("iso_code"), mmdbTestString("AU")),
	)))
	// country present but unusable: the registered_country fallback still wins.
	b.insertV4(6, 6, 6, 0, 24, b.addData(mmdbTestMap(
		mmdbTestString("country"), mmdbTestMap(mmdbTestString("geoname_id"), mmdbTestUint(mmdbUint32, 7)),
		mmdbTestString("registered_country"), mmdbTestMap(mmdbTestString("iso_code"), mmdbTestString("NL")),
	)))
	b.insertV4(4, 4, 4, 0, 24, b.addData(mmdbTestCountry("USA")))
	b.insertV4(7, 7, 7, 0, 24, b.addData(mmdbTestCountry("U1")))
	b.insert([]byte{0x20, 0x01, 0x0d, 0xb8}, 32, b.addData(mmdbTestCountry("DE")))
	return mmdbTestMustOpen(t, b.build())
}

func TestMmdbLookupCountry(t *testing.T) {
	db := mmdbTestFleetDB(t)
	if db.ipv4Start == 0 {
		t.Fatalf("ipv4Start = 0; want the node reached after 96 zero bits")
	}
	cases := []struct{ ip, want string }{
		{"8.8.8.8", "US"},
		{"8.8.8.255", "US"},
		{"::ffff:8.8.8.8", "US"},
		{"2.2.2.2", "US"},
		{"5.5.5.5", "US"},
		{"3.3.3.3", "JP"},
		{"1.1.1.1", "AU"},
		{"6.6.6.6", "NL"},
		{"2001:db8::1", "DE"},
		{"9.9.9.9", ""},
		{"8.8.9.1", ""},
		{"4.4.4.4", ""},
		{"7.7.7.7", ""},
		{"2001:db9::1", ""},
	}
	for _, tc := range cases {
		mmdbTestWant(t, db, tc.ip, tc.want)
	}
}

func TestMmdbRecordSizes(t *testing.T) {
	for _, recordSize := range []uint16{24, 28, 32} {
		for _, ipVersion := range []uint16{4, 6} {
			b := newMmdbTestBuilder(recordSize, ipVersion)
			// 10.0.0.0/8 sits under the left record of the tree's second level
			// and 192.0.0.0/2 under the right one, so both halves of every node
			// layout get read.
			b.insertV4(10, 0, 0, 0, 8, b.addData(mmdbTestCountry("CN")))
			b.insertV4(192, 0, 0, 0, 2, b.addData(mmdbTestCountry("SG")))
			db := mmdbTestMustOpen(t, b.build())
			if db.recordSize != recordSize {
				t.Fatalf("recordSize = %d; want %d", db.recordSize, recordSize)
			}
			mmdbTestWant(t, db, "10.1.2.3", "CN")
			mmdbTestWant(t, db, "200.1.2.3", "SG")
			mmdbTestWant(t, db, "11.1.2.3", "")
		}
	}
}

func TestMmdbIPv6AddressInIPv4Database(t *testing.T) {
	b := newMmdbTestBuilder(24, 4)
	b.insertV4(10, 0, 0, 0, 8, b.addData(mmdbTestCountry("CN")))
	db := mmdbTestMustOpen(t, b.build())
	mmdbTestWant(t, db, "2001:db8::1", "")
	// ::ffff:10.0.0.1 is an IPv4 address in disguise and must still resolve.
	mmdbTestWant(t, db, "::ffff:10.0.0.1", "CN")
}

func TestMmdbOpenRejectsMalformedFiles(t *testing.T) {
	valid := func() []byte {
		b := newMmdbTestBuilder(28, 6)
		b.insertV4(8, 8, 8, 0, 24, b.addData(mmdbTestCountry("US")))
		return b.build()
	}
	tooFar := append(valid(), make([]byte, geoDBMetadataMaxTail+1)...)
	noNodeCount := func() []byte {
		b := newMmdbTestBuilder(28, 6)
		b.insertV4(8, 8, 8, 0, 24, b.addData(mmdbTestCountry("US")))
		raw := b.build()
		raw = raw[:len(raw)-len(mmdbTestMetadata(uint64(len(b.nodes)), 28, 6))]
		return append(raw, mmdbTestMap(mmdbTestString("record_size"), mmdbTestUint(mmdbUint16, 28))...)
	}
	oversizedTree := func() []byte {
		b := newMmdbTestBuilder(28, 6)
		b.insertV4(8, 8, 8, 0, 24, b.addData(mmdbTestCountry("US")))
		return b.buildWithMetadata(1<<20, 28, 6)
	}
	cases := []struct {
		name string
		raw  []byte
	}{
		{"empty", nil},
		{"garbage", []byte("this is not a maxmind database at all")},
		{"truncated", valid()[:len(valid())/2]},
		{"marker only", []byte(geoDBMarker)},
		{"marker beyond tail window", tooFar},
		{"metadata missing node_count", noNodeCount()},
		{"bad record size", func() []byte {
			b := newMmdbTestBuilder(28, 6)
			b.insertV4(8, 8, 8, 0, 24, b.addData(mmdbTestCountry("US")))
			return b.buildWithMetadata(uint64(len(b.nodes)), 26, 6)
		}()},
		{"bad ip version", func() []byte {
			b := newMmdbTestBuilder(28, 6)
			b.insertV4(8, 8, 8, 0, 24, b.addData(mmdbTestCountry("US")))
			return b.buildWithMetadata(uint64(len(b.nodes)), 28, 5)
		}()},
		{"zero node count", func() []byte {
			b := newMmdbTestBuilder(28, 6)
			b.insertV4(8, 8, 8, 0, 24, b.addData(mmdbTestCountry("US")))
			return b.buildWithMetadata(0, 28, 6)
		}()},
		{"search tree larger than file", oversizedTree()},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if db, err := mmdbTestOpen(t, tc.raw); err == nil {
				t.Fatalf("openGeoDB accepted %s (ipv4Start %d)", tc.name, db.ipv4Start)
			}
		})
	}
}

func TestMmdbOpenMissingFile(t *testing.T) {
	if _, err := openGeoDB(filepath.Join(t.TempDir(), "absent.mmdb")); err == nil {
		t.Fatal("openGeoDB accepted a missing file")
	}
}

func TestMmdbLookupSurvivesCorruptRecords(t *testing.T) {
	b := newMmdbTestBuilder(24, 4)
	good := b.addData(mmdbTestCountry("US"))
	b.insertV4(8, 0, 0, 0, 8, good)
	// A record value that resolves past the end of the data section.
	b.insertV4(10, 0, 0, 0, 8, 1<<20)
	// A pointer inside the data section aimed outside it.
	b.insertV4(11, 0, 0, 0, 8, b.addData(mmdbTestMap(
		mmdbTestString("country"), mmdbTestPointer32(1<<24),
	)))
	// A pointer that resolves to itself: the hop cap has to turn this into an
	// error instead of a hang.
	selfRef := uint32(len(b.data))
	b.insertV4(12, 0, 0, 0, 8, b.addData(mmdbTestPointer(selfRef)))
	// A string whose length runs off the end of the section.
	b.insertV4(14, 0, 0, 0, 8, b.addData(mmdbTestMap(
		mmdbTestString("country"), mmdbTestMap(
			mmdbTestString("iso_code"), mmdbTestHeader(mmdbString, 500),
		),
	)))
	// A key of a type that is not a string.
	b.insertV4(15, 0, 0, 0, 8, b.addData(append(
		mmdbTestHeader(mmdbMap, 1),
		append(mmdbTestUint(mmdbUint32, 9), mmdbTestString("US")...)...,
	)))
	// A container type (12) that these files never carry.
	b.insertV4(16, 0, 0, 0, 8, b.addData(mmdbTestMap(
		mmdbTestString("country"), mmdbTestBlob(12, 2),
		mmdbTestString("registered_country"), mmdbTestMap(mmdbTestString("iso_code"), mmdbTestString("FR")),
	)))
	// A map claiming far more pairs than the section holds. It goes last so the
	// traversal it triggers runs off the end of the section rather than into a
	// neighbouring record.
	b.insertV4(13, 0, 0, 0, 8, b.addData(append(
		mmdbTestHeader(mmdbMap, 400),
		mmdbTestString("country")...,
	)))
	db := mmdbTestMustOpen(t, b.build())

	mmdbTestWant(t, db, "8.1.1.1", "US")
	for _, ip := range []string{"10.1.1.1", "11.1.1.1", "12.1.1.1", "13.1.1.1", "14.1.1.1", "15.1.1.1", "16.1.1.1"} {
		mmdbTestWant(t, db, ip, "")
	}
}

func TestMmdbLookupRejectsNonISOCodes(t *testing.T) {
	b := newMmdbTestBuilder(24, 4)
	// The country.mmdb shipped with this repo really does answer "GOOGLE" for
	// 8.8.8.8, and the code is rendered in the web UI.
	b.insertV4(8, 8, 8, 0, 24, b.addData(mmdbTestCountry("GOOGLE")))
	b.insertV4(9, 9, 9, 0, 24, b.addData(mmdbTestCountry("")))
	b.insertV4(10, 10, 10, 0, 24, b.addData(mmdbTestCountry("uß")))
	b.insertV4(11, 11, 11, 0, 24, b.addData(mmdbTestCountry("<b")))
	b.insertV4(12, 12, 12, 0, 24, b.addData(mmdbTestCountry("de")))
	db := mmdbTestMustOpen(t, b.build())
	for _, ip := range []string{"8.8.8.8", "9.9.9.9", "10.10.10.10", "11.11.11.11"} {
		mmdbTestWant(t, db, ip, "")
	}
	mmdbTestWant(t, db, "12.12.12.12", "DE")
}

func TestMmdbLookupInvalidAddress(t *testing.T) {
	db := mmdbTestFleetDB(t)
	if code, ok := db.lookupCountry(netip.Addr{}); ok || code != "" {
		t.Fatalf("lookupCountry(zero Addr) = %q, %v; want not found", code, ok)
	}
	var nilDB *geoDB
	if code, ok := nilDB.lookupCountry(netip.MustParseAddr("8.8.8.8")); ok || code != "" {
		t.Fatalf("nil geoDB lookup = %q, %v; want not found", code, ok)
	}
}

func TestMmdbConcurrentLookups(t *testing.T) {
	db := mmdbTestFleetDB(t)
	queries := []struct{ ip, want string }{
		{"8.8.8.8", "US"},
		{"3.3.3.3", "JP"},
		{"1.1.1.1", "AU"},
		{"2001:db8::1", "DE"},
		{"9.9.9.9", ""},
	}
	var wg sync.WaitGroup
	for worker := 0; worker < 8; worker++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < 200; i++ {
				q := queries[i%len(queries)]
				code, ok := db.lookupCountry(netip.MustParseAddr(q.ip))
				if q.want == "" {
					if ok {
						t.Errorf("lookupCountry(%s) = %q; want not found", q.ip, code)
					}
					continue
				}
				if !ok || code != q.want {
					t.Errorf("lookupCountry(%s) = %q, %v; want %q", q.ip, code, ok, q.want)
				}
			}
		}()
	}
	wg.Wait()
}

// mmdbTestASNDB builds a synthetic ASN database covering the shapes lookupASN
// has to survive: both fields present, each one alone, neither, and a record
// whose organisation carries control characters.
func mmdbTestASNDB(t *testing.T) *geoDB {
	t.Helper()
	b := newMmdbTestBuilder(28, 6)
	b.insertV4(8, 8, 8, 0, 24, b.addData(mmdbTestMap(
		mmdbTestString("autonomous_system_number"), mmdbTestUint(mmdbUint32, 15169),
		mmdbTestString("autonomous_system_organization"), mmdbTestString("Google LLC"),
	)))
	// Number only -- still a hit; "AS13335" alone is useful.
	b.insertV4(1, 1, 1, 0, 24, b.addData(mmdbTestMap(
		mmdbTestString("autonomous_system_number"), mmdbTestUint(mmdbUint32, 13335),
	)))
	// Organisation only -- also a hit.
	b.insertV4(2, 2, 2, 0, 24, b.addData(mmdbTestMap(
		mmdbTestString("autonomous_system_organization"), mmdbTestString("Example Net"),
	)))
	// Neither field: present in the tree, but nothing to show.
	b.insertV4(3, 3, 3, 0, 24, b.addData(mmdbTestMap(
		mmdbTestString("geoname_id"), mmdbTestUint(mmdbUint32, 7),
	)))
	b.insertV4(4, 4, 4, 0, 24, b.addData(mmdbTestMap(
		mmdbTestString("autonomous_system_number"), mmdbTestUint(mmdbUint32, 64500),
		mmdbTestString("autonomous_system_organization"), mmdbTestString("Bad\x00Org\nInc\t"),
	)))
	b.insert([]byte{0x20, 0x01, 0x0d, 0xb8}, 32, b.addData(mmdbTestMap(
		mmdbTestString("autonomous_system_number"), mmdbTestUint(mmdbUint32, 3320),
		mmdbTestString("autonomous_system_organization"), mmdbTestString("Deutsche Telekom AG"),
	)))
	return mmdbTestMustOpen(t, b.build())
}

func TestMmdbLookupASN(t *testing.T) {
	db := mmdbTestASNDB(t)
	cases := []struct {
		ip      string
		wantNum uint32
		wantOrg string
		wantOK  bool
	}{
		{"8.8.8.8", 15169, "Google LLC", true},
		{"1.1.1.1", 13335, "", true},
		{"2.2.2.2", 0, "Example Net", true},
		// Both fields absent: a tree hit is not an ASN.
		{"3.3.3.3", 0, "", false},
		// Control characters are stripped, not escaped or passed through.
		{"4.4.4.4", 64500, "BadOrgInc", true},
		{"2001:db8::1", 3320, "Deutsche Telekom AG", true},
		// ::ffff:8.8.8.8 must resolve through the ::/96 IPv4 subtree, the same
		// as lookupCountry -- both now share recordOffset.
		{"::ffff:8.8.8.8", 15169, "Google LLC", true},
		{"9.9.9.9", 0, "", false},
	}
	for _, tt := range cases {
		addr, err := netip.ParseAddr(tt.ip)
		if err != nil {
			t.Fatalf("ParseAddr(%q): %v", tt.ip, err)
		}
		got, ok := db.lookupASN(addr)
		if ok != tt.wantOK {
			t.Fatalf("lookupASN(%s) ok = %v, want %v", tt.ip, ok, tt.wantOK)
		}
		if got.Number != tt.wantNum || got.Org != tt.wantOrg {
			t.Fatalf("lookupASN(%s) = %d/%q, want %d/%q", tt.ip, got.Number, got.Org, tt.wantNum, tt.wantOrg)
		}
	}
}

func TestMmdbLookupASNOnNilDatabase(t *testing.T) {
	var db *geoDB
	if _, ok := db.lookupASN(netip.MustParseAddr("8.8.8.8")); ok {
		t.Fatal("lookupASN on a nil database must report a miss, not panic")
	}
}

func TestSanitizeASNOrgBoundsLength(t *testing.T) {
	long := strings.Repeat("x", asnOrgMaxLen+50)
	if got := sanitizeASNOrg(long); len(got) != asnOrgMaxLen {
		t.Fatalf("sanitizeASNOrg length = %d, want %d", len(got), asnOrgMaxLen)
	}
	if got := sanitizeASNOrg("  Cloudflare, Inc.  "); got != "Cloudflare, Inc." {
		t.Fatalf("sanitizeASNOrg = %q, want the trimmed name", got)
	}
}
