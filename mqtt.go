package main

import (
	"encoding/json"
	"log/slog"
	"time"

	mqtt "github.com/eclipse/paho.mqtt.golang"
)

// mqttPublisher publishes ExporterLog events to an MQTT broker as they're
// ingested, so a Discord bot (or anything else) can subscribe to
// "<topicPrefix>/#" for a live event stream without touching Postgres or
// the game server pod. Entirely optional and best-effort: publish never
// blocks the ingestion pipeline and a broker outage never drops an event
// from Postgres, only from the live stream.
type mqttPublisher struct {
	client      mqtt.Client
	topicPrefix string
}

// newMQTTPublisher connects to broker (e.g. "tcp://mosquitto.mqtt.svc.cluster.local:1883").
// Events are published to "<topicPrefix>/<event-type>". username/password
// may be empty if the broker allows anonymous connections.
func newMQTTPublisher(broker, topicPrefix, username, password string) (*mqttPublisher, error) {
	opts := mqtt.NewClientOptions().
		AddBroker(broker).
		SetClientID("zomboid-exporter").
		SetAutoReconnect(true).
		SetConnectTimeout(5 * time.Second)
	if username != "" {
		opts.SetUsername(username)
		opts.SetPassword(password)
	}
	client := mqtt.NewClient(opts)
	if token := client.Connect(); token.WaitTimeout(5*time.Second) && token.Error() != nil {
		return nil, token.Error()
	}
	return &mqttPublisher{client: client, topicPrefix: topicPrefix}, nil
}

// publish is fire-and-forget (QoS 0, non-blocking): it must never slow down
// or interrupt the ExporterLog ingestion loop that calls it.
func (p *mqttPublisher) publish(ev *exporterEvent) {
	if p == nil {
		return
	}
	payload, err := json.Marshal(ev.Fields)
	if err != nil {
		slog.Error("mqtt: failed to marshal event", "eventType", ev.EventType, "err", err)
		return
	}
	topic := p.topicPrefix + "/" + ev.EventType
	p.client.Publish(topic, 0, false, payload)
}

func (p *mqttPublisher) close() {
	if p == nil {
		return
	}
	p.client.Disconnect(250)
}
