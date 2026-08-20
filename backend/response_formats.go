// response_formats.go owns the OpenRouter JSON response contracts for the
// document types that support structured generation.
//
// The selector and builders in this file are called from chat.go, while the
// resulting schemas are also inspected by focused chat request tests. Keeping
// the contracts together makes their ownership explicit without changing the
// request construction or validation behavior.
package main

import (
	openrouter "github.com/OpenRouterTeam/go-sdk"
	"github.com/OpenRouterTeam/go-sdk/models/components"
	"github.com/OpenRouterTeam/go-sdk/optionalnullable"
)

// These content-type values identify the stored documents that have explicit
// response contracts. Keeping them beside the response-format functions makes
// it harder for a request to accidentally receive another area's JSON shape.
const (
	maintenanceTasksContentType          = "maintenance_tasks"
	weeklyMealRecommendationsContentType = "weekly_meal_recommendations"
	weeklyWorkoutRoutineContentType      = "weekly_workout_routine"
)

// responseFormatForContentType selects the JSON contract that matches the
// document sent to OpenRouter. The boolean is false for documents such as a
// future Reading updater that still rely only on their stored prompt.
// Called by buildChatRequest in chat.go.
func responseFormatForContentType(contentType string) (components.ResponseFormat, bool) {
	switch contentType {
	case maintenanceTasksContentType:
		return maintenanceTasksResponseFormat(), true
	case weeklyMealRecommendationsContentType:
		return weeklyMealRecommendationsResponseFormat(), true
	case weeklyWorkoutRoutineContentType:
		return weeklyWorkoutRoutineResponseFormat(), true
	default:
		return components.ResponseFormat{}, false
	}
}

// maintenanceTasksResponseFormat defines the Home document returned after its
// completed tasks are refreshed. The response remains a complete document so it
// can safely travel through the existing whole-document validation and storage
// boundaries: every input task whose state is open must be copied unchanged,
// while each input task whose state is done is replaced in the same array
// position with a new task.
//
// The response only permits the open state. This matters in the interface
// because a replacement is new work that should appear in the Open filter, not
// remain hidden as though it were already completed. Legacy open entries can
// omit display fields accepted by the frontend, so those fields are optional in
// this provider schema. Descriptions tell the model that done-task replacements
// must include the complete shape; area_content_validation.go makes that
// conditional rule final.
// Called by responseFormatForContentType in response_formats.go.
func maintenanceTasksResponseFormat() components.ResponseFormat {
	metadataSchema := map[string]any{
		"type": "object",
		"properties": map[string]any{
			"label": map[string]any{
				"type":        "string",
				"minLength":   1,
				"description": "Short visible label such as Category or Due.",
			},
			"value": map[string]any{
				"type":        "string",
				"minLength":   1,
				"description": "Human-readable value displayed beside the metadata label.",
			},
			"attention": map[string]any{
				"type":        "boolean",
				"description": "Whether the Review screen should emphasize this value. Copied legacy open metadata may omit it; a replacement for a completed task must include it.",
			},
		},
		"required":             []string{"label", "value"},
		"additionalProperties": false,
	}

	entrySchema := map[string]any{
		"type": "object",
		"properties": map[string]any{
			"id": map[string]any{
				"type":        "string",
				"minLength":   1,
				"description": "Copy this identifier for an unfinished input task; create a new unique identifier only when replacing a completed task.",
			},
			"title": map[string]any{
				"type":        "string",
				"minLength":   1,
				"description": "Concise household task heading shown in the Home section.",
			},
			"details": map[string]any{
				"type":        "string",
				"description": "Specific instructions or context that make the household task easy to start. A copied legacy open task may omit it; a completed-task replacement must include it, using an empty string when no detail is needed.",
			},
			"href": map[string]any{
				"type":        "string",
				"description": "Task destination represented as an empty string when the task has no link. A copied legacy open task may omit it; a completed-task replacement must include it.",
			},
			"state": map[string]any{
				"type":        "string",
				"enum":        []string{"open"},
				"description": "Every returned task is open: unfinished input tasks stay open and replacements for completed tasks begin open.",
			},
			"metadata": map[string]any{
				"type":        "array",
				"minItems":    1,
				"maxItems":    2,
				"description": "One or two compact details, such as a category and due date, displayed beneath the task. A copied legacy open task may omit metadata; a completed-task replacement must include it.",
				"items":       metadataSchema,
			},
		},
		// A provider cannot express whether an entry copied an open input task or
		// replaced a done one. Requiring only the identity/state fields permits
		// copied legacy rows, while descriptions and area_content_validation.go
		// require details, href, metadata, and attention for every replacement.
		"required":             []string{"id", "title", "state"},
		"additionalProperties": false,
	}

	responseFormat := components.CreateResponseFormatJSONSchema(
		components.ChatFormatJSONSchemaConfig{
			JSONSchema: components.ChatJSONSchemaConfig{
				Name: maintenanceTasksContentType,
				Schema: map[string]any{
					"type":        "object",
					"description": "Return the complete Home document. Copy every input entry whose state is open without changing any field, including omitted legacy fields. Replace only entries whose input state is done, keeping the same array length and order. Each completed-task replacement must include id, title, details, href, state, metadata, and attention on every metadata item.",
					"properties": map[string]any{
						"entries": map[string]any{
							"type":        "array",
							"minItems":    1,
							"description": "All Home tasks in their original order; only positions that held completed input tasks may contain new tasks.",
							"items":       entrySchema,
						},
					},
					"required":             []string{"entries"},
					"additionalProperties": false,
				},
				// Home must be able to reproduce a legacy unfinished task whose
				// optional properties were genuinely absent. Some providers' strict
				// structured-output dialects require every declared property, which
				// would make that exact copy impossible before generation even starts.
				// The response still carries this JSON Schema. The validator in
				// area_content_validation.go applies strict decoding plus the stronger
				// position-aware preservation rules before any generated document can
				// be saved.
				Strict: optionalnullable.From(openrouter.Pointer(false)),
			},
		},
	)
	return responseFormat
}

