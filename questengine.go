package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"time"
)

// questengine.go is the "Gate 1" TWR quest engine -- see
// zomboid-exporter-ideas/antagonist/quest-db/ for the full design docs
// this is deliberately trimmed from, and PLAN.md ("TWR Quest Engine
// Gate 1") for the scoping rationale. Postgres-only, on purpose, not
// mirrored to store_sqlite.go: the relational joins across
// quest_instances/step_instances/step_definitions/step_conditions are
// substantial to duplicate for a feature this project only ever runs
// against the always-Postgres-backed zomboid/zomboid-test databases
// (SQLite is documented elsewhere as a zero-dependency fallback for
// people who don't already run Postgres -- not what's deployed here).
//
// State machine (schema_postgres.sql has the authoritative column
// comments):
//
//	twr_step_instances: locked -> armed -> triggered -> resolving -> completed
//	twr_jobs:            QUEUED -> DISPATCHED -> WAITING_WORLD -> APPLYING -> APPLIED
//
// This file owns everything upstream of job dispatch (signal
// production, condition evaluation, job creation) and the
// reconciliation that closes the loop once TWR.Emit.jobResult's
// existing outbound pipeline (twrlog.go -> handleTWRJobResult) proves a
// job actually succeeded. It does NOT own getting a QUEUED job to Lua
// -- that's Phase 3/4 of the plan, blocked on the transport research
// question in antagonist/tests/quest-job-dispatch-transport-research.md.

// runQuestEnginePipeline drives the quest engine for the lifetime of
// ctx. A no-op when pg is nil, same reasoning as every other pipeline
// in this codebase (a DB outage/absence shouldn't take down anything
// else the exporter does).
func runQuestEnginePipeline(ctx context.Context, pg *pgStore) {
	if pg == nil {
		return
	}

	// Startup reconciliation once immediately, before the first tick --
	// covers a job that reached APPLIED while this pipeline was down
	// (JOB-A..F's restart-safety requirement, pending-job-durability.md).
	if err := qeReconcileAppliedJobs(ctx, pg); err != nil {
		slog.Warn("quest engine startup reconciliation failed", "err", err)
	}

	ticker := time.NewTicker(3 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			qeTick(ctx, pg)
		}
	}
}

func qeTick(ctx context.Context, pg *pgStore) {
	if err := qePollSleepSignals(ctx, pg); err != nil {
		slog.Warn("qePollSleepSignals failed", "err", err)
	}
	if err := qePollMediaPlaybackSignals(ctx, pg); err != nil {
		slog.Warn("qePollMediaPlaybackSignals failed", "err", err)
	}
	if err := qeArmCampaignActivationSteps(ctx, pg); err != nil {
		slog.Warn("qeArmCampaignActivationSteps failed", "err", err)
	}
	if err := qeEvaluateArmedSteps(ctx, pg); err != nil {
		slog.Warn("qeEvaluateArmedSteps failed", "err", err)
	}
	if err := qeReconcileAppliedJobs(ctx, pg); err != nil {
		slog.Warn("qeReconcileAppliedJobs failed", "err", err)
	}
}

// qeGetCursor/qeSetCursor track "how far have I read" per named signal
// source in twr_signal_cursors -- same purpose as processed_files, just
// keyed by a small fixed set of source names instead of a file path,
// since these poll DB tables rather than log files. The upsert-with-
// RETURNING trick in qeGetCursor gets-or-creates the row in one round
// trip: on conflict it "updates" source to its own existing value (a
// no-op) purely so RETURNING has something to return.
func qeGetCursor(ctx context.Context, pg *pgStore, source string) (int64, error) {
	var lastID int64
	err := pg.pool.QueryRow(ctx, `
		INSERT INTO twr_signal_cursors (source, last_id) VALUES ($1, 0)
		ON CONFLICT (source) DO UPDATE SET source = EXCLUDED.source
		RETURNING last_id
	`, source).Scan(&lastID)
	return lastID, err
}

func qeSetCursor(ctx context.Context, pg *pgStore, source string, lastID int64) error {
	_, err := pg.pool.Exec(ctx, `
		INSERT INTO twr_signal_cursors (source, last_id) VALUES ($1, $2)
		ON CONFLICT (source) DO UPDATE SET last_id = EXCLUDED.last_id
	`, source, lastID)
	return err
}

