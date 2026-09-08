package main

import (
	"errors"

	"github.com/peterw22/pi-go/internal/agent"
)

func safetyDecisionTool() agent.Tool {
	return agent.Tool{
		Name:        "submit_safety_decision",
		Description: "Submit the safety decision and notification summary for the proposed operation exactly once. This only reports a result; it does not execute the operation.",
		Parameters: map[string]any{
			"type": "object", "additionalProperties": false,
			"required": []string{"allowed", "reason", "notificationSummary", "authorization", "effectScopes"},
			"properties": map[string]any{
				"allowed":             map[string]any{"type": "boolean", "description": "Whether the complete operation may run without manual approval."},
				"reason":              map[string]any{"type": "string", "description": "A short explanation of the decision and any specific harm."},
				"notificationSummary": map[string]any{"type": "string", "description": "One notification-safe plain-text sentence, at most 220 characters, ending in sentence punctuation. Exclude secrets, full commands, and private paths."},
				"authorization":       map[string]any{"type": "string", "enum": []string{"none", "latest_user_prompt", "prior_approval"}},
				"effectScopes":        map[string]any{"type": "array", "items": map[string]any{"type": "string"}, "description": "Narrow concrete scopes of persistent, privileged, external-transmission, secret, or out-of-workspace effects. Use an empty array when none apply."},
			},
		},
	}
}

func safetyDecisionFromArguments(arguments map[string]any) (safetyDecision, error) {
	var decision safetyDecision
	if err := requireOutputFields(arguments, "allowed", "reason", "notificationSummary", "authorization", "effectScopes"); err != nil {
		return decision, err
	}
	allowed, ok := arguments["allowed"].(bool)
	if !ok {
		return decision, errors.New("output tool argument allowed must be a boolean")
	}
	decision.Allowed = allowed
	var err error
	if decision.Reason, err = outputString(arguments, "reason"); err != nil {
		return decision, err
	}
	if decision.NotificationSummary, err = outputString(arguments, "notificationSummary"); err != nil {
		return decision, err
	}
	if decision.Authorization, err = outputString(arguments, "authorization"); err != nil {
		return decision, err
	}
	// JSON-decoding providers supply []any; in-process providers may supply []string.
	switch scopes := arguments["effectScopes"].(type) {
	case []any:
		if scopes == nil {
			return decision, errors.New("output tool argument effectScopes must be an array")
		}
		decision.EffectScopes = make([]string, 0, len(scopes))
		for _, scope := range scopes {
			text, ok := scope.(string)
			if !ok {
				return decision, errors.New("output tool effectScopes items must be strings")
			}
			decision.EffectScopes = append(decision.EffectScopes, text)
		}
	case []string:
		if scopes == nil {
			return decision, errors.New("output tool argument effectScopes must be an array")
		}
		decision.EffectScopes = append([]string{}, scopes...)
	default:
		return decision, errors.New("output tool argument effectScopes must be an array")
	}
	return decision, nil
}