// weeklyMealRecommendationsResponseFormat defines the complete Meals document
// shown in the Review screen. The calendar helper supplies the target Sunday,
// and this schema requires one ordered recommendation for every day through the
// following Saturday. Tags remain a list so the model can describe ingredients,
// cooking style, or other useful planning categories without adding new fields.
// Called by responseFormatForContentType in response_formats.go.
func weeklyMealRecommendationsResponseFormat() components.ResponseFormat {
	recommendationSchema := map[string]any{
		"type": "object",
		"properties": map[string]any{
			"day": map[string]any{
				"type": "string",
				"enum": []string{
					"Sunday",
					"Monday",
					"Tuesday",
					"Wednesday",
					"Thursday",
					"Friday",
					"Saturday",
				},
				"description": "Weekday represented by this meal recommendation.",
			},
			"title": map[string]any{
				"type":        "string",
				"minLength":   1,
				"description": "Concise meal name shown as the Review entry title.",
			},
			"description": map[string]any{
				"type":        "string",
				"minLength":   1,
				"description": "Short practical description of the meal and its principal sides or preparation.",
			},
			"tags": map[string]any{
				"type":        "array",
				"description": "Brief planning labels such as chicken, vegetarian, quick, or sheet pan.",
				"items": map[string]any{
					"type":      "string",
					"minLength": 1,
				},
				"uniqueItems": true,
			},
		},
		"required":             []string{"day", "title", "description", "tags"},
		"additionalProperties": false,
	}

	responseFormat := components.CreateResponseFormatJSONSchema(
		components.ChatFormatJSONSchemaConfig{
			JSONSchema: components.ChatJSONSchemaConfig{
				Name: weeklyMealRecommendationsContentType,
				Schema: map[string]any{
					"type": "object",
					"properties": map[string]any{
						"week_starting": map[string]any{
							"type":        "string",
							"pattern":     `^\d{4}-\d{2}-\d{2}$`,
							"description": "Sunday that begins this meal-planning week, formatted as YYYY-MM-DD.",
						},
						"recommendations": map[string]any{
							"type":        "array",
							"minItems":    7,
							"maxItems":    7,
							"description": "Exactly seven meal recommendations ordered from Sunday through Saturday.",
							"items":       recommendationSchema,
						},
					},
					"required":             []string{"week_starting", "recommendations"},
					"additionalProperties": false,
				},
				Strict: optionalnullable.From(openrouter.Pointer(true)),
			},
		},
	)
	return responseFormat
}

