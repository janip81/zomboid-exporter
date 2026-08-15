package main

import (
	"context"
	"embed"
	"html/template"
	"log/slog"
	"net/http"
)

// webui.go -- read-only admin/stats dashboard, served by the SAME
// exporter binary and HTTP server that already exposes /metrics (see
// main.go) -- no sidecar container, no new transport: it reuses the
// exact *pgxpool.Pool the rest of the exporter already holds via
// pgConcrete. Per Jani's 2026-08-15 direction: "we want web UI for
// exporterLog anyway, maybe it can also show TWR addons in menu if
// its active" -- the /twr route is only registered when twrEnabled is
// true, mirroring every other TWR-conditional surface in this
// codebase (schema, dispatch pipelines, Helm mount).
//
// SCAFFOLD, not a finished admin panel: read-only, no auth. Do not
// expose this port publicly (no Ingress/HTTPRoute) until an auth story
// exists -- the intended access pattern for now is the same
// port-forward-only pattern already used for the pgweb debug
// deployment (kubectl port-forward), not a routed hostname. A mutating
// "Retry" action (re-queue a FAILED_FINAL job) is deliberately NOT
// included yet -- that needs auth first, since unlike the Lua debug
// menu's admin-access-level check, this HTTP server currently has no
// caller-identity concept at all.
//
//go:embed webui_templates/*.html
var webuiTemplatesFS embed.FS

var webuiTemplates = template.Must(template.ParseFS(webuiTemplatesFS, "webui_templates/*.html"))

type webuiPageData struct {
	Title      string
	ServerName string
	Active     string
	TWREnabled bool

	// dashboard
	Players          []webuiPlayerRow
	Events           []webuiEventRow
	TopKillers       []webuiLeaderboardRow
	MostTraveled     []webuiLeaderboardRow
	RoadWarriors     []webuiLeaderboardRow
	MostDeaths       []webuiLeaderboardRow
	LongestSurvivors []webuiLeaderboardRow

	// twr
	Campaigns      []webuiCampaignRow
	QuestInstances []webuiQuestInstanceRow
	StepInstances  []webuiStepInstanceRow
	Jobs           []webuiJobRow
}

func registerWebUIRoutes(mux *http.ServeMux, pg *pgStore, serverName string, twrEnabled bool) {
	if pg == nil {
		slog.Info("webui: no Postgres connection, dashboard routes not registered")
		return
	}

	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/" {
			http.NotFound(w, r)
			return
		}
		renderDashboard(w, r, pg, serverName, twrEnabled)
	})

	if twrEnabled {
		mux.HandleFunc("/twr", func(w http.ResponseWriter, r *http.Request) {
			renderTWR(w, r, pg, serverName)
		})
	}

	slog.Info("webui: dashboard routes registered", "twr", twrEnabled)
}

func renderDashboard(w http.ResponseWriter, r *http.Request, pg *pgStore, serverName string, twrEnabled bool) {
	ctx := r.Context()
	data := webuiPageData{
		Title:      "Dashboard",
		ServerName: serverName,
		Active:     "dashboard",
		TWREnabled: twrEnabled,
	}

	players, err := webuiFetchPlayers(ctx, pg)
	if err != nil {
		slog.Warn("webui: fetch players failed", "err", err)
	}
	data.Players = players

	events, err := webuiFetchRecentEvents(ctx, pg)
	if err != nil {
		slog.Warn("webui: fetch events failed", "err", err)
	}
	data.Events = events

	data.TopKillers, err = webuiFetchLeaderboard(ctx, pg, webuiQueryTopKillers)
	if err != nil {
		slog.Warn("webui: fetch top killers failed", "err", err)
	}
	data.MostTraveled, err = webuiFetchLeaderboard(ctx, pg, webuiQueryMostTraveled)
	if err != nil {
		slog.Warn("webui: fetch most traveled failed", "err", err)
	}
	data.RoadWarriors, err = webuiFetchLeaderboard(ctx, pg, webuiQueryRoadWarriors)
	if err != nil {
		slog.Warn("webui: fetch road warriors failed", "err", err)
	}
	data.MostDeaths, err = webuiFetchLeaderboard(ctx, pg, webuiQueryMostDeaths)
	if err != nil {
		slog.Warn("webui: fetch most deaths failed", "err", err)
	}
	data.LongestSurvivors, err = webuiFetchLeaderboard(ctx, pg, webuiQueryLongestSurvivors)
	if err != nil {
		slog.Warn("webui: fetch longest survivors failed", "err", err)
	}

	webuiRender(w, "dashboard", data)
}

