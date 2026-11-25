package durable

import (
	"encoding/json"
	"time"

	"github.com/FlyTOmeLight/agentic-kit-api/pkg/execution"
)

type StreamDelta = execution.StreamDelta

const (
	EventModelReasoningDelta = execution.EventModelReasoningDelta
	EventModelContentDelta   = execution.EventModelContentDelta
	LiveStreamField          = "delta"
	LiveStreamMaxLen         = int64(10000)
	LiveStreamTTL            = time.Hour
	PublishLiveDeltaLua      = `local id = redis.call('XADD', KEYS[1], 'MAXLEN', '~', ARGV[1], '*', 'delta', ARGV[2])
redis.call('EXPIRE', KEYS[1], ARGV[3])
return id`
)

func LiveStreamKey(id string) string { return "durable:run:" + id + ":live" }

func EncodeStreamDelta(delta StreamDelta) ([]byte, error) {
	if err := validateStreamDelta(delta); err != nil {
		return nil, err
	}
	return json.Marshal(delta)
}

func DecodeStreamDelta(data []byte) (StreamDelta, error) {
	var delta StreamDelta
	if err := json.Unmarshal(data, &delta); err != nil {
		return StreamDelta{}, err
	}
	if err := validateStreamDelta(delta); err != nil {
		return StreamDelta{}, err
	}
	return delta, nil
}

func validateStreamDelta(delta StreamDelta) error {
	if delta.RunID == "" || delta.Epoch < 1 || delta.Turn < 0 || delta.Offset < 0 || delta.Delta == "" {
		return ErrInvalid
	}
	switch delta.Kind {
	case EventModelReasoningDelta, EventModelContentDelta:
		return nil
	default:
		return ErrInvalid
	}
}
