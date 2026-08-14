//go:build integration

package workflow

import (
	"context"
	"os"
	"sync"
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
	otherPublished, changed, err := restarted.PublishDefinitionVersion(ctx, other, "other-workspace-publish")
	if err != nil || !changed || otherPublished.Version != 1 {
		t.Fatalf("cross-workspace same definition publish: version=%+v changed=%v err=%v", otherPublished, changed, err)
	}
	if got, err := restarted.ListPublishedDefinitionVersions(ctx, other.WorkspaceID, false); err != nil || len(got) != 1 || got[0].DefinitionID != v.DefinitionID {
		t.Fatalf("workspace isolation: got=%v err=%v", got, err)
	}

	// Distinct publishers can race on the same next version. The repository must
	// return two durable, sequential versions instead of leaking a transient
	// uniqueness conflict to either caller.
	type publishResult struct {
		version WorkflowDefinitionVersion
		changed bool
		err     error
	}
	results := make(chan publishResult, 2)
	start := make(chan struct{})
	var wg sync.WaitGroup
	for _, key := range []string{"concurrent-publish-1", "concurrent-publish-2"} {
		wg.Add(1)
		go func(idempotencyKey string) {
			defer wg.Done()
			<-start
			version, changed, publishErr := restarted.PublishDefinitionVersion(ctx, v, idempotencyKey)
			results <- publishResult{version: version, changed: changed, err: publishErr}
		}(key)
	}
	close(start)
	wg.Wait()
	close(results)
	versions := map[int]bool{}
	for result := range results {
		if result.err != nil || !result.changed {
			t.Fatalf("concurrent publish: version=%+v changed=%v err=%v", result.version, result.changed, result.err)
		}
		versions[result.version.Version] = true
	}
	if !versions[3] || !versions[4] || len(versions) != 2 {
		t.Fatalf("concurrent publishers did not receive sequential versions: %v", versions)
	}
}
