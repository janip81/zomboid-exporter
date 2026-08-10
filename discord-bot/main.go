package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/bwmarrin/discordgo"
	mqtt "github.com/eclipse/paho.mqtt.golang"
	"github.com/gorcon/rcon"
	"github.com/jackc/pgx/v5/pgxpool"
)

func main() {
	rconHost := flag.String("rcon-host", "", "Zomboid RCON host:port, e.g. zomboid-zomboid-server-rcon.zomboid.svc.cluster.local:27015")
	mqttBroker := flag.String("mqtt-broker", "", "MQTT broker URL, e.g. tcp://mosquitto.mqtt.svc.cluster.local:1883")
	mqttUsername := flag.String("mqtt-username", "", "MQTT broker username, if auth is required (password read from MQTT_PASSWORD env var)")
	mqttTopicPrefix := flag.String("mqtt-topic-prefix", "zomboid/those-who-remain", "MQTT topic prefix to subscribe to (subscribes to <prefix>/#)")
	discordChannelID := flag.String("discord-channel-id", "", "Discord channel ID to post live MQTT events into. If empty, events are only logged, not posted.")
	dbHost := flag.String("db-host", "", "Postgres host for stats/leaderboard queries")
	dbPort := flag.Int("db-port", 5432, "Postgres port")
	dbName := flag.String("db-name", "zomboid", "Postgres database name")
	dbUser := flag.String("db-user", "zomboid", "Postgres user (password read from DB_PASSWORD env var)")
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

	discordSession, err := discordgo.New("Bot " + discordToken)
	if err != nil {
		logger.Error("failed to create Discord session", "err", err)
		os.Exit(1)
	}
	if err := discordSession.Open(); err != nil {
		logger.Error("failed to connect to Discord", "err", err)
		os.Exit(1)
	}
	defer discordSession.Close()
	logger.Info("connected to Discord", "user", discordSession.State.User.Username)

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
		handler := newMQTTHandler(discordSession, *discordChannelID)
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

	if *dbHost != "" {
		dsn := fmt.Sprintf("postgres://%s:%s@%s:%d/%s", *dbUser, os.Getenv("DB_PASSWORD"), *dbHost, *dbPort, *dbName)
		dbPool, err := pgxpool.New(ctx, dsn)
		if err != nil {
			logger.Error("failed to create Postgres pool", "err", err)
		} else {
			defer dbPool.Close()
			if err := dbPool.Ping(ctx); err != nil {
				logger.Error("failed to reach Postgres", "err", err)
			} else {
				logger.Info("connected to Postgres, stats/leaderboard commands enabled")
			}
		}
	} else {
		logger.Warn("--db-host not set, stats/leaderboard commands unavailable")
	}

	logger.Info("zomboid-discord-bot started")
	<-ctx.Done()
	logger.Info("shutting down")
	time.Sleep(200 * time.Millisecond) // let deferred Discord/MQTT/Postgres cleanup finish
}

// newMQTTHandler returns an MQTT message handler that posts each event to
// channelID as a Discord message. If channelID is empty, events are only
// logged -- lets --mqtt-broker be tested without a channel configured yet.
// world_stats is deliberately never posted: it's periodic housekeeping
// telemetry with no player attached, not a notable live event.
func newMQTTHandler(session *discordgo.Session, channelID string) mqtt.MessageHandler {
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

		if channelID == "" {
			return
		}
		if _, err := session.ChannelMessageSend(channelID, text); err != nil {
			slog.Error("failed to post event to Discord", "channelID", channelID, "err", err)
		}
	}
}
