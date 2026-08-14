package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// questdispatch.go implements the Postgres -> Lua half of the quest
// engine's job transport -- see zomboid-exporter-ideas/antagonist/tests/
// quest-job-dispatch-transport-chatgpt-response.md for the full design
// this follows. Deliberately NOT modeled on PanelBridge's dual
// independently-persisted sequence-counter protocol (that project hit a
// real counter-drift bug); instead the exporter atomically publishes a
// manifest naming the exact dispatchable job files, and Postgres's own
// twr_jobs.id is the only identity that matters anywhere in this
// pipeline -- there is no second counter to drift.
//
// File layout under the scoped RW mount (helm-charts' zomboid-server
// chart, subPath: Lua/twr_dispatch -- see that chart's statefulset.yaml
// comment for why this is a narrow subPath mount, not full volume RW):
//
//	twr_dispatch/
//	    manifest.txt      -- revision=<n>\njob=<id>\njob=<id>...
//	    inbox/
//	        job-<id>.txt   -- {"jobId":"<id>","actionType":"...","actionParams":{...}}
//
// Delivery lifecycle (this file only owns steps 1-2 and 6-8; steps 3-5
// are the Lua-side poller, Phase 4):
//
//  1. twr_jobs.status = QUEUED (created by questengine.go's evaluator)
//  2. dispatcher claims it, writes inbox/job-<id>.txt + manifest.txt,
//     status -> DISPATCHED
//  3. Lua reads manifest, reads job payload, validates
//  4. Lua durably accepts (PendingActions/SGOS), emits an "accepted"
//     result over the existing TWR log channel
//  5. (Lua eventually applies the mutation, emits "applied" -- already
//     handled by qeReconcileAppliedJobs in questengine.go)
//  6. exporter's existing outbound pipeline (twrlog.go) sees "accepted",
//     flips status -> WAITING_WORLD, job drops out of the manifest
//  7. a DISPATCHED job with no "accepted" receipt within the lease
//     timeout is redispatched -- same job_id, same file, fresh
//     dispatched_at -- never a new logical job
//  8. "applied" already closes the loop via qeReconcileAppliedJobs
//
// A dispatch file existing does NOT mean the job is safely delivered --
// only a durable Lua-side "accepted" receipt does. This is why the
// manifest excludes anything already WAITING_WORLD/APPLIED/terminal:
// once Lua has it in SGOS, re-announcing it via the file queue would
// just be noise (and Lua's own PendingActions/idempotency handles
// replay if the same file gets read twice regardless -- TRANSPORT-B).

const (
	twrDispatchDir          = "/data/Lua/twr_dispatch"
	twrDispatchInboxDir     = twrDispatchDir + "/inbox"
	twrDispatchLeaseTimeout = 60 * time.Second
)

func runQuestDispatchPipeline(ctx context.Context, pg *pgStore) {
	if pg == nil {
		return
	}
	if err := os.MkdirAll(twrDispatchInboxDir, 0o775); err != nil {
		slog.Error("quest dispatch: cannot create inbox dir, dispatch disabled", "dir", twrDispatchInboxDir, "err", err)
		return
	}

	ticker := time.NewTicker(3 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if err := qdTick(ctx, pg); err != nil {
				slog.Warn("quest dispatch tick failed", "err", err)
			}
		}
	}
}

func qdTick(ctx context.Context, pg *pgStore) error {
	if err := qdRedispatchStaleLeases(ctx, pg); err != nil {
		return fmt.Errorf("redispatch stale leases: %w", err)
	}
	if err := qdDispatchQueuedJobs(ctx, pg); err != nil {
		return fmt.Errorf("dispatch queued jobs: %w", err)
	}
	if err := qdPublishManifest(ctx, pg); err != nil {
		return fmt.Errorf("publish manifest: %w", err)
	}
	return nil
}

type dispatchableJob struct {
	id           int64
	actionType   string
	actionParams []byte
}

// qdWriteJobFile atomically publishes one job's payload -- write to a
// temp file in the same directory (so the rename is same-filesystem and
// therefore atomic), fsync, close, rename into place. Lua must never be
// able to observe a partially-written file.
func qdWriteJobFile(job dispatchableJob) error {
	payload, err := json.Marshal(map[string]any{
		"jobId":        strconv.FormatInt(job.id, 10),
		"actionType":   job.actionType,
		"actionParams": json.RawMessage(job.actionParams),
	})
	if err != nil {
		return err
	}
	finalPath := filepath.Join(twrDispatchInboxDir, fmt.Sprintf("job-%d.txt", job.id))
	return qdWriteFileAtomic(finalPath, payload)
}

func qdWriteFileAtomic(finalPath string, data []byte) error {
	dir := filepath.Dir(finalPath)
	tmp, err := os.CreateTemp(dir, ".tmp-*")
	if err != nil {
		return err
	}
	tmpPath := tmp.Name()
	if _, err := tmp.Write(data); err != nil {
		tmp.Close() //nolint:errcheck
		os.Remove(tmpPath)
		return err
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close() //nolint:errcheck
		os.Remove(tmpPath)
		return err
	}
	if err := tmp.Close(); err != nil {
		os.Remove(tmpPath)
		return err
	}
	if err := os.Rename(tmpPath, finalPath); err != nil {
		os.Remove(tmpPath)
		return err
	}
	return nil
}

