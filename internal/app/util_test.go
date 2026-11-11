package app

import "testing"

// B5: an id equal to a routed path segment would be unreachable through
// /api/instances/{id}; such a name must get a suffixed id instead.
func TestUniqueIDSkipsRoutedSlugs(t *testing.T) {
	if got := uniqueID("Chain Candidates", map[string]*Instance{}); got != "chain-candidates-2" {
		t.Fatalf("uniqueID(Chain Candidates) = %q, want chain-candidates-2", got)
	}
	if got := uniqueID("Chain", map[string]*Instance{}); got != "chain" {
		t.Fatalf("uniqueID(Chain) = %q, want chain", got)
	}
}
