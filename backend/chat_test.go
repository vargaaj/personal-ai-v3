// chat_test.go verifies OpenRouter model and privacy selection without sending
// personal JSON to a provider or requiring an API key.
package main

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestBuildChatRequestUsesLingAndZDRForPrivateSystems(t *testing.T) {
	t.Parallel()

	request := buildChatRequest(
		"Update Email.",
		`{"messages":[]}`,
		"email_summary",
		true,
	)
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

	request := buildChatRequest(
		"Update Reading.",
		`{"entries":[]}`,
		"reading_queue",
		false,
	)
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
	if _, hasResponseFormat := payload["response_format"]; hasResponseFormat {
		t.Fatal("prompt-only request unexpectedly included a response schema")
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

func TestBuildChatRequestUsesStrictSchemaForWeeklyMealRecommendations(t *testing.T) {
	t.Parallel()

	request := buildChatRequest(
		"Create one meal recommendation for every day of the target week.",
		`{"week_starting":"2026-07-26","recommendations":[]}`,
		weeklyMealRecommendationsContentType,
		false,
	)
	payload := marshalChatRequestForTest(t, request)

	assertRequestRequiresResponseFormatForTest(t, payload)
	_, schema := responseSchemaForTest(t, payload, weeklyMealRecommendationsContentType, true)

	properties := objectFieldForTest(t, schema, "properties")
	recommendations := objectFieldForTest(t, properties, "recommendations")
	if gotMinimum := recommendations["minItems"]; gotMinimum != float64(7) {
		t.Fatalf("meal recommendations minItems = %v, want 7", gotMinimum)
	}
	if gotMaximum := recommendations["maxItems"]; gotMaximum != float64(7) {
		t.Fatalf("meal recommendations maxItems = %v, want 7", gotMaximum)
	}

	recommendationSchema := objectFieldForTest(t, recommendations, "items")
	recommendationProperties := objectFieldForTest(t, recommendationSchema, "properties")
	daySchema := objectFieldForTest(t, recommendationProperties, "day")
	weekdays, ok := daySchema["enum"].([]any)
	if !ok {
		t.Fatalf("meal day enum = %#v, want an array", daySchema["enum"])
	}
	wantWeekdays := []string{
		"Sunday",
		"Monday",
		"Tuesday",
		"Wednesday",
		"Thursday",
		"Friday",
		"Saturday",
	}
	if len(weekdays) != len(wantWeekdays) {
		t.Fatalf("len(meal day enum) = %d, want %d", len(weekdays), len(wantWeekdays))
	}
	for index, wantWeekday := range wantWeekdays {
		if weekdays[index] != wantWeekday {
			t.Fatalf("meal day enum %d = %v, want %q", index, weekdays[index], wantWeekday)
		}
	}
}

func TestBuildChatRequestUsesLegacyCompatibleTaskSchemaForHome(t *testing.T) {
	t.Parallel()

	request := buildChatRequest(
		"Replace completed household tasks while preserving unfinished tasks.",
		`{"entries":[]}`,
		maintenanceTasksContentType,
		false,
	)
	payload := marshalChatRequestForTest(t, request)

	assertRequestRequiresResponseFormatForTest(t, payload)
	_, schema := responseSchemaForTest(t, payload, maintenanceTasksContentType, false)

	properties := objectFieldForTest(t, schema, "properties")
	entries := objectFieldForTest(t, properties, "entries")
	entrySchema := objectFieldForTest(t, entries, "items")
	entryProperties := objectFieldForTest(t, entrySchema, "properties")
	stateSchema := objectFieldForTest(t, entryProperties, "state")
	states, ok := stateSchema["enum"].([]any)
	if !ok {
		t.Fatalf("Home state enum = %#v, want an array", stateSchema["enum"])
	}
	if len(states) != 1 || states[0] != "open" {
		t.Fatalf("Home state enum = %#v, want only open", states)
	}

	// JSON Schema cannot know which output entries copied old open tasks, so it
	// must permit their legacy omissions. The server validates the stricter
	// replacement-only fields after it compares each output position to its input.
	requiredEntryFields, ok := entrySchema["required"].([]any)
	if !ok {
		t.Fatalf("Home entry required fields = %#v, want an array", entrySchema["required"])
	}
	wantRequiredEntryFields := []string{"id", "title", "state"}
	if len(requiredEntryFields) != len(wantRequiredEntryFields) {
		t.Fatalf(
			"len(Home entry required fields) = %d, want %d",
			len(requiredEntryFields),
			len(wantRequiredEntryFields),
		)
	}
	for index, wantField := range wantRequiredEntryFields {
		if requiredEntryFields[index] != wantField {
			t.Fatalf(
				"Home entry required field %d = %v, want %q",
				index,
				requiredEntryFields[index],
				wantField,
			)
		}
	}
	if gotAdditionalProperties := entrySchema["additionalProperties"]; gotAdditionalProperties != false {
		t.Fatalf("Home entry additionalProperties = %v, want false", gotAdditionalProperties)
	}

	metadataSchema := objectFieldForTest(t, entryProperties, "metadata")
	metadataItemSchema := objectFieldForTest(t, metadataSchema, "items")
	requiredMetadataFields, ok := metadataItemSchema["required"].([]any)
	if !ok {
		t.Fatalf("Home metadata required fields = %#v, want an array", metadataItemSchema["required"])
	}
	wantRequiredMetadataFields := []string{"label", "value"}
	if len(requiredMetadataFields) != len(wantRequiredMetadataFields) {
		t.Fatalf(
			"len(Home metadata required fields) = %d, want %d",
			len(requiredMetadataFields),
			len(wantRequiredMetadataFields),
		)
	}
	for index, wantField := range wantRequiredMetadataFields {
		if requiredMetadataFields[index] != wantField {
			t.Fatalf(
				"Home metadata required field %d = %v, want %q",
				index,
				requiredMetadataFields[index],
				wantField,
			)
		}
	}
	if gotAdditionalProperties := metadataItemSchema["additionalProperties"]; gotAdditionalProperties != false {
		t.Fatalf("Home metadata additionalProperties = %v, want false", gotAdditionalProperties)
	}

	// The schema description supplies the relationship JSON Schema cannot encode:
	// returned open tasks are copied, while only input tasks marked done change
	// and receive the complete current task shape.
	description, ok := schema["description"].(string)
	if !ok {
		t.Fatalf("Home schema description = %#v, want a string", schema["description"])
	}
	if !strings.Contains(description, "state is open without changing any field") {
		t.Fatalf("Home schema description omitted the preserve-open rule: %q", description)
	}
	if !strings.Contains(description, "input state is done") {
		t.Fatalf("Home schema description omitted the replace-completed rule: %q", description)
	}
	if !strings.Contains(description, "must include id, title, details, href, state, metadata") {
		t.Fatalf("Home schema description omitted the complete replacement rule: %q", description)
	}
}

func TestBuildChatRequestUsesStrictSchemaForWeeklyWorkoutRoutine(t *testing.T) {
	t.Parallel()

	request := buildChatRequest(
		"Create the next Sunday-through-Saturday workout routine.",
		`{"last_updated":"2026-07-26","entries":[]}`,
		weeklyWorkoutRoutineContentType,
		false,
	)
	encodedRequest, err := json.Marshal(request)
	if err != nil {
		t.Fatalf("marshal Health chat request: %v", err)
	}

	var payload map[string]any
	if err := json.Unmarshal(encodedRequest, &payload); err != nil {
		t.Fatalf("decode Health chat request: %v", err)
	}

	// OpenRouter must reject providers that would silently ignore the workout
	// response schema. Health is not a ZDR target, so this provider object should
	// contain the parameter requirement without inheriting Email's privacy flags.
	provider, ok := payload["provider"].(map[string]any)
	if !ok {
		t.Fatalf("provider = %#v, want an object", payload["provider"])
	}
	if gotRequireParameters := provider["require_parameters"]; gotRequireParameters != true {
		t.Fatalf("provider.require_parameters = %v, want true", gotRequireParameters)
	}
	if _, hasZDR := provider["zdr"]; hasZDR {
		t.Fatal("Health request unexpectedly included the Email ZDR restriction")
	}
	if _, hasCollectionPolicy := provider["data_collection"]; hasCollectionPolicy {
		t.Fatal("Health request unexpectedly included the Email data-collection restriction")
	}

	responseFormat, ok := payload["response_format"].(map[string]any)
	if !ok {
		t.Fatalf("response_format = %#v, want an object", payload["response_format"])
	}
	if gotType := responseFormat["type"]; gotType != "json_schema" {
		t.Fatalf("response_format.type = %v, want json_schema", gotType)
	}

	jsonSchema, ok := responseFormat["json_schema"].(map[string]any)
	if !ok {
		t.Fatalf("response_format.json_schema = %#v, want an object", responseFormat["json_schema"])
	}
	if gotName := jsonSchema["name"]; gotName != weeklyWorkoutRoutineContentType {
		t.Fatalf(
			"response_format.json_schema.name = %v, want %q",
			gotName,
			weeklyWorkoutRoutineContentType,
		)
	}
	if gotStrict := jsonSchema["strict"]; gotStrict != true {
		t.Fatalf("response_format.json_schema.strict = %v, want true", gotStrict)
	}

	schema, ok := jsonSchema["schema"].(map[string]any)
	if !ok {
		t.Fatalf("response_format.json_schema.schema = %#v, want an object", jsonSchema["schema"])
	}
	if gotAdditionalProperties := schema["additionalProperties"]; gotAdditionalProperties != false {
		t.Fatalf(
			"workout schema additionalProperties = %v, want false",
			gotAdditionalProperties,
		)
	}

	properties, ok := schema["properties"].(map[string]any)
	if !ok {
		t.Fatalf("workout schema properties = %#v, want an object", schema["properties"])
	}
	entries, ok := properties["entries"].(map[string]any)
	if !ok {
		t.Fatalf("workout entries schema = %#v, want an object", properties["entries"])
	}
	if gotMinimum := entries["minItems"]; gotMinimum != float64(7) {
		t.Fatalf("workout entries minItems = %v, want 7", gotMinimum)
	}
	if gotMaximum := entries["maxItems"]; gotMaximum != float64(7) {
		t.Fatalf("workout entries maxItems = %v, want 7", gotMaximum)
	}

	entrySchema, ok := entries["items"].(map[string]any)
	if !ok {
		t.Fatalf("workout entry schema = %#v, want an object", entries["items"])
	}
	requiredFields, ok := entrySchema["required"].([]any)
	if !ok {
		t.Fatalf("workout entry required fields = %#v, want an array", entrySchema["required"])
	}
	wantRequiredFields := []string{"id", "title", "details", "href", "state", "metadata"}
	if len(requiredFields) != len(wantRequiredFields) {
		t.Fatalf(
			"len(workout entry required fields) = %d, want %d",
			len(requiredFields),
			len(wantRequiredFields),
		)
	}
	for index, wantField := range wantRequiredFields {
		if requiredFields[index] != wantField {
			t.Fatalf(
				"workout entry required field %d = %v, want %q",
				index,
				requiredFields[index],
				wantField,
			)
		}
	}
}

// marshalChatRequestForTest converts the generated SDK request into ordinary
// maps and arrays. That representation lets the focused schema tests inspect
// the exact JSON OpenRouter receives without making a network request.
func marshalChatRequestForTest(t *testing.T, request any) map[string]any {
	t.Helper()

	encodedRequest, err := json.Marshal(request)
	if err != nil {
		t.Fatalf("marshal chat request: %v", err)
	}

	var payload map[string]any
	if err := json.Unmarshal(encodedRequest, &payload); err != nil {
		t.Fatalf("decode chat request: %v", err)
	}
	return payload
}

// assertRequestRequiresResponseFormatForTest verifies the OpenRouter provider
// cannot accept the request while silently dropping its structured response.
func assertRequestRequiresResponseFormatForTest(t *testing.T, payload map[string]any) {
	t.Helper()

	provider := objectFieldForTest(t, payload, "provider")
	if gotRequireParameters := provider["require_parameters"]; gotRequireParameters != true {
		t.Fatalf("provider.require_parameters = %v, want true", gotRequireParameters)
	}
	if _, hasZDR := provider["zdr"]; hasZDR {
		t.Fatal("ordinary structured request unexpectedly included the Email ZDR restriction")
	}
	if _, hasCollectionPolicy := provider["data_collection"]; hasCollectionPolicy {
		t.Fatal("ordinary structured request unexpectedly included the Email data-collection restriction")
	}
}

// responseSchemaForTest extracts one JSON Schema and confirms that its public
// name and strictness match the stored content type used to select it. Health
// and Meals require strict provider output; Home deliberately does not because
// it may need to copy an unfinished legacy row with omitted optional fields.
func responseSchemaForTest(
	t *testing.T,
	payload map[string]any,
	wantName string,
	wantStrict bool,
) (map[string]any, map[string]any) {
	t.Helper()

	responseFormat := objectFieldForTest(t, payload, "response_format")
	if gotType := responseFormat["type"]; gotType != "json_schema" {
		t.Fatalf("response_format.type = %v, want json_schema", gotType)
	}

	jsonSchema := objectFieldForTest(t, responseFormat, "json_schema")
	if gotName := jsonSchema["name"]; gotName != wantName {
		t.Fatalf("response_format.json_schema.name = %v, want %q", gotName, wantName)
	}
	if gotStrict := jsonSchema["strict"]; gotStrict != wantStrict {
		t.Fatalf("response_format.json_schema.strict = %v, want %v", gotStrict, wantStrict)
	}

	schema := objectFieldForTest(t, jsonSchema, "schema")
	if gotAdditionalProperties := schema["additionalProperties"]; gotAdditionalProperties != false {
		t.Fatalf("schema additionalProperties = %v, want false", gotAdditionalProperties)
	}
	return jsonSchema, schema
}

// objectFieldForTest reads a nested JSON object and reports the surrounding
// field name when a generated request contains an unexpected shape.
func objectFieldForTest(t *testing.T, object map[string]any, field string) map[string]any {
	t.Helper()

	value, ok := object[field].(map[string]any)
	if !ok {
		t.Fatalf("%s = %#v, want an object", field, object[field])
	}
	return value
}
