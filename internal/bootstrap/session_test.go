package bootstrap

import (
	"context"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/schema"
	"github.com/ishi-o/golem/core/agent"
	coreconfig "github.com/ishi-o/golem/core/config"
	"github.com/ishi-o/golem/core/storage"
	"github.com/ishi-o/golem/core/tools"
	sqlxstore "github.com/ishi-o/golem/store/sqlx"
	"github.com/stretchr/testify/require"
)

func TestSQLXStorePersistsSessionConversation(t *testing.T) {
	ctx := context.Background()
	db, err := openSQLite(ctx, filepath.Join(t.TempDir(), "session.db"))
	require.NoError(t, err)
	defer db.Close()

	backend, err := sqlxstore.New(db, sqlxstore.WithDialect(sqlxstore.DialectSQLite))
	require.NoError(t, err)
	require.NoError(t, backend.Migrate(ctx))

	require.NoError(t, backend.Append(ctx, "trip", []*schema.Message{
		schema.UserMessage("plan a trip"),
		schema.AssistantMessage("where would you like to go?", nil),
	}))
	require.NoError(t, backend.Append(ctx, "trip", []*schema.Message{
		schema.UserMessage("Kyoto"),
	}))

	conversation, err := backend.Load(ctx, "trip", 0)
	require.NoError(t, err)
	require.Len(t, conversation, 3)
	require.Equal(t, "plan a trip", conversation[0].Content)
	require.Equal(t, "Kyoto", conversation[2].Content)

	ids, err := backend.ListConversations(ctx)
	require.NoError(t, err)
	require.Equal(t, []string{"trip"}, ids)
}

type sessionTestModel struct {
	mu    sync.Mutex
	input [][]*schema.Message
}

func (m *sessionTestModel) WithTools([]*schema.ToolInfo) (model.ToolCallingChatModel, error) {
	return m, nil
}

func (m *sessionTestModel) Generate(_ context.Context, messages []*schema.Message, _ ...model.Option) (*schema.Message, error) {
	return m.reply(messages), nil
}

func (m *sessionTestModel) Stream(_ context.Context, messages []*schema.Message, _ ...model.Option) (*schema.StreamReader[*schema.Message], error) {
	return schema.StreamReaderFromArray([]*schema.Message{m.reply(messages)}), nil
}

func (m *sessionTestModel) reply(messages []*schema.Message) *schema.Message {
	m.mu.Lock()
	copyOfInput := append([]*schema.Message(nil), messages...)
	m.input = append(m.input, copyOfInput)
	m.mu.Unlock()
	return &schema.Message{Role: schema.Assistant, Content: "ack"}
}

func TestAgentLoadsAndAppendsSQLXSessionHistory(t *testing.T) {
	ctx := context.Background()
	db, err := openSQLite(ctx, filepath.Join(t.TempDir(), "agent-session.db"))
	require.NoError(t, err)
	defer db.Close()
	backend, err := sqlxstore.New(db, sqlxstore.WithDialect(sqlxstore.DialectSQLite))
	require.NoError(t, err)
	require.NoError(t, backend.Migrate(ctx))

	cfg := coreconfig.Config{}
	workspaces := storage.NewWorkspaceFactory(t.TempDir())
	provider := tools.NewProvider(cfg, workspaces, backend, nil)
	model := &sessionTestModel{}
	runner := agent.New(model, backend, provider, cfg)

	fire := func(id, text string) {
		done := make(chan struct{})
		require.NoError(t, runner.Fire(agent.NewRequest(agent.ChatScenario, text,
			agent.WithRequestID(id),
			agent.WithIdentity("local", "trip", "cli"),
			agent.WithConversation("trip", "trip", id),
			agent.WithListener(agent.ListenerFuncs{OnFinishedFunc: func(agent.Outcome) { close(done) }}),
		)))
		select {
		case <-done:
		case <-time.After(time.Second):
			t.Fatal("agent run did not finish")
		}
	}

	fire("trip-1", "first")
	fire("trip-2", "second")
	require.NoError(t, runner.Shutdown(ctx))

	conversation, err := backend.Load(ctx, "trip", 0)
	require.NoError(t, err)
	require.Len(t, conversation, 4)
	require.Equal(t, "first", conversation[0].Content)
	require.Equal(t, "second", conversation[2].Content)

	model.mu.Lock()
	defer model.mu.Unlock()
	require.Len(t, model.input, 2)
	// The second model call includes the first run's user/assistant pair.
	require.Contains(t, contents(model.input[1]), "first")
}

func contents(messages []*schema.Message) []string {
	result := make([]string, 0, len(messages))
	for _, message := range messages {
		result = append(result, message.Content)
	}
	return result
}