func renderTWR(w http.ResponseWriter, r *http.Request, pg *pgStore, serverName string) {
	ctx := r.Context()
	data := webuiPageData{
		Title:      "TWR",
		ServerName: serverName,
		Active:     "twr",
		TWREnabled: true,
	}

	var err error
	data.Campaigns, err = webuiFetchCampaigns(ctx, pg)
	if err != nil {
		slog.Warn("webui: fetch campaigns failed", "err", err)
	}
	data.QuestInstances, err = webuiFetchQuestInstances(ctx, pg)
	if err != nil {
		slog.Warn("webui: fetch quest instances failed", "err", err)
	}
	data.StepInstances, err = webuiFetchStepInstances(ctx, pg)
	if err != nil {
		slog.Warn("webui: fetch step instances failed", "err", err)
	}
	data.Jobs, err = webuiFetchJobs(ctx, pg)
	if err != nil {
		slog.Warn("webui: fetch jobs failed", "err", err)
	}

	webuiRender(w, "twr", data)
}

func webuiRender(w http.ResponseWriter, name string, data webuiPageData) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := webuiTemplates.ExecuteTemplate(w, name, data); err != nil {
		slog.Error("webui: template execution failed", "template", name, "err", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
	}
}

// -- data access --------------------------------------------------
//
// Deliberately plain, hand-written queries (no ORM/query builder) --
// matches this codebase's existing style throughout store_postgres.go
// and questengine.go. Each Fetch* function is independent and
// tolerant of the OTHER queries failing (renderDashboard/renderTWR
// log-and-continue per query, same "partial page beats no page"
// posture as everything else in this file).

// Leaderboard queries all return the same (label, formatted value)
// shape, so they share one fetch helper -- unlike the TWR row types
// below (each genuinely different columns), these five are
// identical in shape immediately, not a hypothetical future need.
//
// Field names (zombieKills, km) are grounded in the actual Lua
// trackers that emit them (lua-mod/src/ExporterLog/Trackers/Kills.lua,
// Vehicles.lua, Movement.lua) and in discord-bot/milestones.go's own
// use of the same fields -- not guessed.
type webuiLeaderboardRow struct {
	Label, Value string
}

