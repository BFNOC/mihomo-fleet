package app

import (
	"context"
	"fmt"
	"log"
	"time"
)

func (c *Controller) subscriptionUserAgent() string {
	return "clash-verge/v" + c.appVersion
}

func (c *Controller) refreshProfileSubscription(ctx context.Context, id string) (*Profile, []SelectionReconciliation, error) {
	profile, ok := c.store.GetProfile(id)
	if !ok {
		return nil, nil, fmt.Errorf("profile %q not found", id)
	}
	if profile.SubscriptionURL == "" {
		return nil, nil, fmt.Errorf("profile %q is not a subscription profile", id)
	}
	if !c.beginSubscriptionUpdate(id) {
		return nil, nil, fmt.Errorf("profile %q subscription update is already running", id)
	}
	defer c.endSubscriptionUpdate(id)

	ctx, cancel := context.WithTimeout(ctx, 25*time.Second)
	defer cancel()
	fetched, err := fetchSubscription(ctx, c.subscriptionClient, profile.SubscriptionURL, c.subscriptionUserAgent())
	if err != nil {
		c.store.SetProfileUpdateError(id, err.Error())
		return nil, nil, err
	}
	return c.store.ApplySubscriptionFetchForURL(id, profile.SubscriptionURL, fetched)
}

func (c *Controller) beginSubscriptionUpdate(id string) bool {
	c.subscriptionMu.Lock()
	defer c.subscriptionMu.Unlock()
	if c.subscriptionRunning[id] {
		return false
	}
	c.subscriptionRunning[id] = true
	return true
}

func (c *Controller) endSubscriptionUpdate(id string) {
	c.subscriptionMu.Lock()
	defer c.subscriptionMu.Unlock()
	delete(c.subscriptionRunning, id)
}

func (c *Controller) startSubscriptionScheduler() {
	ctx, cancel := context.WithCancel(context.Background())
	c.subscriptionCancel = cancel
	go func() {
		timer := time.NewTimer(3 * time.Second)
		defer timer.Stop()
		ticker := time.NewTicker(time.Minute)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-timer.C:
				c.refreshDueSubscriptions(ctx)
			case <-ticker.C:
				c.refreshDueSubscriptions(ctx)
			}
		}
	}()
}

// maxConcurrentSubscriptionRefreshes caps how many refreshDueSubscriptions
// goroutines may be fetching a subscription at once (testing H5 /
// concurrency L-4 area): previously one goroutine was spawned per due
// profile with no limit, so a fleet with many subscription profiles that
// all came due in the same scheduler tick (e.g. after being offline) would
// fire that many concurrent outbound HTTP fetches.
const maxConcurrentSubscriptionRefreshes = 4

func (c *Controller) refreshDueSubscriptions(ctx context.Context) {
	now := time.Now().UTC()
	sem := make(chan struct{}, maxConcurrentSubscriptionRefreshes)
	for _, profile := range c.store.ListProfiles() {
		if !profileSubscriptionDue(profile, now) {
			continue
		}
		if c.subscriptionUpdateRunning(profile.ID) {
			continue
		}
		profileID := profile.ID
		sem <- struct{}{}
		go func() {
			defer func() { <-sem }()
			_, changes, err := c.refreshProfileSubscription(ctx, profileID)
			if err != nil {
				log.Printf("subscription update failed for profile %s: %v", profileID, err)
				return
			}
			// This scheduler run has no HTTP response to attach changes to
			// (unlike the manual-refresh and URL-change handlers below), so
			// without this log a reassignment triggered by the background
			// scheduler would stay exactly as silent as the bug this fixes.
			for _, change := range changes {
				log.Printf("subscription update for profile %s reassigned instance %s (%s) group %q: %q -> %q/%q",
					profileID, change.InstanceID, change.InstanceName, change.Group, change.VanishedProxy, change.ReplacementGroup, change.ReplacementProxy)
			}
		}()
	}
}

func (c *Controller) subscriptionUpdateRunning(id string) bool {
	c.subscriptionMu.Lock()
	defer c.subscriptionMu.Unlock()
	return c.subscriptionRunning[id]
}
