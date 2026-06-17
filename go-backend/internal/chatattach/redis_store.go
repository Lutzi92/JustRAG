package chatattach

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/redis/go-redis/v9"
)

const keyPrefix = "chat_attachment:"

// DefaultTTL is the per-entry TTL applied when a non-positive TTL is given.
const DefaultTTL = 24 * time.Hour

// RedisStore persists chat attachments in Redis with a per-entry TTL. It
// matches the client type and key conventions used by internal/sessionmem.
type RedisStore struct {
	client *redis.Client
	ttl    time.Duration
}

var _ Store = (*RedisStore)(nil)

// NewRedisStore returns a RedisStore using the given client and TTL.
func NewRedisStore(c *redis.Client, ttl time.Duration) *RedisStore {
	if ttl <= 0 {
		ttl = DefaultTTL
	}
	return &RedisStore{client: c, ttl: ttl}
}

func encodeAttachment(att Attachment) ([]byte, error) { return json.Marshal(att) }

func decodeAttachment(b []byte) (Attachment, error) {
	var att Attachment
	err := json.Unmarshal(b, &att)
	return att, err
}

func (s *RedisStore) Put(ctx context.Context, att Attachment) (string, error) {
	if att.ID == "" {
		id, err := newID()
		if err != nil {
			return "", err
		}
		att.ID = id
	}
	if att.CreatedAt.IsZero() {
		att.CreatedAt = time.Now()
	}
	b, err := encodeAttachment(att)
	if err != nil {
		return "", err
	}
	if err := s.client.Set(ctx, keyPrefix+att.ID, b, s.ttl).Err(); err != nil {
		return "", fmt.Errorf("chatattach: redis set: %w", err)
	}
	return att.ID, nil
}

func (s *RedisStore) Get(ctx context.Context, id string) (Attachment, error) {
	b, err := s.client.Get(ctx, keyPrefix+id).Bytes()
	if errors.Is(err, redis.Nil) {
		return Attachment{}, ErrNotFound
	}
	if err != nil {
		return Attachment{}, fmt.Errorf("chatattach: redis get: %w", err)
	}
	att, err := decodeAttachment(b)
	if err != nil {
		return Attachment{}, fmt.Errorf("chatattach: redis get: decode: %w", err)
	}
	return att, nil
}
