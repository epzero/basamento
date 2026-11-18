package bus

import (
	"context"
	"errors"
	"github.com/nats-io/nats.go/jetstream"
	"slices"
)

type jetAssets struct {
	stream jetstream.Stream
	meta   jetstream.KeyValue
	data   jetstream.KeyValue
}

func openJetAssets(ctx context.Context, js jetstream.JetStream, opts Options) (jetAssets, error) {
	wantSubject := busSubjectPrefix + ">"
	str, err := js.Stream(ctx, streamName)
	if err != nil {
		if !errors.Is(err, jetstream.ErrStreamNotFound) {
			return jetAssets{}, mapConnErr(err)
		}
		cfg := jetstream.StreamConfig{
			Name:        streamName,
			Subjects:    []string{wantSubject},
			Storage:     jetstream.FileStorage,
			MaxMsgSize:  int32(opts.MaxMessageSize),
			AllowDirect: true,
			Replicas:    opts.Replicas,
		}
		str, err = js.CreateStream(ctx, cfg)
		if err != nil {
			return jetAssets{}, mapConnErr(err)
		}
	} else if err := verifyStream(ctx, str, opts); err != nil {
		return jetAssets{}, err
	}
	meta, err := openKV(ctx, js, metaBucket, kvMetaOpts(opts))
	if err != nil {
		return jetAssets{}, err
	}
	data, err := openKV(ctx, js, dataBucket, kvDataOpts(opts))
	if err != nil {
		return jetAssets{}, err
	}
	return jetAssets{stream: str, meta: meta, data: data}, nil
}

func verifyStream(ctx context.Context, str jetstream.Stream, opts Options) error {
	info, err := str.Info(ctx)
	if err != nil {
		return mapConnErr(err)
	}
	cfg := info.Config
	want := busSubjectPrefix + ">"
	if !slices.Contains(cfg.Subjects, want) {
		return assetMismatch(streamName)
	}
	if cfg.MaxMsgSize > 0 && int(cfg.MaxMsgSize) < opts.MaxMessageSize {
		return assetMismatch(streamName)
	}
	if cfg.MaxAge > 0 {
		return assetMismatch(streamName)
	}
	return nil
}

func kvMetaOpts(opts Options) jetstream.KeyValueConfig {
	return jetstream.KeyValueConfig{
		Bucket:   metaBucket,
		Replicas: opts.Replicas,
		History:  1,
	}
}

func kvDataOpts(opts Options) jetstream.KeyValueConfig {
	return jetstream.KeyValueConfig{
		Bucket:   dataBucket,
		Replicas: opts.Replicas,
		History:  1,
	}
}

func openKV(ctx context.Context, js jetstream.JetStream, bucket string, want jetstream.KeyValueConfig) (jetstream.KeyValue, error) {
	kv, err := js.KeyValue(ctx, bucket)
	if err != nil {
		if !errors.Is(err, jetstream.ErrBucketNotFound) {
			return nil, mapConnErr(err)
		}
		return js.CreateKeyValue(ctx, want)
	}
	status, err := kv.Status(ctx)
	if err != nil {
		return nil, mapConnErr(err)
	}
	if status.TTL() > 0 {
		return nil, assetMismatch(bucket)
	}
	if want.History > 0 && status.History() < int64(want.History) {
		return nil, assetMismatch(bucket)
	}
	return kv, nil
}
