// chat.go owns the OpenRouter request boundary for JSON document updates.
//
// Each caller explicitly states whether its document requires Zero Data
// Retention. Sensitive documents use Ling with strict privacy filters. Ordinary
// documents use an ordered list of strong free models with automatic fallback.
package main

import (
	"context"
	"errors"
	"os"
	"strings"

	openrouter "github.com/OpenRouterTeam/go-sdk"
	"github.com/OpenRouterTeam/go-sdk/models/components"
	"github.com/OpenRouterTeam/go-sdk/optionalnullable"
)

// privateZDRModel is the deliberately narrow route used for sensitive systems
// such as a future Email integration. The request also requires a ZDR provider
// endpoint, so OpenRouter fails closed if Ling is not privately available.
const privateZDRModel = "inclusionai/ling-3.0-flash:free"

// generalFreeModels is ordered from the preferred general-purpose model to
// smaller fallbacks. Free availability changes over time, so this list should
// be reviewed periodically against OpenRouter's current free model collection.
var generalFreeModels = []string{
	"nvidia/nemotron-3-ultra-550b-a55b:free",
	"nvidia/nemotron-3-super-120b-a12b:free",
	"inclusionai/ling-3.0-flash:free",
}

// The struct we return in sendChatMessage function
type chatResponse struct {
	Model    string `json:"model"`
	Response string `json:"response"`
}

// sendChatMessage sends one JSON update request. zdr identifies whether this
// particular system must use a Zero Data Retention provider endpoint. Health
// and Meals pass false; a future Email updater will pass true.
func sendChatMessage(
	ctx context.Context,
	prompt string,
	currentJSON string,
	zdr bool,
) (chatResponse, error) {
	prompt = strings.TrimSpace(prompt)
	if prompt == "" {
		prompt = "Update the provided JSON. Return only the updated JSON with no markdown."
	}

	currentJSON = strings.TrimSpace(currentJSON)
	if currentJSON == "" {
		return chatResponse{}, errors.New("json is required")
	}

	apiKey := os.Getenv("OPENROUTER_API_KEY")
	if apiKey == "" {
		return chatResponse{}, errors.New("OPENROUTER_API_KEY is not set")
	}

	client := openrouter.New(
		openrouter.WithSecurity(apiKey),
	)

	// Model and privacy selection live in a pure helper so tests can inspect the
	// outgoing request without an API key or a live OpenRouter call.
	chatRequest := buildChatRequest(prompt, currentJSON, zdr)

	response, err := client.Chat.Send(
		ctx,
		chatRequest,
		nil,
	)
	if err != nil {
		return chatResponse{}, err
	}

	if response == nil || response.ChatResult == nil {
		return chatResponse{}, errors.New("OpenRouter returned no chat result")
	}

	if len(response.ChatResult.Choices) == 0 {
		return chatResponse{}, errors.New("OpenRouter returned no choices")
	}
	// We actually get the content of the response here if there are no errors.
	content, ok := response.ChatResult.Choices[0].Message.Content.GetOrZero()
	if !ok || content.Str == nil {
		return chatResponse{}, errors.New("OpenRouter returned no text content")
	}

	// The generated SDK models message content as a union: it may be a string, an
	// array of content items, or another JSON value. Its Str member is therefore
	// a *string so nil means "this content is not the string variant." The check
	// above proves the pointer is non-nil; `*content.Str` then dereferences it,
	// copying the actual string into chatResponse.Response.
	return chatResponse{
		Model:    response.ChatResult.Model,
		Response: *content.Str,
	}, nil
}

// buildChatRequest selects models and provider privacy for one system.
//
// When zdr is true, Model contains only Ling and Provider requires both no data
// collection and zero retention. When zdr is false, Models contains the ordered
// free fallback list and Provider is omitted, so this request adds no privacy
// restrictions beyond settings configured on the OpenRouter account itself.
func buildChatRequest(prompt string, currentJSON string, zdr bool) components.ChatRequest {
	request := components.ChatRequest{
		Messages: []components.ChatMessages{
			components.CreateChatMessagesUser(
				components.ChatUserMessage{
					Role: components.ChatUserMessageRoleUser,
					Content: components.CreateChatUserMessageContentStr(
						buildJSONUpdateMessage(prompt, currentJSON),
					),
				},
			),
		},
	}

	if zdr {
		// A single explicit model avoids randomly selecting a free model for
		// sensitive content. The policy ensures that even Ling is rejected if its
		// currently available endpoint does not meet both privacy requirements.
		request.Model = openrouter.Pointer(privateZDRModel)
		request.Provider = optionalnullable.From(openrouter.Pointer(components.ProviderPreferences{
			DataCollection: optionalnullable.From(openrouter.Pointer(components.DataCollectionDeny)),
			Zdr:            optionalnullable.From(openrouter.Pointer(true)),
		}))
		return request
	}

	// Copy the slice so the SDK cannot mutate the package-level configuration
	// used to build later requests.
	request.Models = append([]string(nil), generalFreeModels...)
	return request
}

func buildJSONUpdateMessage(prompt string, currentJSON string) string {
	return "Instructions:\n" + prompt +
		"\n\nCurrent JSON:\n" + currentJSON +
		"\n\nReturn only the updated JSON."
}
