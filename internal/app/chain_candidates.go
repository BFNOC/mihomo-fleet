package app

import (
	"errors"
	"fmt"
	"net/http"
	"strings"

	"gopkg.in/yaml.v3"
)

// chainCandidatesMax caps the Candidates array POST
// /api/instances/chain-candidates returns. profileConfig can be up to
// several MB of subscription YAML with thousands of proxies (the same
// content ProfileProxyGroups already parses), so the response is bounded the
// same way the geoip batch endpoint bounds its own input (controller.go's
// geoBatchLimit).
const chainCandidatesMax = 5000

// chainCandidates computes the authoritative set of names a global-chain
// "chain" array may reference for profileConfig plus a draft localProxies
// YAML: buildGlobalChainPlan's allowed set (config.go:262-278) is inline
// profile proxy names union local proxy names union
// {globalChainSelectGroupName}. Proxy-provider names are never valid chain
// members (buildGlobalChainPlan only ever wires them in via `use:`, never as
// a dialer-proxy target), so they are reported separately in
// ChainCandidatesResult.ProviderNames instead of being mixed into
// Candidates.
//
// Unlike applyGlobalChainConfig, a malformed localProxies draft is not a
// hard failure here: the UI's picker must keep offering profile candidates
// while the user is still mid-typing the local-proxies YAML box, so a
// parseLocalProxyItems error is surfaced as LocalError (its exact message
// text -- app.js's errorPatterns already localize it, see constants.ts) --
// rather than failing the whole request. The one case that does still zero
// out the local names without a parse error is config.go:196-200's
// cross-check: a local name that collides with a profile-inline name.
func chainCandidates(profileConfig, localProxies string) (ChainCandidatesResult, error) {
	var cfg map[string]any
	if err := yaml.Unmarshal([]byte(profileConfig), &cfg); err != nil {
		return ChainCandidatesResult{}, fmt.Errorf("parse profile config: %w", err)
	}
	if cfg == nil {
		cfg = make(map[string]any)
	}
	_, inlineNames, err := proxyItemsAndNames(cfg["proxies"])
	if err != nil {
		return ChainCandidatesResult{}, err
	}
	providerNames := proxyProviderNames(cfg["proxy-providers"])

	result := ChainCandidatesResult{ProviderNames: providerNames}

	_, localNames, localErr := parseLocalProxyItems(localProxies)
	if localErr != nil {
		result.LocalError = localErr.Error()
		localNames = nil
	} else {
		inlineKnown := stringSet(inlineNames)
		for _, name := range localNames {
			if inlineKnown[name] {
				result.LocalError = fmt.Sprintf("local proxy name %q conflicts with profile proxy", name)
				localNames = nil
				break
			}
		}
	}

	candidates := make([]ChainCandidate, 0, 1+len(localNames)+len(inlineNames))
	seen := make(map[string]bool, 1+len(localNames)+len(inlineNames))
	add := func(name, kind string) {
		if seen[name] {
			return
		}
		seen[name] = true
		candidates = append(candidates, ChainCandidate{Name: name, Kind: kind})
	}
	// Order: 节点选择 first, then local names in their YAML order, then
	// profile names in config order -- matching how buildGlobalChainPlan
	// itself orders a default (no explicit Chain) plan.
	add(globalChainSelectGroupName, "group")
	for _, name := range localNames {
		add(name, "local")
	}

	// The cap only ever trims profile names: 节点选择 and every local name
	// (the two kinds a picker cannot silently drop without breaking the
	// draft the user is actively editing) are always kept in full.
	profileNames := inlineNames
	if remaining := chainCandidatesMax - len(candidates); len(profileNames) > remaining {
		if remaining < 0 {
			remaining = 0
		}
		profileNames = profileNames[:remaining]
		result.Truncated = true
	}
	for _, name := range profileNames {
		add(name, "profile")
	}

	result.Candidates = candidates
	return result, nil
}

func (c *Controller) handleChainCandidates(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		methodNotAllowed(w)
		return
	}
	var req struct {
		ProfileID    string `json:"profileId"`
		LocalProxies string `json:"localProxies"`
	}
	if err := readJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	profileID := strings.TrimSpace(req.ProfileID)
	if profileID == "" {
		writeError(w, http.StatusBadRequest, errors.New("profileId is required"))
		return
	}
	cfg, err := c.store.ReadProfileConfig(profileID)
	if err != nil {
		writeError(w, http.StatusNotFound, sanitizedProfileConfigReadError(profileID, err))
		return
	}
	result, err := chainCandidates(cfg, req.LocalProxies)
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	writeJSON(w, result)
}
