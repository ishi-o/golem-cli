package bootstrap

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	sqlxstore "github.com/ishi-o/golem/store/sqlx"
	"github.com/jmoiron/sqlx"
)

// SessionMessage is the CLI-facing representation of one persisted message.
type SessionMessage struct {
	Role    string
	Content string
}

// ListSessions returns conversation ids persisted by golem's SQLX store.
// It opens only the store, so listing sessions does not require model
// credentials.
func ListSessions() ([]string, error) {
	ctx := context.Background()
	backend, db, err := openSessionStore(ctx)
	if err != nil {
		return nil, err
	}
	defer db.Close()
	return backend.ListConversations(ctx)
}

// LoadSessionHistory returns the persisted messages for one conversation.
// System prompts are not persisted by golem's agent, so this is the user,
// assistant, and tool history that will be resumed by a later run.
func LoadSessionHistory(sessionID string) ([]SessionMessage, error) {
	sessionID = strings.TrimSpace(sessionID)
	if sessionID == "" {
		return nil, fmt.Errorf("bootstrap: session id is empty")
	}
	ctx := context.Background()
	backend, db, err := openSessionStore(ctx)
	if err != nil {
		return nil, err
	}
	defer db.Close()
	messages, err := backend.Load(ctx, sessionID, 0)
	if err != nil {
		return nil, err
	}
	result := make([]SessionMessage, 0, len(messages))
	for _, message := range messages {
		if message == nil {
			continue
		}
		result = append(result, SessionMessage{Role: string(message.Role), Content: message.Content})
	}
	return result, nil
}

func openSessionStore(ctx context.Context) (*sqlxstore.Store, *sqlx.DB, error) {
	settings, err := LoadSettings()
	if err != nil {
		return nil, nil, err
	}
	dbPath := firstNonEmpty(os.Getenv(sqliteEnv), settings.SQLitePath)
	if dbPath == "" {
		dbPath = filepath.Join(settings.Config.Storage.Location, "golem.db")
	}
	db, err := openSQLite(ctx, dbPath)
	if err != nil {
		return nil, nil, err
	}
	backend, err := sqlxstore.New(db, sqlxstore.WithDialect(sqlxstore.DialectSQLite))
	if err != nil {
		_ = db.Close()
		return nil, nil, fmt.Errorf("bootstrap: create sqlite store: %w", err)
	}
	if err := backend.Migrate(ctx); err != nil {
		_ = db.Close()
		return nil, nil, fmt.Errorf("bootstrap: migrate sqlite store: %w", err)
	}
	return backend, db, nil
}
