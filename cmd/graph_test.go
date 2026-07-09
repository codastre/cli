package cmd

import (
	"strings"
	"testing"
)

// --topic is a Kafka-only, seed-free lookup. These cover the argument
// validation that runs before any network call.

func TestRunGraph_TopicRequiresKafka(t *testing.T) {
	defer func(k, tp, f string) { graphKind, graphTopic, graphFormat = k, tp, f }(
		graphKind, graphTopic, graphFormat)
	graphKind, graphTopic, graphFormat = "http", "orders", "human"

	err := runGraph(graphCmd, nil)
	if err == nil || !strings.Contains(err.Error(), "kafka") {
		t.Fatalf("expected --topic-with-non-kafka error mentioning kafka, got %v", err)
	}
}

func TestRunGraph_RequiresSeedOrTopic(t *testing.T) {
	defer func(k, tp, f string) { graphKind, graphTopic, graphFormat = k, tp, f }(
		graphKind, graphTopic, graphFormat)
	graphKind, graphTopic, graphFormat = "", "", "human"

	err := runGraph(graphCmd, nil)
	if err == nil || !strings.Contains(err.Error(), "topic") {
		t.Fatalf("expected seed-or-topic error, got %v", err)
	}
}
