package bus

import (
	"context"
	"errors"
	"slices"

	"github.com/nats-io/nats.go/jetstream"
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
	meta, err := openKV(ctx, js, metaBucket, kvMetaOpts(opts), opts.MaxMessageSize)
	if err != nil {
		return jetAssets{}, err
	}
	data, err := openKV(ctx, js, dataBucket, kvDataOpts(opts), opts.MaxMessageSize)
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
	if incompatibleBusStreamConfig(info.Config, opts.MaxMessageSize) {
		return assetMismatch(streamName)
	}
	return nil
}

func incompatibleBusStreamConfig(cfg jetstream.StreamConfig, clientMax int) bool {
	want := busSubjectPrefix + ">"
	if !slices.Contains(cfg.Subjects, want) {
		return true
	}
	if cfg.MaxMsgSize > 0 && int(cfg.MaxMsgSize) < clientMax {
		return true
	}
	if cfg.MaxAge > 0 {
		return true
	}
	if cfg.Retention != jetstream.LimitsPolicy {
		return true
	}
	if cfg.MaxMsgs > 0 || cfg.MaxBytes > 0 || cfg.MaxMsgsPerSubject > 0 {
		return true
	}
	if cfg.DenyPurge || cfg.Sealed {
		return true
	}
	if cfg.Mirror != nil || len(cfg.Sources) > 0 {
		return true
	}
	return false
}

func incompatibleKVBackingStreamConfig(cfg jetstream.StreamConfig) bool {
	if cfg.Retention != jetstream.LimitsPolicy {
		return true
	}
	if cfg.MaxAge > 0 {
		return true
	}
	if cfg.DenyPurge || cfg.Sealed {
		return true
	}
	if cfg.Mirror != nil || len(cfg.Sources) > 0 {
		return true
	}
	return false
}

func kvStreamInfo(st jetstream.KeyValueStatus) *jetstream.StreamInfo {
	type getter interface {
		StreamInfo() *jetstream.StreamInfo
	}
	if g, ok := st.(getter); ok {
		return g.StreamInfo()
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

func openKV(ctx context.Context, js jetstream.JetStream, bucket string, want jetstream.KeyValueConfig, clientMax int) (jetstream.KeyValue, error) {
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
	if si := kvStreamInfo(status); si != nil {
		if incompatibleKVBackingStreamConfig(si.Config) {
			return nil, assetMismatch(bucket)
		}
		if si.Config.Mirror != nil || len(si.Config.Sources) > 0 {
			return nil, assetMismatch(bucket)
		}
	}
	return kv, nil
}
