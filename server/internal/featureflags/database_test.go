package featureflags

import (
	"context"
	"github.com/jackc/pgx/v5/pgxpool"
	"os"
	"testing"
)

func TestOverrideSchema(t *testing.T) {
	url := os.Getenv("DATABASE_URL")
	if url == "" {
		t.Skip("isolated DATABASE_URL required")
	}
	pool, err := pgxpool.New(context.Background(), url)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	tx, err := pool.Begin(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(context.Background())
	_, err = tx.Exec(context.Background(), `INSERT INTO workspace_feature_flag_override(workspace_id,scope_type,scope_id,flag_key,enabled,updated_by_type) VALUES ('11111111-1111-1111-1111-111111111111','workspace','11111111-1111-1111-1111-111111111111','plugins_v1',true,'system')`)
	if err != nil {
		t.Fatal(err)
	}
	// A member override cannot forge a workspace lock, even outside the API.
	_, err = tx.Exec(context.Background(), `INSERT INTO workspace_feature_flag_override(workspace_id,scope_type,scope_id,flag_key,enabled,locked,updated_by_type) VALUES ('11111111-1111-1111-1111-111111111111','member','22222222-2222-2222-2222-222222222222','plugins_v1',true,true,'system')`)
	if err == nil {
		t.Fatal("member lock accepted")
	}
}
