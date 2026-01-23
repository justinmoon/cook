// Spike 3: NATS Go client with JetStream
// Tests: connect to NATS, create stream, publish events, consume events

package main

import (
	"context"
	"fmt"
	"log"
	"time"

	"github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"
)

func main() {
	// Connect to NATS
	nc, err := nats.Connect(nats.DefaultURL)
	if err != nil {
		log.Fatalf("Failed to connect to NATS: %v", err)
	}
	defer nc.Close()
	fmt.Println("Connected to NATS")

	// Create JetStream context
	js, err := jetstream.New(nc)
	if err != nil {
		log.Fatalf("Failed to create JetStream context: %v", err)
	}
	fmt.Println("JetStream context created")

	ctx := context.Background()

	// Create or update stream
	stream, err := js.CreateOrUpdateStream(ctx, jetstream.StreamConfig{
		Name:        "COOK_EVENTS",
		Description: "Cook event stream",
		Subjects:    []string{"cook.>"},
		Retention:   jetstream.LimitsPolicy,
		MaxAge:      7 * 24 * time.Hour, // 7 days
		Storage:     jetstream.FileStorage,
	})
	if err != nil {
		log.Fatalf("Failed to create stream: %v", err)
	}
	fmt.Printf("Stream created/updated: %s\n", stream.CachedInfo().Config.Name)

	// Publish some events
	events := []struct {
		subject string
		data    string
	}{
		{"cook.branch.myrepo.feature-x.created", `{"type":"created","repo":"myrepo","branch":"feature-x"}`},
		{"cook.agent.myrepo.feature-x.started", `{"type":"started","repo":"myrepo","branch":"feature-x","agent":"claude"}`},
		{"cook.agent.myrepo.feature-x.output", `{"type":"output","data":"Hello from agent"}`},
		{"cook.gate.myrepo.feature-x.ci.started", `{"type":"started","gate":"ci"}`},
		{"cook.gate.myrepo.feature-x.ci.passed", `{"type":"passed","gate":"ci"}`},
	}

	for _, e := range events {
		ack, err := js.Publish(ctx, e.subject, []byte(e.data))
		if err != nil {
			log.Printf("Failed to publish %s: %v", e.subject, err)
			continue
		}
		fmt.Printf("Published: %s (seq: %d)\n", e.subject, ack.Sequence)
	}

	// Create a consumer
	consumer, err := stream.CreateOrUpdateConsumer(ctx, jetstream.ConsumerConfig{
		Durable:       "spike-consumer",
		FilterSubject: "cook.>",
		AckPolicy:     jetstream.AckExplicitPolicy,
	})
	if err != nil {
		log.Fatalf("Failed to create consumer: %v", err)
	}
	fmt.Println("Consumer created")

	// Consume messages
	fmt.Println("\nConsuming messages:")
	msgs, err := consumer.Fetch(10, jetstream.FetchMaxWait(2*time.Second))
	if err != nil {
		log.Fatalf("Failed to fetch: %v", err)
	}

	for msg := range msgs.Messages() {
		fmt.Printf("  Subject: %s\n", msg.Subject())
		fmt.Printf("  Data: %s\n", string(msg.Data()))
		msg.Ack()
	}

	if msgs.Error() != nil {
		fmt.Printf("Fetch completed with: %v\n", msgs.Error())
	}

	// Clean up - delete consumer and stream for repeatability
	stream.DeleteConsumer(ctx, "spike-consumer")
	js.DeleteStream(ctx, "COOK_EVENTS")
	fmt.Println("\nCleanup complete")
}