func webuiFetchLeaderboard(ctx context.Context, pg *pgStore, query string) ([]webuiLeaderboardRow, error) {
	rows, err := pg.pool.Query(ctx, query)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []webuiLeaderboardRow
	for rows.Next() {
		var r webuiLeaderboardRow
		if err := rows.Scan(&r.Label, &r.Value); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// kill events carry zombieKills as an already-running total (per the
// Lua tracker), so MAX per player is correct here -- unlike
// movement_distance/driving_distance below, which emit incremental
// per-session deltas and need SUM instead.
const webuiQueryTopKillers = `
	SELECT COALESCE(p.last_username, e.steam_id), MAX((e.details->>'zombieKills')::int)::text
	FROM events e
	JOIN players p ON p.steam_id = e.steam_id
	WHERE e.event_type = 'kill'
	GROUP BY COALESCE(p.last_username, e.steam_id)
	ORDER BY MAX((e.details->>'zombieKills')::int) DESC
	LIMIT 10
`

const webuiQueryMostTraveled = `
	SELECT COALESCE(p.last_username, e.steam_id), ROUND(SUM((e.details->>'km')::numeric), 1)::text || ' km'
	FROM events e
	JOIN players p ON p.steam_id = e.steam_id
	WHERE e.event_type = 'movement_distance'
	GROUP BY COALESCE(p.last_username, e.steam_id)
	ORDER BY SUM((e.details->>'km')::numeric) DESC
	LIMIT 10
`

const webuiQueryRoadWarriors = `
	SELECT COALESCE(p.last_username, e.steam_id), ROUND(SUM((e.details->>'km')::numeric), 1)::text || ' km'
	FROM events e
	JOIN players p ON p.steam_id = e.steam_id
	WHERE e.event_type = 'driving_distance'
	GROUP BY COALESCE(p.last_username, e.steam_id)
	ORDER BY SUM((e.details->>'km')::numeric) DESC
	LIMIT 10
`

const webuiQueryMostDeaths = `
	SELECT COALESCE(p.last_username, c.steam_id), COUNT(*)::text
	FROM characters c
	JOIN players p ON p.steam_id = c.steam_id
	WHERE c.died_at IS NOT NULL
	GROUP BY COALESCE(p.last_username, c.steam_id)
	ORDER BY COUNT(*) DESC
	LIMIT 10
`

// Per-life ranking (not grouped by player) -- a player's best single
// life, not a cumulative total across characters.
const webuiQueryLongestSurvivors = `
	SELECT COALESCE(p.last_username, c.steam_id) || ' (character #' || c.character_number || ')',
	       ROUND(c.hours_survived_at_death::numeric, 1)::text || ' hours'
	FROM characters c
	JOIN players p ON p.steam_id = c.steam_id
	WHERE c.hours_survived_at_death IS NOT NULL
	ORDER BY c.hours_survived_at_death DESC
	LIMIT 10
`

type webuiPlayerRow struct {
	LastUsername, FirstSeen, LastSeen string
}

func webuiFetchPlayers(ctx context.Context, pg *pgStore) ([]webuiPlayerRow, error) {
	rows, err := pg.pool.Query(ctx, `
		SELECT last_username, first_seen::text, last_seen::text
		FROM players
		ORDER BY last_seen DESC
		LIMIT 25
	`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []webuiPlayerRow
	for rows.Next() {
		var p webuiPlayerRow
		if err := rows.Scan(&p.LastUsername, &p.FirstSeen, &p.LastSeen); err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

type webuiEventRow struct {
	EventType, SteamID, OccurredAt, Details string
}

func webuiFetchRecentEvents(ctx context.Context, pg *pgStore) ([]webuiEventRow, error) {
	rows, err := pg.pool.Query(ctx, `
		SELECT event_type, COALESCE(steam_id, ''), occurred_at::text, details::text
		FROM events
		ORDER BY id DESC
		LIMIT 25
	`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []webuiEventRow
	for rows.Next() {
		var e webuiEventRow
		if err := rows.Scan(&e.EventType, &e.SteamID, &e.OccurredAt, &e.Details); err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

type webuiCampaignRow struct {
	ID, CampaignKey, State, StartedAt string
	StateBadgeClass                   string
}

func webuiFetchCampaigns(ctx context.Context, pg *pgStore) ([]webuiCampaignRow, error) {
	rows, err := pg.pool.Query(ctx, `
		SELECT id::text, campaign_key, state, COALESCE(started_at::text, '')
		FROM twr_campaigns
		ORDER BY id DESC
		LIMIT 25
	`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []webuiCampaignRow
	for rows.Next() {
		var c webuiCampaignRow
		if err := rows.Scan(&c.ID, &c.CampaignKey, &c.State, &c.StartedAt); err != nil {
			return nil, err
		}
		c.StateBadgeClass = webuiStateBadgeClass(c.State)
		out = append(out, c)
	}
	return out, rows.Err()
}

type webuiQuestInstanceRow struct {
	ID, QuestKey, State, StartedAt, CompletedAt string
	StateBadgeClass                             string
}

func webuiFetchQuestInstances(ctx context.Context, pg *pgStore) ([]webuiQuestInstanceRow, error) {
	rows, err := pg.pool.Query(ctx, `
		SELECT qi.id::text, qd.quest_key, qi.state, COALESCE(qi.started_at::text, ''), COALESCE(qi.completed_at::text, '')
		FROM twr_quest_instances qi
		JOIN twr_quest_definitions qd ON qd.id = qi.quest_definition_id
		ORDER BY qi.id DESC
		LIMIT 25
	`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []webuiQuestInstanceRow
	for rows.Next() {
		var q webuiQuestInstanceRow
		if err := rows.Scan(&q.ID, &q.QuestKey, &q.State, &q.StartedAt, &q.CompletedAt); err != nil {
			return nil, err
		}
		q.StateBadgeClass = webuiStateBadgeClass(q.State)
		out = append(out, q)
	}
	return out, rows.Err()
}

type webuiStepInstanceRow struct {
	ID, QuestInstanceID, StepKey, State, TriggeredAt, CompletedAt string
	StateBadgeClass                                               string
}

func webuiFetchStepInstances(ctx context.Context, pg *pgStore) ([]webuiStepInstanceRow, error) {
	rows, err := pg.pool.Query(ctx, `
		SELECT si.id::text, si.quest_instance_id::text, sd.step_key, si.state,
		       COALESCE(si.triggered_at::text, ''), COALESCE(si.completed_at::text, '')
		FROM twr_step_instances si
		JOIN twr_step_definitions sd ON sd.id = si.step_definition_id
		ORDER BY si.quest_instance_id DESC, si.id ASC
		LIMIT 50
	`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []webuiStepInstanceRow
	for rows.Next() {
		var s webuiStepInstanceRow
		if err := rows.Scan(&s.ID, &s.QuestInstanceID, &s.StepKey, &s.State, &s.TriggeredAt, &s.CompletedAt); err != nil {
			return nil, err
		}
		s.StateBadgeClass = webuiStateBadgeClass(s.State)
		out = append(out, s)
	}
	return out, rows.Err()
}

type webuiJobRow struct {
	ID, ActionType, Status, AttemptCount           string
	CreatedAt, DispatchedAt, AcceptedAt, AppliedAt string
	StatusBadgeClass                               string
}

func webuiFetchJobs(ctx context.Context, pg *pgStore) ([]webuiJobRow, error) {
	rows, err := pg.pool.Query(ctx, `
		SELECT id::text, action_type, status, attempt_count::text,
		       created_at::text, COALESCE(dispatched_at::text, ''),
		       COALESCE(accepted_at::text, ''), COALESCE(applied_at::text, '')
		FROM twr_jobs
		ORDER BY id DESC
		LIMIT 25
	`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []webuiJobRow
	for rows.Next() {
		var j webuiJobRow
		if err := rows.Scan(&j.ID, &j.ActionType, &j.Status, &j.AttemptCount, &j.CreatedAt, &j.DispatchedAt, &j.AcceptedAt, &j.AppliedAt); err != nil {
			return nil, err
		}
		j.StatusBadgeClass = webuiStatusBadgeClass(j.Status)
		out = append(out, j)
	}
	return out, rows.Err()
}

// webuiStateBadgeClass/webuiStatusBadgeClass are deliberately separate
// (not a shared lookup table) -- twr_jobs.status and the various
// state columns (twr_campaigns.state, twr_quest_instances.state,
// twr_step_instances.state) use different vocabularies (schema_twr_
// postgres.sql PART A/B/D), and conflating them into one switch would
// make it easy to silently misclassify a value that happens to share
// a name across tables but means something different.
func webuiStateBadgeClass(state string) string {
	switch state {
	case "completed", "active":
		return "badge-ok"
	case "failed", "cancelled":
		return "badge-error"
	case "locked", "pending", "inactive":
		return "badge-neutral"
	default:
		return "badge-pending"
	}
}

func webuiStatusBadgeClass(status string) string {
	switch status {
	case "APPLIED":
		return "badge-ok"
	case "FAILED_FINAL", "CANCELLED":
		return "badge-error"
	case "QUEUED":
		return "badge-neutral"
	default:
		return "badge-pending"
	}
}
