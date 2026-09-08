package agent

import (
	"context"
	"reflect"
	"sync"
	"testing"
)

func TestSerialToolsPreserveBatchOrder(t *testing.T) {
	var mu sync.Mutex
	var order []string
	tool := func(name string, serial bool) Tool {
		return Tool{Name: name, Serial: serial, Execute: func(context.Context, map[string]any, func(ToolResult)) (ToolResult, error) {
			mu.Lock()
			order = append(order, name)
			mu.Unlock()
			return ToolResult{Content: []ContentBlock{{Type: "text", Text: "done"}}}, nil
		}}
	}
	core, err := New(Config{Model: "test", Provider: &scriptedProvider{}, ParallelTools: true,
		Tools: []Tool{tool("read", false), tool("browser_navigate", true), tool("browser_screenshot", true)},
	})
	if err != nil {
		t.Fatal(err)
	}
	calls := []ContentBlock{{ID: "a", Name: "read"}, {ID: "b", Name: "browser_navigate"}, {ID: "c", Name: "browser_screenshot"}}
	if _, err := core.executeTools(t.Context(), calls, func(Event) {}); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(order, []string{"read", "browser_navigate", "browser_screenshot"}) {
		t.Fatalf("order=%v", order)
	}
}

func TestRejectedBrowserNavigateDoesNotExecute(t *testing.T) {
	executed := false
	core, err := New(Config{Model: "test", Provider: &scriptedProvider{}, ToolGuard: denyingGuard{}, Tools: []Tool{{
		Name: "browser_navigate", Serial: true,
		Execute: func(context.Context, map[string]any, func(ToolResult)) (ToolResult, error) {
			executed = true
			return ToolResult{}, nil
		},
	}}})
	if err != nil {
		t.Fatal(err)
	}
	var statuses []string
	_, err = core.executeTools(t.Context(), []ContentBlock{{ID: "browser", Name: "browser_navigate"}}, func(event Event) {
		if event.Type == EventToolSafetyUpdate {
			statuses = append(statuses, event.SafetyStatus)
		}
	})
	if err != nil || executed || !reflect.DeepEqual(statuses, []string{"classifying", "rejected"}) {
		t.Fatalf("executed=%v statuses=%v err=%v", executed, statuses, err)
	}
}
