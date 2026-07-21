package main

import (
	"github.com/sealessland/sea-music/internal/events"
	"github.com/sealessland/sea-music/internal/platform/config"
)

func eventBrokerConfig(cfg config.Config) events.BrokerConfig {
	return events.BrokerConfig{
		Driver: cfg.Broker.Driver, Endpoints: cfg.Broker.Brokers,
		AccessKey: cfg.Broker.AccessKey, AccessSecret: cfg.Broker.AccessSecret,
	}
}
