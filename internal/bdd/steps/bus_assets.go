package steps

import (
	"context"
	"fmt"
	"time"

	"github.com/cucumber/godog"
	"github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"

	"github.com/epzero/basamento/platform/bus"
)

func registerBusAssets(sc *godog.ScenarioContext) {
	sc.Step(`^a second client created the JetStream assets the bus requires on the cloud test server$`, func(ctx context.Context) error {
		return provisionBusAssets(ctx, "matching", "")
	})

	sc.Step(`^a second client created the bus asset "([^"]*)" on the cloud test server as "([^"]*)"$`, provisionBusAssetsStep)

	sc.Step(`^the bus asset "([^"]*)" is still configured as "([^"]*)"$`, func(ctx context.Context, asset, fault string) error {
		w := world(ctx).Bus
		if w.backend != "cloud" {
			return godog.ErrSkip
		}
		if err := w.ensureCloudServer(); err != nil {
			return err
		}
		nc, err := nats.Connect(w.nats.URL, nats.Timeout(2*time.Second))
		if err != nil {
			return err
		}
		defer nc.Close()
		js, err := jetstream.New(nc)
		if err != nil {
			return err
		}
		ok, err := assetStillFaulty(ctx, js, asset, fault)
		if err != nil {
			return err
		}
		if !ok {
			return fmt.Errorf("asset %s no longer %s", asset, fault)
		}
		return nil
	})
}

func provisionBusAssetsStep(ctx context.Context, asset, fault string) error {
	return provisionBusAssets(ctx, asset, fault)
}

func provisionBusAssets(ctx context.Context, asset, fault string) error {
	w := world(ctx).Bus
	if w.backend != "cloud" {
		return godog.ErrSkip
	}
	if err := w.ensureCloudServer(); err != nil {
		return err
	}
	nc, err := nats.Connect(w.nats.URL, nats.Timeout(2*time.Second))
	if err != nil {
		return err
	}
	defer nc.Close()
	js, err := jetstream.New(nc)
	if err != nil {
		return err
	}
	switch asset {
	case "matching":
		return createMatchingAssets(ctx, js, w.opts(world(ctx).Core.clock).MaxMessageSize)
	case "KIT_BUS":
		return createFaultyBusStream(ctx, js, fault, w.opts(world(ctx).Core.clock).MaxMessageSize)
	case "KIT_KV":
		return createFaultyKV(ctx, js, fault)
	default:
		return fmt.Errorf("unknown asset %q", asset)
	}
}

func createMatchingAssets(ctx context.Context, js jetstream.JetStream, maxMsg int) error {
	_, err := js.CreateStream(ctx, jetstream.StreamConfig{
		Name:        "KIT_BUS",
		Subjects:    []string{"kit.bus.>"},
		Retention:   jetstream.LimitsPolicy,
		MaxMsgSize:  int32(maxMsg),
		AllowDirect: true,
	})
	if err != nil {
		return err
	}
	_, err = js.CreateKeyValue(ctx, jetstream.KeyValueConfig{Bucket: "KIT_BUS_META", History: 1})
	if err != nil {
		return err
	}
	_, err = js.CreateKeyValue(ctx, jetstream.KeyValueConfig{Bucket: bus.KVDataBucket, History: 1})
	return err
}

func createFaultyBusStream(ctx context.Context, js jetstream.JetStream, fault string, maxMsg int) error {
	cfg := jetstream.StreamConfig{
		Name:       "KIT_BUS",
		Subjects:   []string{"kit.bus.>"},
		MaxMsgSize: int32(maxMsg),
	}
	switch fault {
	case "a mirror":
		cfg.Mirror = &jetstream.StreamSource{Name: "OTHER"}
	case "a source":
		cfg.Sources = []*jetstream.StreamSource{{Name: "OTHER"}}
	case "work-queue retention":
		cfg.Retention = jetstream.WorkQueuePolicy
	default:
		return fmt.Errorf("unknown bus fault %q", fault)
	}
	_, err := js.CreateStream(ctx, cfg)
	return err
}

func createFaultyKV(ctx context.Context, js jetstream.JetStream, fault string) error {
	if fault != "work-queue retention" {
		return fmt.Errorf("unknown kv fault %q", fault)
	}
	_, err := js.CreateStream(ctx, jetstream.StreamConfig{
		Name:      "KV_KIT_KV",
		Retention: jetstream.WorkQueuePolicy,
		Subjects:  []string{"$KV.KIT_KV.>"},
	})
	return err
}

func assetStillFaulty(ctx context.Context, js jetstream.JetStream, asset, fault string) (bool, error) {
	switch asset {
	case "KIT_BUS":
		info, err := js.Stream(ctx, "KIT_BUS")
		if err != nil {
			return false, err
		}
		si, err := info.Info(ctx)
		if err != nil {
			return false, err
		}
		cfg := si.Config
		switch fault {
		case "a mirror":
			return cfg.Mirror != nil, nil
		case "a source":
			return len(cfg.Sources) > 0, nil
		case "work-queue retention":
			return cfg.Retention == jetstream.WorkQueuePolicy, nil
		}
	case "KIT_KV":
		_, err := js.Stream(ctx, "KV_KIT_KV")
		if err != nil {
			return false, err
		}
		if fault == "work-queue retention" {
			info, err := js.Stream(ctx, "KV_KIT_KV")
			if err != nil {
				return false, err
			}
			si, err := info.Info(ctx)
			if err != nil {
				return false, err
			}
			return si.Config.Retention == jetstream.WorkQueuePolicy, nil
		}
	}
	return false, fmt.Errorf("unhandled %s/%s", asset, fault)
}