// weeklyWorkoutRoutineResponseFormat defines the complete JSON contract used by
// the Health section. The stored document currently contains seven entries—one
// for each day from Sunday through Saturday—and the frontend translates these
// fields directly into visible Review rows.
//
// Every object rejects additional properties so a model cannot introduce an
// undocumented field that the frontend silently ignores. All currently stored
// fields are required, including href and attention; entries without a link use
// an empty href string, preserving one predictable shape across all seven days.
// Called by responseFormatForContentType in response_formats.go.
func weeklyWorkoutRoutineResponseFormat() components.ResponseFormat {
	metadataSchema := map[string]any{
		"type": "object",
		"properties": map[string]any{
			"label": map[string]any{
				"type":        "string",
				"minLength":   1,
				"description": "Short visible label such as Plan or Duration.",
			},
			"value": map[string]any{
				"type":        "string",
				"minLength":   1,
				"description": "Human-readable value displayed beside the metadata label.",
			},
			"attention": map[string]any{
				"type":        "boolean",
				"description": "Whether the Review screen should visually emphasize this metadata value.",
			},
		},
		"required":             []string{"label", "value", "attention"},
		"additionalProperties": false,
	}

	entrySchema := map[string]any{
		"type": "object",
		"properties": map[string]any{
			"id": map[string]any{
				"type":        "string",
				"minLength":   1,
				"description": "Stable, unique identifier for this day's Review entry.",
			},
			"title": map[string]any{
				"type":        "string",
				"minLength":   1,
				"description": "Concise workout or recovery-day heading shown in the Review list.",
			},
			"details": map[string]any{
				"type":        "string",
				"minLength":   1,
				"description": "Complete, readable instructions for the day's workout or recovery activity.",
			},
			"href": map[string]any{
				"type":        "string",
				"description": "Optional destination represented as an empty string when this entry has no link.",
			},
			"state": map[string]any{
				"type":        "string",
				"enum":        []string{"open", "done"},
				"description": "Completion state understood by the Review screen.",
			},
			"metadata": map[string]any{
				"type":        "array",
				"minItems":    2,
				"maxItems":    2,
				"description": "Exactly two compact label/value details displayed beneath the entry.",
				"items":       metadataSchema,
			},
		},
		"required": []string{
			"id",
			"title",
			"details",
			"href",
			"state",
			"metadata",
		},
		"additionalProperties": false,
	}

	responseFormat := components.CreateResponseFormatJSONSchema(
		components.ChatFormatJSONSchemaConfig{
			JSONSchema: components.ChatJSONSchemaConfig{
				Name: "weekly_workout_routine",
				Schema: map[string]any{
					"type": "object",
					"properties": map[string]any{
						"last_updated": map[string]any{
							"type":        "string",
							"pattern":     `^\d{4}-\d{2}-\d{2}$`,
							"description": "Calendar date when this weekly routine was generated, formatted as YYYY-MM-DD.",
						},
						"entries": map[string]any{
							"type":        "array",
							"minItems":    7,
							"maxItems":    7,
							"description": "Seven ordered Review entries covering Sunday through Saturday.",
							"items":       entrySchema,
						},
					},
					"required":             []string{"last_updated", "entries"},
					"additionalProperties": false,
				},
				Strict: optionalnullable.From(openrouter.Pointer(true)),
			},
		},
	)
	return responseFormat
}