// qePollSleepSignals translates new `events` rows with event_type='sleep'
// (written by ExporterLog's Sleeping.lua tracker into the same generic
// events table every other ExporterLog stat uses) into normalized
// twr_signals rows an ARMED step with trigger_type='sleep' can match
// against. This is the exact translation quest-database-design.md gives
// as its worked example: "ExporterLog raw sleep event -> backend
// normalizes -> twr_signal(type='sleep', payload={hours,location,x,y,z})".
// `details` already contains x/y/z (Sleeping.lua's own event fields),
// so no extra normalization is needed beyond copying it through as the
// signal payload.
func qePollSleepSignals(ctx context.Context, pg *pgStore) error {
	lastID, err := qeGetCursor(ctx, pg, "sleep_events")
	if err != nil {
		return err
	}

	rows, err := pg.pool.Query(ctx, `
		SELECT id, steam_id, occurred_at, details
		FROM events
		WHERE event_type = 'sleep' AND id > $1
		ORDER BY id
	`, lastID)
	if err != nil {
		return err
	}
	type sleepRow struct {
		id         int64
		steamID    *string
		occurredAt time.Time
		details    []byte
	}
	var toProcess []sleepRow
	for rows.Next() {
		var r sleepRow
		if err := rows.Scan(&r.id, &r.steamID, &r.occurredAt, &r.details); err != nil {
			rows.Close()
			return err
		}
		toProcess = append(toProcess, r)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return err
	}
	rows.Close()

	for _, r := range toProcess {
		dedupeKey := fmt.Sprintf("sleep-event-%d", r.id)
		if _, err := pg.pool.Exec(ctx, `
			INSERT INTO twr_signals (signal_type, occurred_at, steam_id, source_type, source_ref, dedupe_key, payload)
			VALUES ('sleep', $1, $2, 'exporter_event', $3, $4, $5)
			ON CONFLICT (dedupe_key) DO NOTHING
		`, r.occurredAt, r.steamID, fmt.Sprintf("%d", r.id), dedupeKey, r.details); err != nil {
			return err
		}
		lastID = r.id
	}

	if len(toProcess) > 0 {
		return qeSetCursor(ctx, pg, "sleep_events", lastID)
	}
	return nil
}

// qePollMediaPlaybackSignals translates new twr_job_attempts rows with
// action_type='recorded_media_viewed' (written by TWR.Mechanics.
// RecordedMedia.pollDeviceMedia -- confirmed live 2026-08-13/14, see
// server/TWR/Mechanics/RecordedMedia.lua) into media_playback_completed
// twr_signals rows. artifact_key on twr_job_attempts holds the
// contentId (added 2026-08-14 alongside steam_id specifically to
// support this -- see schema_postgres.sql's comment on that column).
func qePollMediaPlaybackSignals(ctx context.Context, pg *pgStore) error {
	lastID, err := qeGetCursor(ctx, pg, "media_playback_attempts")
	if err != nil {
		return err
	}

	rows, err := pg.pool.Query(ctx, `
		SELECT id, steam_id, occurred_at, artifact_key
		FROM twr_job_attempts
		WHERE action_type = 'recorded_media_viewed' AND result = 'applied' AND id > $1
		ORDER BY id
	`, lastID)
	if err != nil {
		return err
	}
	type mediaRow struct {
		id          int64
		steamID     *string
		occurredAt  time.Time
		artifactKey *string
	}
	var toProcess []mediaRow
	for rows.Next() {
		var r mediaRow
		if err := rows.Scan(&r.id, &r.steamID, &r.occurredAt, &r.artifactKey); err != nil {
			rows.Close()
			return err
		}
		toProcess = append(toProcess, r)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return err
	}
	rows.Close()

	for _, r := range toProcess {
		if r.artifactKey != nil && *r.artifactKey != "" {
			payload, err := json.Marshal(map[string]any{"contentId": *r.artifactKey})
			if err != nil {
				return err
			}
			dedupeKey := fmt.Sprintf("media-attempt-%d", r.id)
			if _, err := pg.pool.Exec(ctx, `
				INSERT INTO twr_signals (signal_type, occurred_at, steam_id, source_type, source_ref, dedupe_key, payload)
				VALUES ('media_playback_completed', $1, $2, 'twr_event', $3, $4, $5)
				ON CONFLICT (dedupe_key) DO NOTHING
			`, r.occurredAt, r.steamID, fmt.Sprintf("%d", r.id), dedupeKey, payload); err != nil {
				return err
			}
		}
		lastID = r.id
	}

	if len(toProcess) > 0 {
		return qeSetCursor(ctx, pg, "media_playback_attempts", lastID)
	}
	return nil
}

