package session

import (
	"encoding/json"
	"fmt"
	"log"
	"os"
)

// MigrateFromJSON reads an existing sessions.json file and imports
// records into the SQLite store. Migration only runs if the JSON file
// exists and the database has no existing sessions.
func MigrateFromJSON(jsonPath string, store *SQLiteStore) error {
	raw, err := os.ReadFile(jsonPath)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return fmt.Errorf("migrate: read %q: %w", jsonPath, err)
	}

	var count int
	if err := store.db.QueryRow(`SELECT COUNT(*) FROM sessions`).Scan(&count); err != nil {
		return fmt.Errorf("migrate: check existing sessions: %w", err)
	}
	if count > 0 {
		return nil
	}

	var data map[string]*Session
	if err := json.Unmarshal(raw, &data); err != nil {
		return fmt.Errorf("migrate: parse %q: %w", jsonPath, err)
	}

	for _, sess := range data {
		if err := store.Create(sess); err != nil {
			return fmt.Errorf("migrate: import session %q: %w", sess.ID, err)
		}
	}

	log.Printf("migrate: imported %d sessions from %s", len(data), jsonPath)
	return nil
}
