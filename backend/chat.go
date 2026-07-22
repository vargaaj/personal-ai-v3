package main

import (
	"context"
	"errors"
	"os"
	"strings"

	openrouter "github.com/OpenRouterTeam/go-sdk"
	"github.com/OpenRouterTeam/go-sdk/models/components"
)

// The struct we return in sendChatMessage function
type chatResponse struct {
	Model    string `json:"model"`
	Response string `json:"response"`
}

func sendChatMessage(ctx context.Context, prompt string, currentJSON string) (chatResponse, error) {
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

	response, err := client.Chat.Send(
		ctx,
		components.ChatRequest{
			Model: openrouter.Pointer("openrouter/free"),
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
		},
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

	// Return the chatResponse struct with the model used and the response itself.
	return chatResponse{
		Model:    response.ChatResult.Model,
		Response: *content.Str,
	}, nil
}

func buildJSONUpdateMessage(prompt string, currentJSON string) string {
	return "Instructions:\n" + prompt +
		"\n\nCurrent JSON:\n" + currentJSON +
		"\n\nReturn only the updated JSON."
}