// qeArmCampaignActivationSteps immediately triggers any step whose
// trigger_type is 'campaign_activation'. There's no real external
// signal to wait for -- the step's own quest_instance already being
// 'active' with this step still 'locked' (i.e. it's the quest's first
// step) IS the trigger. This is the "Trigger: test/admin activation
// until the real DB dispatcher exists" case the KVLS fixture doc
// describes for its own Step 00.
func qeArmCampaignActivationSteps(ctx context.Context, pg *pgStore) error {
	rows, err := pg.pool.Query(ctx, `
		SELECT si.id, sd.id
		FROM twr_step_instances si
		JOIN twr_step_definitions sd ON sd.id = si.step_definition_id
		JOIN twr_quest_instances qi ON qi.id = si.quest_instance_id
		WHERE si.state = 'locked' AND sd.trigger_type = 'campaign_activation' AND qi.state = 'active'
	`)
	if err != nil {
		return err
	}
	type row struct{ stepInstanceID, stepDefinitionID int64 }
	var toArm []row
	for rows.Next() {
		var r row
		if err := rows.Scan(&r.stepInstanceID, &r.stepDefinitionID); err != nil {
			rows.Close()
			return err
		}
		toArm = append(toArm, r)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return err
	}
	rows.Close()

	for _, r := range toArm {
		if _, err := pg.pool.Exec(ctx, `UPDATE twr_step_instances SET state='armed', armed_at=now() WHERE id = $1 AND state = 'locked'`, r.stepInstanceID); err != nil {
			return err
		}
		if err := qeTriggerStep(ctx, pg, r.stepInstanceID, r.stepDefinitionID, nil); err != nil {
			return err
		}
	}
	return nil
}

// qeEvaluateArmedSteps consumes new twr_signals rows (via the
// "evaluator" cursor) and, for each, finds ARMED step_instances whose
// step_definition.trigger_type matches signal_type and whose
// twr_step_conditions ALL pass (AND-only -- see schema_postgres.sql
// PART A's comment on twr_step_conditions for the v1 scoping this
// mirrors). A matching step is triggered.
func qeEvaluateArmedSteps(ctx context.Context, pg *pgStore) error {
	lastID, err := qeGetCursor(ctx, pg, "evaluator")
	if err != nil {
		return err
	}

	rows, err := pg.pool.Query(ctx, `
		SELECT id, signal_type, payload
		FROM twr_signals
		WHERE id > $1
		ORDER BY id
	`, lastID)
	if err != nil {
		return err
	}
	type sigRow struct {
		id         int64
		signalType string
		payload    []byte
	}
	var signals []sigRow
	for rows.Next() {
		var r sigRow
		if err := rows.Scan(&r.id, &r.signalType, &r.payload); err != nil {
			rows.Close()
			return err
		}
		signals = append(signals, r)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return err
	}
	rows.Close()

	for _, sig := range signals {
		if err := qeEvaluateOneSignal(ctx, pg, sig.id, sig.signalType, sig.payload); err != nil {
			return err
		}
		lastID = sig.id
	}

	if len(signals) > 0 {
		return qeSetCursor(ctx, pg, "evaluator", lastID)
	}
	return nil
}

