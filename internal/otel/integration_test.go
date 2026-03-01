package otel

import (
	"context"
	"fmt"
	"testing"
	"time"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracehttp"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	semconv "go.opentelemetry.io/otel/semconv/v1.26.0"
	oteltrace "go.opentelemetry.io/otel/trace"
)

// TestIntegration_SendAndReceive starts a real receiver, sends spans via the
// OTEL SDK, and verifies they arrive in the store. This is an end-to-end test.
func TestIntegration_SendAndReceive(t *testing.T) {
	// Start receiver
	store := NewSpanStore()
	port := 14320
	receiver := NewReceiver(store, port)
	if err := receiver.Start(); err != nil {
		t.Fatalf("receiver start: %v", err)
	}
	defer receiver.Stop()
	time.Sleep(100 * time.Millisecond) // let server start

	// Create OTEL exporter pointing at our receiver
	ctx := context.Background()
	exporter, err := otlptracehttp.New(ctx,
		otlptracehttp.WithEndpoint(fmt.Sprintf("127.0.0.1:%d", port)),
		otlptracehttp.WithInsecure(),
	)
	if err != nil {
		t.Fatalf("create exporter: %v", err)
	}

	// Create trace provider
	res, _ := resource.New(ctx,
		resource.WithAttributes(semconv.ServiceName("test-agent")),
	)
	tp := sdktrace.NewTracerProvider(
		sdktrace.WithBatcher(exporter,
			sdktrace.WithBatchTimeout(100*time.Millisecond),
		),
		sdktrace.WithResource(res),
	)
	defer tp.Shutdown(ctx)

	tracer := tp.Tracer("test")

	// Send spans: session root -> turn -> tool call
	sessionCtx, sessionSpan := tracer.Start(ctx, "invoke_agent test-project",
		oteltrace.WithAttributes(
			attribute.String("gen_ai.operation.name", "invoke_agent"),
			attribute.String("gen_ai.agent.name", "test-project"),
			attribute.String("gen_ai.conversation.id", "session-test-123"),
		),
	)

	turnCtx, turnSpan := tracer.Start(sessionCtx, "chat turn-1",
		oteltrace.WithAttributes(
			attribute.String("gen_ai.operation.name", "chat"),
			attribute.String("gen_ai.input.messages", "fix the bug"),
			attribute.String("gen_ai.output.messages", "I'll look at main.go"),
			attribute.String("gen_ai.request.model", "claude-opus-4-6"),
			attribute.Int64("gen_ai.usage.input_tokens", 5000),
			attribute.Int64("gen_ai.usage.output_tokens", 200),
		),
	)

	_, toolSpan := tracer.Start(turnCtx, "execute_tool Read",
		oteltrace.WithAttributes(
			attribute.String("gen_ai.operation.name", "execute_tool"),
			attribute.String("gen_ai.tool.name", "Read"),
			attribute.String("gen_ai.tool.call.arguments", "main.go"),
		),
	)
	toolSpan.End()
	turnSpan.End()
	sessionSpan.End()

	// Force flush and wait for spans to arrive
	if err := tp.ForceFlush(ctx); err != nil {
		t.Fatalf("flush: %v", err)
	}
	time.Sleep(200 * time.Millisecond) // give receiver time to process

	// Verify spans arrived
	if !store.HasData() {
		t.Fatal("store has no data after sending spans")
	}

	// Check we can find the session by conversation ID
	root := store.GetByConversation("session-test-123")
	if root == nil {
		t.Fatal("GetByConversation('session-test-123') returned nil")
	}

	if root.AttrStr("gen_ai.agent.name") != "test-project" {
		t.Errorf("agent name = %q, want %q", root.AttrStr("gen_ai.agent.name"), "test-project")
	}

	// Verify tree structure
	if len(root.Children) != 1 {
		t.Fatalf("root has %d children, want 1 (the turn)", len(root.Children))
	}

	turn := root.Children[0]
	if turn.AttrStr("gen_ai.input.messages") != "fix the bug" {
		t.Errorf("turn input = %q, want %q", turn.AttrStr("gen_ai.input.messages"), "fix the bug")
	}
	if turn.AttrStr("gen_ai.request.model") != "claude-opus-4-6" {
		t.Errorf("model = %q, want %q", turn.AttrStr("gen_ai.request.model"), "claude-opus-4-6")
	}
	if turn.AttrInt64("gen_ai.usage.input_tokens") != 5000 {
		t.Errorf("input_tokens = %d, want 5000", turn.AttrInt64("gen_ai.usage.input_tokens"))
	}

	if len(turn.Children) != 1 {
		t.Fatalf("turn has %d children, want 1 (the tool call)", len(turn.Children))
	}

	tool := turn.Children[0]
	if tool.AttrStr("gen_ai.tool.name") != "Read" {
		t.Errorf("tool name = %q, want %q", tool.AttrStr("gen_ai.tool.name"), "Read")
	}

	// Verify converter produces valid turns
	turns := SpansToTurns(root)
	if len(turns) != 1 {
		t.Fatalf("SpansToTurns produced %d turns, want 1", len(turns))
	}
	if turns[0].UserLines[0] != "fix the bug" {
		t.Errorf("converted turn UserLines = %v", turns[0].UserLines)
	}
	if turns[0].Model != "claude-opus-4-6" {
		t.Errorf("converted turn Model = %q", turns[0].Model)
	}
	if len(turns[0].Actions) != 1 || turns[0].Actions[0].Name != "Read" {
		t.Errorf("converted turn Actions = %v", turns[0].Actions)
	}

	t.Logf("Integration test passed: %d spans received, tree assembled, converter works", len(store.GetSpans(root.TraceID)))
}
