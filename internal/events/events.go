package events

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/nats-io/nats.go"
)

type EventType string

const (
	// Branch events
	EventBranchCreated   EventType = "branch.created"
	EventBranchMerged    EventType = "branch.merged"
	EventBranchAbandoned EventType = "branch.abandoned"

	// Gate events
	EventGateStarted EventType = "gate.started"
	EventGatePassed  EventType = "gate.passed"
	EventGateFailed  EventType = "gate.failed"

	// Agent events
	EventAgentStarted   EventType = "agent.started"
	EventAgentCompleted EventType = "agent.completed"

	// Task events
	EventTaskCreated EventType = "task.created"
	EventTaskClosed  EventType = "task.closed"
)

type Event struct {
	Type      EventType   `json:"type"`
	Branch    string      `json:"branch,omitempty"`
	Repo      string      `json:"repo,omitempty"`
	TaskID    string      `json:"task_id,omitempty"`
	GateName  string      `json:"gate_name,omitempty"`
	Data      interface{} `json:"data,omitempty"`
	Timestamp time.Time   `json:"timestamp"`
}

type Bus struct {
	nc     *nats.Conn
	js     nats.JetStreamContext
	subs   []*nats.Subscription
	active bool
}

func NewBus(natsURL string) (*Bus, error) {
	if natsURL == "" {
		// No NATS configured, return inactive bus
		return &Bus{active: false}, nil
	}

	nc, err := nats.Connect(natsURL)
	if err != nil {
		return nil, fmt.Errorf("failed to connect to NATS: %w", err)
	}

	js, err := nc.JetStream()
	if err != nil {
		nc.Close()
		return nil, fmt.Errorf("failed to get JetStream context: %w", err)
	}

	bus := &Bus{
		nc:     nc,
		js:     js,
		active: true,
	}

	// Create streams
	if err := bus.createStreams(); err != nil {
		nc.Close()
		return nil, err
	}

	return bus, nil
}

func (b *Bus) createStreams() error {
	streams := []struct {
		name     string
		subjects []string
	}{
		{"COOK_BRANCHES", []string{"cook.branch.>"}},
		{"COOK_GATES", []string{"cook.gate.>"}},
		{"COOK_AGENTS", []string{"cook.agent.>"}},
		{"COOK_TASKS", []string{"cook.task.>"}},
	}

	for _, s := range streams {
		_, err := b.js.AddStream(&nats.StreamConfig{
			Name:      s.name,
			Subjects:  s.subjects,
			Retention: nats.LimitsPolicy,
			MaxAge:    24 * time.Hour, // Keep events for 24 hours
			Storage:   nats.FileStorage,
		})
		if err != nil && err != nats.ErrStreamNameAlreadyInUse {
			return fmt.Errorf("failed to create stream %s: %w", s.name, err)
		}
	}

	return nil
}

func (b *Bus) Publish(event Event) error {
	if !b.active {
		return nil // Silently ignore if NATS not configured
	}

	event.Timestamp = time.Now()

	data, err := json.Marshal(event)
	if err != nil {
		return fmt.Errorf("failed to marshal event: %w", err)
	}

	subject := b.subjectFor(event)
	_, err = b.js.Publish(subject, data)
	if err != nil {
		return fmt.Errorf("failed to publish event: %w", err)
	}

	return nil
}

func (b *Bus) subjectFor(event Event) string {
	// Subject format: cook.<type>.<repo>.<branch>.<event>
	// Repo format is "owner/name" which we encode as "owner.name" for NATS subjects
	repoKey := strings.ReplaceAll(event.Repo, "/", ".")
	switch event.Type {
	case EventBranchCreated, EventBranchMerged, EventBranchAbandoned:
		return fmt.Sprintf("cook.branch.%s.%s.%s", repoKey, event.Branch, event.Type)
	case EventGateStarted, EventGatePassed, EventGateFailed:
		return fmt.Sprintf("cook.gate.%s.%s.%s.%s", repoKey, event.Branch, event.GateName, event.Type)
	case EventAgentStarted, EventAgentCompleted:
		return fmt.Sprintf("cook.agent.%s.%s.%s", repoKey, event.Branch, event.Type)
	case EventTaskCreated, EventTaskClosed:
		return fmt.Sprintf("cook.task.%s.%s.%s", repoKey, event.TaskID, event.Type)
	default:
		return fmt.Sprintf("cook.unknown.%s", event.Type)
	}
}

// Subscribe to events matching a subject pattern. Returns unsubscribe function.
func (b *Bus) Subscribe(subject string, handler func(Event)) (func(), error) {
	if !b.active {
		return func() {}, nil
	}

	sub, err := b.nc.Subscribe(subject, func(msg *nats.Msg) {
		var event Event
		if err := json.Unmarshal(msg.Data, &event); err != nil {
			return // Skip malformed events
		}
		handler(event)
	})
	if err != nil {
		return nil, fmt.Errorf("failed to subscribe: %w", err)
	}

	// Track subscription for cleanup on Close()
	b.subs = append(b.subs, sub)

	return func() { sub.Unsubscribe() }, nil
}

// SubscribeBranch subscribes to all events for a specific repo/branch. Returns unsubscribe function.
// repoRef should be "owner/repo", branchName is the branch name.
func (b *Bus) SubscribeBranch(repoRef, branchName string, handler func(Event)) (func(), error) {
	repoKey := strings.ReplaceAll(repoRef, "/", ".")
	return b.Subscribe(fmt.Sprintf("cook.*.%s.%s.>", repoKey, branchName), handler)
}

func (b *Bus) Close() error {
	if !b.active {
		return nil
	}

	for _, sub := range b.subs {
		sub.Unsubscribe()
	}

	b.nc.Close()
	return nil
}

func (b *Bus) IsActive() bool {
	return b.active
}