func qeEvaluateOneSignal(ctx context.Context, pg *pgStore, signalID int64, signalType string, payloadRaw []byte) error {
	var payload map[string]any
	if err := json.Unmarshal(payloadRaw, &payload); err != nil {
		payload = map[string]any{}
	}

	rows, err := pg.pool.Query(ctx, `
		SELECT si.id, sd.id
		FROM twr_step_instances si
		JOIN twr_step_definitions sd ON sd.id = si.step_definition_id
		WHERE si.state = 'armed' AND sd.trigger_type = $1
	`, signalType)
	if err != nil {
		return err
	}
	type armedStep struct{ stepInstanceID, stepDefinitionID int64 }
	var candidates []armedStep
	for rows.Next() {
		var c armedStep
		if err := rows.Scan(&c.stepInstanceID, &c.stepDefinitionID); err != nil {
			rows.Close()
			return err
		}
		candidates = append(candidates, c)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return err
	}
	rows.Close()

	for _, c := range candidates {
		ok, err := qeConditionsPass(ctx, pg, c.stepDefinitionID, payload)
		if err != nil {
			return err
		}
		if !ok {
			continue
		}
		sid := signalID
		if err := qeTriggerStep(ctx, pg, c.stepInstanceID, c.stepDefinitionID, &sid); err != nil {
			return err
		}
	}
	return nil
}

func qeConditionsPass(ctx context.Context, pg *pgStore, stepDefinitionID int64, payload map[string]any) (bool, error) {
	rows, err := pg.pool.Query(ctx, `
		SELECT condition_type, params, negate
		FROM twr_step_conditions
		WHERE step_definition_id = $1
		ORDER BY position
	`, stepDefinitionID)
	if err != nil {
		return false, err
	}
	defer rows.Close()

	for rows.Next() {
		var condType string
		var paramsRaw []byte
		var negate bool
		if err := rows.Scan(&condType, &paramsRaw, &negate); err != nil {
			return false, err
		}
		var params map[string]any
		if err := json.Unmarshal(paramsRaw, &params); err != nil {
			params = map[string]any{}
		}
		pass := qeEvaluateCondition(condType, params, payload)
		if negate {
			pass = !pass
		}
		if !pass {
			return false, nil
		}
	}
	return true, rows.Err()
}

// qeEvaluateCondition implements the two condition_types the KVLS
// fixture needs (content_identity_equals, within_location_ref) -- see
// dummy-key-vhs-location-sleep.md's "Required engine vocabulary"
// section. An unrecognized condition_type fails closed (never silently
// advances a step this build doesn't know how to gate).
//
// within_location_ref deliberately only checks 2D (x,y) distance, not
// z -- a documented Gate 1 simplification, not an oversight; revisit if
// a real quest needs floor-level precision.
func qeEvaluateCondition(condType string, params, payload map[string]any) bool {
	switch condType {
	case "content_identity_equals":
		want, _ := params["content_id"].(string)
		got, _ := payload["contentId"].(string)
		return want != "" && want == got
	case "within_location_ref":
		px, pxOK := params["x"].(float64)
		py, pyOK := params["y"].(float64)
		radius, rOK := params["radius"].(float64)
		gx, gxOK := payload["x"].(float64)
		gy, gyOK := payload["y"].(float64)
		if !pxOK || !pyOK || !rOK || !gxOK || !gyOK {
			return false
		}
		dx := px - gx
		dy := py - gy
		return dx*dx+dy*dy <= radius*radius
	default:
		return false
	}
}

