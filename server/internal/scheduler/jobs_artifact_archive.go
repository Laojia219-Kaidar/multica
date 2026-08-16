package scheduler

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/multica-ai/multica/server/internal/companyops"
	"github.com/multica-ai/multica/server/internal/util"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

// JobNameArtifactArchiveReconciler is the canonical audit-row name.
const JobNameArtifactArchiveReconciler = "artifact_archive_reconciler"

// artifactArchiveBatchLimit bounds candidates mirrored per cycle so one pass
// never moves unbounded bytes through the NAS mount.
const artifactArchiveBatchLimit = 25

// ArtifactArchiveReconcilerJob returns the periodic delivery-pipeline job:
// every cycle it finds accepted (approved-or-later) candidates with no
// verified nas-primary replica row, mirrors their bytes into the NAS
// archive, verifies the copy by digest, and records the placement in the
// artifact_replica_location ledger. The job only registers when the archive
// store is configured (HIVECREW_ARTIFACT_ARCHIVE_ROOT).
func ArtifactArchiveReconcilerJob(
	pool *pgxpool.Pool,
	queries *db.Queries,
	store *companyops.NASArtifactArchiveStore,
	source companyops.ArtifactArchiveSource,
) JobSpec {
	return JobSpec{
		Name:              JobNameArtifactArchiveReconciler,
		Cadence:           15 * time.Minute,
		ScheduleDelay:     1 * time.Minute,
		CatchUpMode:       CatchUpLatestOnly,
		CatchUpWindow:     24 * time.Hour,
		RunTimeout:        10 * time.Minute,
		StaleTimeout:      15 * time.Minute,
		HeartbeatInterval: 30 * time.Second,
		AllowStaleReentry: true,
		MaxAttempts:       2,
		RetryBackoff:      []time.Duration{1 * time.Minute},
		Scopes:            StaticScopes(ScopeGlobal),
		Handler:           makeArtifactArchiveReconcilerHandler(pool, queries, store, source),
	}
}

func makeArtifactArchiveReconcilerHandler(
	pool *pgxpool.Pool,
	queries *db.Queries,
	store *companyops.NASArtifactArchiveStore,
	source companyops.ArtifactArchiveSource,
) Handler {
	return func(ctx context.Context, in HandlerInput) (HandlerResult, error) {
		if store == nil {
			return HandlerResult{}, fmt.Errorf("artifact archive store is required")
		}
		if source == nil {
			return HandlerResult{}, fmt.Errorf("artifact archive source is required")
		}
		archiver := &companyops.ArtifactArchiver{Store: store}
		ledger := companyops.NewDurableArtifactArchiveLedger(pool)

		rows, err := pool.Query(ctx, `SELECT id FROM workspace ORDER BY created_at`)
		if err != nil {
			return HandlerResult{}, fmt.Errorf("list workspaces: %w", err)
		}
		var workspaceIDs []pgtype.UUID
		for rows.Next() {
			var wsID pgtype.UUID
			if err := rows.Scan(&wsID); err != nil {
				rows.Close()
				return HandlerResult{}, fmt.Errorf("scan workspace: %w", err)
			}
			workspaceIDs = append(workspaceIDs, wsID)
		}
		rows.Close()
		if rows.Err() != nil {
			return HandlerResult{}, fmt.Errorf("list workspaces: %w", rows.Err())
		}

		var archived, verified, skipped int64
		var firstErr error
		for _, wsID := range workspaceIDs {
			candidates, err := queries.ListArchivePendingArtifactCandidates(ctx, db.ListArchivePendingArtifactCandidatesParams{
				WorkspaceID: wsID,
				LimitRows:   artifactArchiveBatchLimit,
			})
			if err != nil {
				if firstErr == nil {
					firstErr = fmt.Errorf("list archive-pending candidates: %w", err)
				}
				continue
			}
			for _, row := range candidates {
				result, err := archiver.ArchiveCandidate(ctx, source, ledger, companyops.ArtifactArchiveCandidate{
					WorkspaceID:      util.UUIDToString(row.WorkspaceID),
					LineageID:        util.UUIDToString(row.LineageID),
					CandidateID:      util.UUIDToString(row.ID),
					Revision:         int(row.Revision),
					StorageKey:       row.StorageKey,
					Digest:           row.Digest,
					DurableObjectRef: row.DurableObjectRef,
					Filename:         row.Filename,
					ContentType:      row.ContentType,
					SizeBytes:        row.SizeBytes,
				})
				if err != nil {
					// One bad candidate never stops the sweep; the failed
					// ledger row plus the next cycle are the retry path.
					slog.Warn("artifact archive reconciler: candidate failed",
						"error", err, "candidate_id", util.UUIDToString(row.ID))
					if firstErr == nil {
						firstErr = err
					}
					continue
				}
				switch {
				case result.Verified:
					verified++
				case result.Skipped != "":
					skipped++
				default:
					archived++
				}
			}
		}
		return HandlerResult{
			RowsAffected: verified,
		}, firstErr
	}
}
