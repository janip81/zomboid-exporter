package main

import (
	"context"
	_ "embed"
	"encoding/json"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/bwmarrin/discordgo"
	mqtt "github.com/eclipse/paho.mqtt.golang"
	"github.com/gorcon/rcon"
	"github.com/jackc/pgx/v5/pgxpool"
)

//go:embed schema_postgres.sql
var schemaSQL string

func main() {
	rconHost := flag.String("rcon-host", "", "Zomboid RCON host:port, e.g. zomboid-zomboid-server-rcon.zomboid.svc.cluster.local:27015")
	mqttBroker := flag.String("mqtt-broker", "", "MQTT broker URL, e.g. tcp://mosquitto.mqtt.svc.cluster.local:1883")
	mqttUsername := flag.String("mqtt-username", "", "MQTT broker username, if auth is required (password read from MQTT_PASSWORD env var)")
	mqttTopicPrefix := flag.String("mqtt-topic-prefix", "zomboid/those-who-remain", "MQTT topic prefix to subscribe to (subscribes to <prefix>/#)")
	discordChannelID := flag.String("discord-channel-id", "", "Discord channel ID to post live MQTT events into. If empty, events are only logged, not posted.")
	discordAppID := flag.String("discord-app-id", "", "Discord Application ID, required to register slash commands")
	discordGuildID := flag.String("discord-guild-id", "", "Discord server (guild) ID. Guild-scoped command registration is near-instant; global (leave empty) can take up to an hour to propagate to clients.")
	metricsURL := flag.String("metrics-url", "", "Exporter's Prometheus /metrics URL, e.g. http://zomboid-zomboid-server-metrics.zomboid.svc.cluster.local:9091/metrics (used for /serveruptime)")
	serverName := flag.String("server-name", "those-who-remain", "Server name, must match the exporter's --server-name (used to match the right series in /serveruptime)")
	dbHost := flag.String("db-host", "", "Postgres host for stats/leaderboard queries")
	dbPort := flag.Int("db-port", 5432, "Postgres port")
	dbName := flag.String("db-name", "zomboid", "Postgres database name")
	dbUser := flag.String("db-user", "zomboid", "Postgres user (password read from DB_PASSWORD env var)")
	bootstrapAdminIDs := flag.String("bootstrap-admin-ids", "", "Comma-separated Discord user IDs seeded as admin ONLY if discordbot_user_roles is completely empty (first boot / after a full data wipe). Ignored once any role exists -- day-to-day admin changes go through /block and /unblock, not this flag.")

	// Curator LLM feature -- see zomboid-exporter-ideas/curator-llm-integration.md,
	// curator-llm-provider.md, curator-llm-provider-db-config.md. Default
	// OFF and, when off, adds zero DB/network load (curator_llm.go's
	// newCuratorProviderPool never queries Postgres or starts its refresh
	// goroutine unless enabled) -- BOT-LLM-1.
	llmEnabled := flag.Bool("llm-enabled", false, "Enable the Curator LLM chat feature (/curator command + optional natural chat trigger). Provider list/priority/model comes from the discordbot_llm_providers table, not flags -- see curator-llm-provider-db-config.md. Actual paid-provider use additionally requires the LLM_ALLOW_PAID=true env var.")
	curatorNaturalChat := flag.Bool("curator-natural-chat", false, "React to the standalone word 'curator' in ordinary channel messages, not just /curator. REQUIRES the Discord Message Content privileged intent to be enabled for this bot in the Discord Developer Portal, or Discord silently sends empty message content and this never triggers.")
	curatorChannelID := flag.String("curator-channel-id", "", "Restrict the natural chat trigger to one channel ID. Empty means any channel the bot can see.")
	curatorAmbientReplyChance := flag.Float64("curator-ambient-reply-chance", 0.25, "Probability (0-1) of replying to an ORDINARY statement that merely mentions 'curator' (as opposed to a direct question/address, which always attempts a reply).")
	curatorUserCooldown := flag.Duration("curator-user-cooldown", 20*time.Second, "Minimum time between natural-chat Curator replies to the same Discord user.")
	curatorGlobalCooldown := flag.Duration("curator-global-cooldown", 5*time.Second, "Minimum time between ANY two natural-chat Curator replies, across all users -- caps worst-case LLM call rate during a burst of mentions.")
	flag.Parse()

	logger := slog.New(slog.NewTextHandler(os.Stdout, nil))
	slog.SetDefault(logger)

	// Secrets are read directly from the environment, never from flags --
	// flag values end up visible in the pod spec / process argv.
	discordToken := os.Getenv("DISCORD_TOKEN")
	if discordToken == "" {
		logger.Error("DISCORD_TOKEN env var is required")
		os.Exit(1)
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	// Created before the Discord session so botDeps (and therefore the
	// interaction handler's role checks) has it from the start -- role
	// checks, not just /stats-style commands, depend on this now.
	var dbPool *pgxpool.Pool
	if *dbHost != "" {
		dsn := fmt.Sprintf("postgres://%s:%s@%s:%d/%s", *dbUser, os.Getenv("DB_PASSWORD"), *dbHost, *dbPort, *dbName)
		pool, err := pgxpool.New(ctx, dsn)
		if err != nil {
			logger.Error("failed to create Postgres pool", "err", err)
		} else if err := pool.Ping(ctx); err != nil {
			logger.Error("failed to reach Postgres", "err", err)
			pool.Close()
		} else if _, err := pool.Exec(ctx, schemaSQL); err != nil {
			logger.Error("failed to apply schema, admin-role commands unavailable", "err", err)
			pool.Close()
		} else {
			dbPool = pool
			defer dbPool.Close()
			logger.Info("connected to Postgres, stats/leaderboard/admin-role commands enabled")
			if *bootstrapAdminIDs != "" {
				if err := bootstrapAdmins(ctx, dbPool, strings.Split(*bootstrapAdminIDs, ",")); err != nil {
					logger.Error("failed to bootstrap admins", "err", err)
				}
			}
			if err := seedMilestones(ctx, dbPool); err != nil {
				logger.Error("failed to seed milestones", "err", err)
			}
		}
	} else {
		logger.Warn("--db-host not set, stats/leaderboard/admin-role commands unavailable")
	}

	llmPool := newCuratorProviderPool(dbPool, *llmEnabled, llmAllowPaidFromEnv())
	if *llmEnabled {
		if dbPool == nil {
			logger.Warn("--llm-enabled=true but no Postgres connection -- Curator LLM replies unavailable, canned responses still work")
		} else {
			logger.Info("curator LLM enabled, loading provider config from discordbot_llm_providers", "allowPaid", llmAllowPaidFromEnv())
		}
		go llmPool.startRefreshLoop(ctx)
	} else {
		logger.Info("curator LLM disabled (--llm-enabled=false), canned Curator responses still available via /curator")
	}

	deps := botDeps{
		rconHost:     *rconHost,
		rconPassword: os.Getenv("RCON_PASSWORD"),
		metricsURL:   *metricsURL,
		serverName:   *serverName,
		db:           dbPool,
		llmPool:      llmPool,
	}

	discordSession, err := discordgo.New("Bot " + discordToken)
	if err != nil {
		logger.Error("failed to create Discord session", "err", err)
		os.Exit(1)
	}
	discordSession.AddHandler(newInteractionHandler(deps))
	if *curatorNaturalChat {
		// Message Content is a Discord PRIVILEGED intent -- must also be
		// turned on for this bot in the Developer Portal, or Discord
		// sends empty Content for ordinary guild messages and the
		// natural trigger silently never fires. See
		// curator-natural-trigger-and-identity.md's "Discord requirement."
		discordSession.Identify.Intents |= discordgo.IntentsMessageContent
		trigger := newCuratorNaturalTrigger(curatorNaturalTriggerConfig{
			Enabled:            true,
			ChannelID:          *curatorChannelID,
			AmbientReplyChance: *curatorAmbientReplyChance,
			UserCooldown:       *curatorUserCooldown,
			GlobalCooldown:     *curatorGlobalCooldown,
		}, deps)
		discordSession.AddHandler(trigger.handleMessageCreate)
		logger.Info("curator natural chat trigger enabled", "channelID", *curatorChannelID)
	}
	if err := discordSession.Open(); err != nil {
		logger.Error("failed to connect to Discord", "err", err)
		os.Exit(1)
	}
	defer discordSession.Close()
	logger.Info("connected to Discord", "user", discordSession.State.User.Username)

	if *discordAppID != "" {
		if _, err := discordSession.ApplicationCommandBulkOverwrite(*discordAppID, *discordGuildID, slashCommands); err != nil {
			logger.Error("failed to register slash commands", "err", err)
		} else {
			logger.Info("registered slash commands", "count", len(slashCommands), "guildID", *discordGuildID)
		}
		// If we now register guild-scoped, clear out any earlier global
		// registration so the same commands don't show up twice in the
		// picker once the (slow) global propagation eventually catches up.
		if *discordGuildID != "" {
			if _, err := discordSession.ApplicationCommandBulkOverwrite(*discordAppID, "", []*discordgo.ApplicationCommand{}); err != nil {
				logger.Error("failed to clear global slash commands", "err", err)
			}
		}
	} else {
		logger.Warn("--discord-app-id not set, slash commands not registered")
	}

	if *rconHost != "" {
		rconConn, err := rcon.Dial(*rconHost, os.Getenv("RCON_PASSWORD"))
		if err != nil {
			logger.Error("failed to connect to RCON", "host", *rconHost, "err", err)
		} else {
			logger.Info("connected to RCON", "host", *rconHost)
			rconConn.Close()
		}
	} else {
		logger.Warn("--rcon-host not set, RCON commands unavailable")
	}

	if *mqttBroker != "" {
		opts := mqtt.NewClientOptions().
			AddBroker(*mqttBroker).
			SetClientID("zomboid-discord-bot").
			SetAutoReconnect(true)
		if *mqttUsername != "" {
			opts.SetUsername(*mqttUsername)
			opts.SetPassword(os.Getenv("MQTT_PASSWORD"))
		}
		handler := newMQTTHandler(discordSession, *discordChannelID, deps)
		opts.OnConnect = func(c mqtt.Client) {
			topic := *mqttTopicPrefix + "/#"
			if token := c.Subscribe(topic, 1, handler); token.Wait() && token.Error() != nil {
				logger.Error("failed to subscribe to MQTT topic", "topic", topic, "err", token.Error())
				return
			}
			logger.Info("subscribed to MQTT topic", "topic", topic)
		}
		mqttClient := mqtt.NewClient(opts)
		if token := mqttClient.Connect(); token.Wait() && token.Error() != nil {
			logger.Error("failed to connect to MQTT broker", "broker", *mqttBroker, "err", token.Error())
		} else {
			defer mqttClient.Disconnect(250)
		}
	} else {
		logger.Warn("--mqtt-broker not set, live event stream unavailable")
	}

	logger.Info("zomboid-discord-bot started")
	<-ctx.Done()
	logger.Info("shutting down")
	time.Sleep(200 * time.Millisecond) // let deferred Discord/MQTT/Postgres cleanup finish
}

// newMQTTHandler returns an MQTT message handler that posts each event to
// channelID as a Discord message, then checks it against milestones (see
// milestones.go) and posts any newly-hit ones as a separate, distinct
// message. If channelID is empty, events are only logged -- lets
// --mqtt-broker be tested without a channel configured yet. world_stats is
// deliberately never posted: it's periodic housekeeping telemetry with no
// player attached, not a notable live event.
func newMQTTHandler(session *discordgo.Session, channelID string, deps botDeps) mqtt.MessageHandler {
	return func(client mqtt.Client, msg mqtt.Message) {
		eventType := eventTypeFromTopic(msg.Topic())
		if eventType == "world_stats" {
			return
		}

		var fields map[string]any
		if err := json.Unmarshal(msg.Payload(), &fields); err != nil {
			slog.Error("mqtt: failed to unmarshal event payload", "topic", msg.Topic(), "err", err)
			return
		}

		text := formatEvent(eventType, fields)
		slog.Info("mqtt event received", "topic", msg.Topic(), "text", text)

		if channelID != "" {
			if _, err := session.ChannelMessageSend(channelID, text); err != nil {
				slog.Error("failed to post event to Discord", "channelID", channelID, "err", err)
			}
		}

		steamID, _ := fields["steamId"].(string)
		username, _ := fields["username"].(string)
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		for _, hit := range checkMilestones(ctx, deps.db, eventType, steamID, fields) {
			slog.Info("milestone hit", "name", hit.Name, "steamID", steamID, "username", username)
			if channelID != "" {
				discordText := fmt.Sprintf("🏆 **%s** — %s", username, hit.Message)
				if _, err := session.ChannelMessageSend(channelID, discordText); err != nil {
					slog.Error("failed to post milestone to Discord", "channelID", channelID, "err", err)
				}
			}
			if deps.rconHost != "" {
				// servermsg's argument is quote-delimited (it can contain
				// spaces) -- escape any embedded quotes in case a future
				// milestone message or a username ever has one, rather than
				// letting it break RCON's own argument parsing.
				ingameText := strings.ReplaceAll(fmt.Sprintf("Milestone: %s -- %s", username, hit.Message), `"`, "'")
				if _, err := rconExecute(deps.rconHost, deps.rconPassword, fmt.Sprintf(`servermsg "%s"`, ingameText)); err != nil {
					slog.Error("failed to broadcast milestone in-game", "err", err)
				}
			}
		}
	}
}