// qeTriggerStep marks an armed step_instance TRIGGERED and creates one
// QUEUED twr_jobs row per twr_step_actions row on its definition
// (params snapshotted at trigger time -- schema_postgres.sql PART A's
// comment on twr_step_actions explains why: a later edit to the
// definition must never silently change an already-created job's
// behavior). A step with no actions at all completes immediately
// (there's nothing to wait on). signalID is nil for
// campaign_activation-triggered steps, which have no real signal row.
func qeTriggerStep(ctx context.Context, pg *pgStore, stepInstanceID, stepDefinitionID int64, signalID *int64) error {
	tx, err := pg.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx) //nolint:errcheck -- no-op after a successful Commit

	// Guard against double-trigger: only proceed if still armed. A
	// concurrent tick (or a step matched by two different signals in
	// the same batch) must never create a second batch of jobs for the
	// same step_instance -- see schema_postgres.sql's twr_step_instances
	// comment.
	tag, err := tx.Exec(ctx, `
		UPDATE twr_step_instances
		SET state = 'triggered', triggered_at = now(), trigger_signal_id = $2
		WHERE id = $1 AND state = 'armed'
	`, stepInstanceID, signalID)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return nil // already triggered by a concurrent tick -- fine, not an error
	}

	rows, err := tx.Query(ctx, `
		SELECT id, action_type, params
		FROM twr_step_actions
		WHERE step_definition_id = $1
		ORDER BY position
	`, stepDefinitionID)
	if err != nil {
		return err
	}
	type actionRow struct {
		id         int64
		actionType string
		params     []byte
	}
	var actions []actionRow
	for rows.Next() {
		var a actionRow
		if err := rows.Scan(&a.id, &a.actionType, &a.params); err != nil {
			rows.Close()
			return err
		}
		actions = append(actions, a)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return err
	}
	rows.Close()

	if len(actions) == 0 {
		if _, err := tx.Exec(ctx, `
			UPDATE twr_step_instances
			SET state = 'completed', completed_at = now(), completion_count = completion_count + 1
			WHERE id = $1
		`, stepInstanceID); err != nil {
			return err
		}
		if err := tx.Commit(ctx); err != nil {
			return err
		}
		return qeAdvanceAfterCompletion(ctx, pg, stepInstanceID)
	}

	if _, err := tx.Exec(ctx, `UPDATE twr_step_instances SET state = 'resolving' WHERE id = $1`, stepInstanceID); err != nil {
		return err
	}

	var campaignID, questInstanceID int64
	if err := tx.QueryRow(ctx, `
		SELECT qi.campaign_id, si.quest_instance_id
		FROM twr_step_instances si
		JOIN twr_quest_instances qi ON qi.id = si.quest_instance_id
		WHERE si.id = $1
	`, stepInstanceID).Scan(&campaignID, &questInstanceID); err != nil {
		return err
	}

	for _, a := range actions {
		// completion_count isn't known yet at INSERT time for a
		// recurring step's Nth firing (Gate 1 only implements
		// repeat_policy='once', so this is always attempt 1) --
		// idempotency_key stays stable per (step_instance, action)
		// pair, matching "once" semantics.
		idempotencyKey := fmt.Sprintf("step-instance-%d-action-%d-1", stepInstanceID, a.id)
		if _, err := tx.Exec(ctx, `
			INSERT INTO twr_jobs (campaign_id, quest_instance_id, step_instance_id, action_definition_id, action_type, action_params, status, idempotency_key)
			VALUES ($1, $2, $3, $4, $5, $6, 'QUEUED', $7)
			ON CONFLICT (idempotency_key) DO NOTHING
		`, campaignID, questInstanceID, stepInstanceID, a.id, a.actionType, a.params, idempotencyKey); err != nil {
			return err
		}
	}

	return tx.Commit(ctx)
}

// qeAdvanceAfterCompletion arms whatever twr_quest_edges says comes
// after a just-completed step (outcome_key='success' only -- v1
// scoping, see schema_postgres.sql's twr_quest_edges comment), or marks
// the whole quest_instance completed if this was the last step (no
// outgoing edge).
func qeAdvanceAfterCompletion(ctx context.Context, pg *pgStore, stepInstanceID int64) error {
	var stepKey string
	var questDefinitionID, questInstanceID int64
	if err := pg.pool.QueryRow(ctx, `
		SELECT sd.step_key, sd.quest_definition_id, si.quest_instance_id
		FROM twr_step_instances si
		JOIN twr_step_definitions sd ON sd.id = si.step_definition_id
		WHERE si.id = $1
	`, stepInstanceID).Scan(&stepKey, &questDefinitionID, &questInstanceID); err != nil {
		return err
	}

	rows, err := pg.pool.Query(ctx, `
		SELECT to_step_key
		FROM twr_quest_edges
		WHERE quest_definition_id = $1 AND from_step_key = $2 AND outcome_key = 'success'
		ORDER BY position
	`, questDefinitionID, stepKey)
	if err != nil {
		return err
	}
	var nextKeys []string
	for rows.Next() {
		var k string
		if err := rows.Scan(&k); err != nil {
			rows.Close()
			return err
		}
		nextKeys = append(nextKeys, k)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return err
	}
	rows.Close()

	if len(nextKeys) == 0 {
		_, err := pg.pool.Exec(ctx, `
			UPDATE twr_quest_instances SET state = 'completed', completed_at = now()
			WHERE id = $1 AND state != 'completed'
		`, questInstanceID)
		return err
	}

	for _, key := range nextKeys {
		if _, err := pg.pool.Exec(ctx, `
			UPDATE twr_step_instances si
			SET state = 'armed', armed_at = now()
			FROM twr_step_definitions sd
			WHERE si.step_definition_id = sd.id
			  AND si.quest_instance_id = $1
			  AND sd.step_key = $2
			  AND si.state = 'locked'
		`, questInstanceID, key); err != nil {
			return err
		}
	}
	return nil
}

