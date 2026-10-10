package main

import (
	"github.com/multica-ai/multica/server/internal/events"
	"github.com/multica-ai/multica/server/pkg/protocol"
	"testing"
)

func TestPersonalFlagEventIsNotWorkspaceBroadcast(t *testing.T) {
	bus := events.New()
	b := &fakeBroadcaster{}
	registerListeners(bus, b)
	bus.Publish(events.Event{Type: protocol.EventFeatureFlagChanged, WorkspaceID: "workspace", RecipientUserID: "member", ActorID: "agent", ActorType: "agent", Payload: map[string]any{"scope_type": "member", "key": "flag"}})
	if len(b.workspaceCalls) != 0 {
		t.Fatal("personal override leaked to workspace")
	}
	if len(b.userCalls) != 1 || b.userCalls[0].userID != "member" {
		t.Fatalf("event not sent to owning member: %+v", b.userCalls)
	}
}
