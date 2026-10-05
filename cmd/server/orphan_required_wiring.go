// orphan_required_wiring.go — ADR-229 Amendment A1 (CHO-2132).
//
// Binds the OrphanRequiredSubscriber onto the NATS JetStream event bus:
//
//	chora.sharing.atom_reuse.orphan_required.v1
//	  → durable consumer chora-creation-atom-reuse-orphan-required
//
// The durable consumer is created by the event bus at Subscribe time; the
// consumer name is overridable via env.
package main

import (
	"context"
	"log"
	"os"
	"strings"

	"github.com/apollo-chora/chora-common/eventbus"

	pubsubadapter "github.com/apollo-chora/chora-creation/internal/adapter/pubsub"
)

// orphanRequiredSubscriptionName resolves the consumer name
// (no-inline-config: overridable via env).
func orphanRequiredSubscriptionName() string {
	if v := strings.TrimSpace(os.Getenv("CHORA_ORPHAN_REQUIRED_SUBSCRIPTION")); v != "" {
		return v
	}
	return pubsubadapter.DefaultOrphanRequiredSubscription
}

// registerOrphanRequiredSubscriber binds the subscriber to the event bus.
// Nil bus → loud skip (dev in-memory bus has no durable consumer surface).
func registerOrphanRequiredSubscriber(
	ctx context.Context,
	bus eventbus.Bus,
	sub *pubsubadapter.OrphanRequiredSubscriber,
) {
	if sub == nil {
		log.Printf("creation: orphan_required subscriber NOT started — nil subscriber")
		return
	}
	if bus == nil {
		log.Printf("creation: orphan_required subscriber SKIPPED (NATS_URL unset)")
		return
	}
	subscription := orphanRequiredSubscriptionName()

	bus.Subscribe(ctx, consumerConfig(subscription, pubsubadapter.TopicAtomReuseOrphanRequired),
		func(mctx context.Context, msg eventbus.Message) error {
			ev, err := pubsubadapter.DecodeOrphanRequired(msg)
			if err != nil {
				return err
			}
			return sub.HandleOrphanRequired(mctx, ev)
		})
	log.Printf("creation: orphan_required subscriber wired (topic=%s consumer=%s, ADR-229 A1)",
		pubsubadapter.TopicAtomReuseOrphanRequired, subscription)
}