// qeReconcileAppliedJobs flips twr_jobs.status to APPLIED once a
// matching twr_job_attempts row proves the world mutation actually
// succeeded (job_id there is the plain TEXT string form of
// twr_jobs.id -- see schema_postgres.sql PART D's comment for why no
// FK migration was needed on the already-live twr_job_attempts table).
// Runs at startup (covers a job that reached APPLIED while this
// pipeline was down) and every tick thereafter -- this IS the
// restart-reconciliation logic pending-job-durability.md's JOB-C/JOB-E
// require, not just documentation of intent.
//
// Deliberately does not yet handle retryable_error/final_error attempts
// (no RETRYABLE/FAILED_FINAL transition here) -- Gate 1's acceptance
// test only exercises the happy path; failure-branch handling is a
// fast-follow, not required to call Gate 1 done.
func qeReconcileAppliedJobs(ctx context.Context, pg *pgStore) error {
	rows, err := pg.pool.Query(ctx, `
		UPDATE twr_jobs j
		SET status = 'APPLIED', applied_at = now()
		FROM twr_job_attempts a
		WHERE a.job_id = j.id::text
		  AND a.result = 'applied'
		  AND j.status NOT IN ('APPLIED', 'CANCELLED')
		RETURNING j.step_instance_id
	`)
	if err != nil {
		return err
	}
	var stepInstanceIDs []int64
	for rows.Next() {
		var id *int64
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return err
		}
		if id != nil {
			stepInstanceIDs = append(stepInstanceIDs, *id)
		}
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return err
	}
	rows.Close()

	seen := make(map[int64]bool, len(stepInstanceIDs))
	for _, id := range stepInstanceIDs {
		if seen[id] {
			continue
		}
		seen[id] = true
		if err := qeMaybeCompleteStep(ctx, pg, id); err != nil {
			return err
		}
	}
	return nil
}

// qeMaybeCompleteStep marks a 'resolving' step_instance 'completed' once
// every one of its jobs has reached APPLIED (all_actions
// completion_policy -- the only policy Gate 1 implements, matching
// twr_step_definitions' documented default), then advances the quest
// graph via qeAdvanceAfterCompletion.
func qeMaybeCompleteStep(ctx context.Context, pg *pgStore, stepInstanceID int64) error {
	var state string
	var total, applied int
	err := pg.pool.QueryRow(ctx, `
		SELECT si.state,
		       (SELECT count(*) FROM twr_jobs WHERE step_instance_id = $1),
		       (SELECT count(*) FROM twr_jobs WHERE step_instance_id = $1 AND status = 'APPLIED')
		FROM twr_step_instances si WHERE si.id = $1
	`, stepInstanceID).Scan(&state, &total, &applied)
	if err != nil {
		return err
	}
	if state != "resolving" || total == 0 || applied < total {
		return nil
	}

	tag, err := pg.pool.Exec(ctx, `
		UPDATE twr_step_instances
		SET state = 'completed', completed_at = now(), completion_count = completion_count + 1
		WHERE id = $1 AND state = 'resolving'
	`, stepInstanceID)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return nil
	}
	return qeAdvanceAfterCompletion(ctx, pg, stepInstanceID)
}
