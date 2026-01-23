package main

import (
	"github.com/justinmoon/cook/internal/config"
	"github.com/justinmoon/cook/internal/events"
)

// getEventBus creates an event bus from config. Returns nil if NATS not configured.
func getEventBus(cfg *config.Config) *events.Bus {
	if cfg.Server.NatsURL == "" {
		return nil
	}
	bus, err := events.NewBus(cfg.Server.NatsURL)
	if err != nil {
		// Log but don't fail - events are optional
		return nil
	}
	return bus
}

// publishEvent publishes an event if the bus is active
func publishEvent(bus *events.Bus, event events.Event) {
	if bus != nil {
		bus.Publish(event)
	}
}
