package main

import (
	"fmt"
	"log"
	"os"
	"time"

	"github.com/joho/godotenv"
)

func main() {
	// Load OPENROUTER_API_KEY and any other local secrets from backend/.env.local
	// before updateJSON calls sendChatMessage. godotenv adds those values to the
	// process environment, which is where chat.go reads OPENROUTER_API_KEY from.
	err := godotenv.Load(".env.local")
	if err != nil {
		// A missing .env.local is allowed because production shells or CI jobs may
		// provide OPENROUTER_API_KEY directly. Other errors, such as a malformed
		// file that exists, should stop the run so the config can be fixed.
		if os.IsNotExist(err) {
			log.Println("no .env.local found; using system environment")
		} else {
			log.Fatal(err)
		}
	}

	// These paths are relative to the backend folder because the Go module lives
	// in backend/. Run this program from backend/ with `go run .`.
	workoutJSONPath := "../storage/weekly_workout_routine.json"
	workoutPromptPath := "../storage/weekly_workout_update_prompt.txt"

	// Load the current workout routine as raw JSON text. loadJSONFile also checks
	// that the file is not empty and contains valid JSON before the LLM sees it.
	originalJSON, err := loadJSONFile(workoutJSONPath)
	if err != nil {
		log.Fatal(err)
	}

	// Load the reusable instruction prompt through storage.go. That keeps
	// main.go focused on the update workflow instead of duplicating file-reading
	// rules for JSON files and prompt files.
	basePrompt, err := loadPromptFile(workoutPromptPath)
	if err != nil {
		log.Fatal(err)
	}

	// Format today's date as YYYY-MM-DD so the LLM has a concrete value for the
	// last_updated field. Go uses the specific reference date "2006-01-02" as its
	// formatting pattern, so this produces values like "2026-07-20".
	currentDate := time.Now().Format("2006-01-02")

	// The prompt file gives the reusable update instructions. main.go appends the
	// current date at runtime because the prompt file itself is static and would
	// otherwise go stale. The LLM receives this date along with the original JSON,
	// then updateJSON validates the LLM response before main.go writes it.
	updatePrompt := basePrompt + "\n\nCurrent date for last_updated:\n" + currentDate
	updatedJSON, err := updateJSON(nil, updatePrompt, originalJSON)
	if err != nil {
		log.Fatal(err)
	}

	// Only write after updateJSON succeeds. That means invalid JSON or JSON with a
	// changed structure never replaces the saved workout routine.
	err = os.WriteFile(workoutJSONPath, []byte(updatedJSON+"\n"), 0644)
	if err != nil {
		log.Fatal(err)
	}

	fmt.Println("updated", workoutJSONPath)

	// // mux is a multiplexer - handles routing for the server
	// mux := http.NewServeMux()

	// mux.HandleFunc("GET /", func(w http.ResponseWriter, r *http.Request) {
	// 	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	// 	fmt.Fprint(w, "Hello world")
	// })

	// log.Fatal(http.ListenAndServe(":8081", mux))
}