// qdDispatchQueuedJobs claims QUEUED jobs (FOR UPDATE SKIP LOCKED --
// harmless with a single exporter replica today, but correct if this
// ever runs more than one), writes each one's dispatch file, and flips
// status to DISPATCHED. Claiming (the UPDATE) happens BEFORE the file
// write commits durably to Postgres, matching the accepted crash
// window in the design doc: "DB claimed -> exporter dies before file
// write -> stale lease -> redispatch same job" is safe by construction,
// since qdRedispatchStaleLeases will pick it back up.
func qdDispatchQueuedJobs(ctx context.Context, pg *pgStore) error {
	tx, err := pg.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx) //nolint:errcheck

	rows, err := tx.Query(ctx, `
		SELECT id, action_type, action_params
		FROM twr_jobs
		WHERE status = 'QUEUED'
		ORDER BY id
		FOR UPDATE SKIP LOCKED
	`)
	if err != nil {
		return err
	}
	var jobs []dispatchableJob
	for rows.Next() {
		var j dispatchableJob
		if err := rows.Scan(&j.id, &j.actionType, &j.actionParams); err != nil {
			rows.Close()
			return err
		}
		jobs = append(jobs, j)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return err
	}
	rows.Close()

	if len(jobs) == 0 {
		return tx.Commit(ctx)
	}

	for _, j := range jobs {
		if _, err := tx.Exec(ctx, `
			UPDATE twr_jobs SET status = 'DISPATCHED', dispatched_at = now(), attempt_count = attempt_count + 1
			WHERE id = $1
		`, j.id); err != nil {
			return err
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return err
	}

	// File writes happen AFTER the DB commit on purpose: if a write
	// fails here, the job is already DISPATCHED in Postgres and
	// qdRedispatchStaleLeases will retry the file write on the next
	// stale-lease pass -- simpler than trying to roll back the DB
	// claim on a filesystem error.
	for _, j := range jobs {
		if err := qdWriteJobFile(j); err != nil {
			slog.Warn("quest dispatch: failed to write job file, will retry via stale-lease redispatch", "jobId", j.id, "err", err)
		}
	}
	return nil
}

// qdRedispatchStaleLeases re-publishes (same job_id, same file content,
// fresh dispatched_at) any job that's been DISPATCHED longer than
// twrDispatchLeaseTimeout with no "accepted" receipt -- covers every
// crash window between claiming a job and Lua durably accepting it
// (exporter dying before/after the file write, the file never reaching
// Lua, Lua crashing before it can accept). Never mints a new logical
// job -- same id, same idempotency identity throughout.
func qdRedispatchStaleLeases(ctx context.Context, pg *pgStore) error {
	rows, err := pg.pool.Query(ctx, `
		SELECT id, action_type, action_params
		FROM twr_jobs
		WHERE status = 'DISPATCHED' AND dispatched_at < now() - make_interval(secs => $1)
		ORDER BY id
	`, twrDispatchLeaseTimeout.Seconds())
	if err != nil {
		return err
	}
	var jobs []dispatchableJob
	for rows.Next() {
		var j dispatchableJob
		if err := rows.Scan(&j.id, &j.actionType, &j.actionParams); err != nil {
			rows.Close()
			return err
		}
		jobs = append(jobs, j)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return err
	}
	rows.Close()

	for _, j := range jobs {
		if err := qdWriteJobFile(j); err != nil {
			slog.Warn("quest dispatch: stale-lease redispatch file write failed, will retry next tick", "jobId", j.id, "err", err)
			continue
		}
		if _, err := pg.pool.Exec(ctx, `
			UPDATE twr_jobs SET dispatched_at = now(), attempt_count = attempt_count + 1
			WHERE id = $1 AND status = 'DISPATCHED'
		`, j.id); err != nil {
			return err
		}
		slog.Info("quest dispatch: redispatched stale lease", "jobId", j.id)
	}
	return nil
}

// qdPublishManifest atomically rewrites manifest.txt from the current
// DB truth of "what's DISPATCHED right now" -- rebuilt fresh every
// tick rather than incrementally maintained, so there's no separate
// producer-side state to drift from Postgres (the exact class of bug
// PanelBridge's dual-counter protocol hit). revision is a monotonically
// increasing integer purely so Lua can skip reparsing an unchanged
// manifest -- it is NOT a job identity, job_id always is.
func qdPublishManifest(ctx context.Context, pg *pgStore) error {
	rows, err := pg.pool.Query(ctx, `
		SELECT id FROM twr_jobs WHERE status = 'DISPATCHED' ORDER BY id
	`)
	if err != nil {
		return err
	}
	var ids []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return err
		}
		ids = append(ids, id)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return err
	}
	rows.Close()

	revision, err := qdNextManifestRevision(ctx, pg)
	if err != nil {
		return err
	}

	var b strings.Builder
	fmt.Fprintf(&b, "revision=%d\n", revision)
	for _, id := range ids {
		fmt.Fprintf(&b, "job=%d\n", id)
	}

	return qdWriteFileAtomic(filepath.Join(twrDispatchDir, "manifest.txt"), []byte(b.String()))
}

// qdNextManifestRevision reuses the twr_signal_cursors table (same
// get-or-create-and-increment purpose as qeGetCursor/qeSetCursor in
// questengine.go, just needing a plain increment instead of a
// watermark) rather than a dedicated new table for one integer counter.
func qdNextManifestRevision(ctx context.Context, pg *pgStore) (int64, error) {
	var revision int64
	err := pg.pool.QueryRow(ctx, `
		INSERT INTO twr_signal_cursors (source, last_id) VALUES ('dispatch_manifest_revision', 1)
		ON CONFLICT (source) DO UPDATE SET last_id = twr_signal_cursors.last_id + 1
		RETURNING last_id
	`).Scan(&revision)
	return revision, err
}
