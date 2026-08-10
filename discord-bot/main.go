package main

import (
	"context"
	"flag"
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
	discordToken := flag.String("discord-token", "", "Discord bot token (required)")
	rconHost := flag.String("rcon-host", "", "Zomboid RCON host:port, e.g. zomboid-zomboid-server-rcon.zomboid.svc.cluster.local:27015")
	rconPassword := flag.String("rcon-password", "", "Zomboid RCON password")
	mqttBroker := flag.String("mqtt-broker", "", "MQTT broker URL, e.g. tcp://mosquitto.mqtt.svc.cluster.local:1883")
	mqttTopicPrefix := flag.String("mqtt-topic-prefix", "zomboid/those-who-remain", "MQTT topic prefix to subscribe to (subscribes to <prefix>/#)")
	dbDSN := flag.String("db-dsn", "", "Postgres DSN for stats/leaderboard queries, e.g. postgres://user:pass@host:5432/dbname")
	flag.Parse()

	logger := slog.New(slog.NewTextHandler(os.Stdout, nil))
	slog.SetDefault(logger)

	if *discordToken == "" {
		logger.Error("--discord-token is required")
		os.Exit(1)
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	discordSession, err := discordgo.New("Bot " + *discordToken)
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
		rconConn, err := rcon.Dial(*rconHost, *rconPassword)
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
		opts.OnConnect = func(c mqtt.Client) {
			topic := *mqttTopicPrefix + "/#"
			if token := c.Subscribe(topic, 1, onMQTTEvent); token.Wait() && token.Error() != nil {
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

	if *dbDSN != "" {
		dbPool, err := pgxpool.New(ctx, *dbDSN)
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
		logger.Warn("--db-dsn not set, stats/leaderboard commands unavailable")
	}

	logger.Info("zomboid-discord-bot started")
	<-ctx.Done()
	logger.Info("shutting down")
	time.Sleep(200 * time.Millisecond) // let deferred Discord/MQTT/Postgres cleanup finish
}

func onMQTTEvent(client mqtt.Client, msg mqtt.Message) {
	slog.Info("mqtt event received", "topic", msg.Topic(), "payload", string(msg.Payload()))
}
