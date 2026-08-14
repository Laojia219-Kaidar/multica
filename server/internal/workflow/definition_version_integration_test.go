//go:build integration

package workflow

import (
	"context"
	"os"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

func TestDefinitionVersionPublishIsImmutableWorkspaceScopedAndRestartReadable(t *testing.T) {
	url := os.Getenv("DATABASE_URL")
	if url == "" {
		t.Skip("DATABASE_URL not set")
	}
	ctx := context.Background()
	p, err := pgxpool.New(ctx, url)
	if err != nil {
		t.Fatalf("pool: %v", err)
	}
	defer p.Close()
	q := db.New(p)
	workspace := uuid.NewString()
	v := validPublishedVersion()
	v.WorkspaceID = workspace
	r := NewRepository(q)
	first, changed, err := r.PublishDefinitionVersion(ctx, v, "publish-1")
	if err != nil || !changed || first.Version != 1 || first.Digest == "" {
		t.Fatalf("first publish: version=%+v changed=%v err=%v", first, changed, err)
	}
	restarted := NewRepository(q)
	loaded, err := restarted.LoadPublishedDefinitionVersion(ctx, workspace, v.DefinitionID, 1)
	if err != nil || loaded.Digest != first.Digest || len(loaded.Graph.Nodes) != 2 {
		t.Fatalf("restart readback: version=%+v err=%v", loaded, err)
	}
	replayed, changed, err := restarted.PublishDefinitionVersion(ctx, v, "publish-1")
	if err != nil || changed || replayed.Version != 1 || replayed.Digest != first.Digest {
		t.Fatalf("idempotent replay: version=%+v changed=%v err=%v", replayed, changed, err)
	}
	second, changed, err := restarted.PublishDefinitionVersion(ctx, v, "publish-2")
	if err != nil || !changed || second.Version != 2 {
		t.Fatalf("second version: version=%+v changed=%v err=%v", second, changed, err)
	}
	other := v
	other.WorkspaceID = uuid.NewString()
	if got, err := restarted.ListPublishedDefinitionVersions(ctx, other.WorkspaceID, false); err != nil || len(got) != 0 {
		t.Fatalf("workspace isolation: got=%v err=%v", got, err)
	}
}
