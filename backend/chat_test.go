// chat_test.go verifies OpenRouter model and privacy selection without sending
// personal JSON to a provider or requiring an API key.
package main

import (
	"encoding/json"
	"testing"
)

func TestBuildChatRequestUsesLingAndZDRForPrivateSystems(t *testing.T) {
	t.Parallel()

	request := buildChatRequest("Update Email.", `{"messages":[]}`, true)
	encodedRequest, err := json.Marshal(request)
	if err != nil {
		t.Fatalf("marshal private chat request: %v", err)
	}

	var payload map[string]any
	if err := json.Unmarshal(encodedRequest, &payload); err != nil {
		t.Fatalf("decode private chat request: %v", err)
	}

	if gotModel := payload["model"]; gotModel != privateZDRModel {
		t.Fatalf("model = %v, want %q", gotModel, privateZDRModel)
	}
	if _, hasFallbackModels := payload["models"]; hasFallbackModels {
		t.Fatal("private request unexpectedly included general fallback models")
	}

	provider, ok := payload["provider"].(map[string]any)
	if !ok {
		t.Fatalf("provider = %#v, want an object", payload["provider"])
	}
	if gotZDR := provider["zdr"]; gotZDR != true {
		t.Fatalf("provider.zdr = %v, want true", gotZDR)
	}
	if gotCollectionPolicy := provider["data_collection"]; gotCollectionPolicy != "deny" {
		t.Fatalf("provider.data_collection = %v, want deny", gotCollectionPolicy)
	}
}

func TestBuildChatRequestUsesFreeFallbacksForOrdinarySystems(t *testing.T) {
	t.Parallel()

	request := buildChatRequest("Update Meals.", `{"recommendations":[]}`, false)
	encodedRequest, err := json.Marshal(request)
	if err != nil {
		t.Fatalf("marshal ordinary chat request: %v", err)
	}

	var payload map[string]any
	if err := json.Unmarshal(encodedRequest, &payload); err != nil {
		t.Fatalf("decode ordinary chat request: %v", err)
	}

	if _, hasSingleModel := payload["model"]; hasSingleModel {
		t.Fatal("ordinary request unexpectedly included a single model")
	}
	if _, hasProviderPolicy := payload["provider"]; hasProviderPolicy {
		t.Fatal("ordinary request unexpectedly included ZDR provider policy")
	}

	encodedModels, ok := payload["models"].([]any)
	if !ok {
		t.Fatalf("models = %#v, want an array", payload["models"])
	}
	if len(encodedModels) != len(generalFreeModels) {
		t.Fatalf("len(models) = %d, want %d", len(encodedModels), len(generalFreeModels))
	}
	for index, expectedModel := range generalFreeModels {
		if encodedModels[index] != expectedModel {
			t.Fatalf("models[%d] = %v, want %q", index, encodedModels[index], expectedModel)
		}
	}
}
