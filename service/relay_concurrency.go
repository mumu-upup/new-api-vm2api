package service

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"sync"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/go-redis/redis/v8"
	"github.com/google/uuid"
)

const relayConcurrencyLeaseTTL = 90 * time.Second
const RelayConcurrencyHeartbeatInterval = 20 * time.Second

var ErrRelayConcurrencyLimit = errors.New("user concurrent request limit reached")

// RelayConcurrencySnapshot counts active gateway requests, including streaming
// requests and WebSocket connections until their handlers return.
type RelayConcurrencySnapshot struct {
	Current int         `json:"current"`
	Tokens  map[int]int `json:"tokens"`
}

type RelayConcurrencyLease struct {
	userID  int
	tokenID int
	id      string
	redis   *redis.Client
}

var relayConcurrencyMemory = struct {
	sync.Mutex
	users map[int]map[string]int
}{users: make(map[int]map[string]int)}

// Every operation uses Redis TIME: instance clock skew cannot free another
// instance's live leases. User and token keys share a Redis Cluster hash slot.
const relayConcurrencyTimeLua = `
local clock = redis.call('TIME')
local now = tonumber(clock[1]) * 1000 + math.floor(tonumber(clock[2]) / 1000)
for _, key in ipairs(KEYS) do redis.call('ZREMRANGEBYSCORE', key, '-inf', now) end
`

var relayConcurrencyAcquireScript = redis.NewScript(relayConcurrencyTimeLua + `
local count = redis.call('ZCARD', KEYS[1])
if tonumber(ARGV[1]) > 0 and count >= tonumber(ARGV[1]) then return 0 end
for _, key in ipairs(KEYS) do
 redis.call('ZADD', key, now + tonumber(ARGV[3]), ARGV[2])
 redis.call('PEXPIRE', key, ARGV[3])
end
return 1
`)

var relayConcurrencyRefreshScript = redis.NewScript(relayConcurrencyTimeLua + `
if not redis.call('ZSCORE', KEYS[1], ARGV[1]) then return 0 end
for _, key in ipairs(KEYS) do
 redis.call('ZADD', key, now + tonumber(ARGV[2]), ARGV[1])
 redis.call('PEXPIRE', key, ARGV[2])
end
return 1
`)

var relayConcurrencyReleaseScript = redis.NewScript(`
for _, key in ipairs(KEYS) do redis.call('ZREM', key, ARGV[1]) end
return 1
`)

var relayConcurrencySnapshotScript = redis.NewScript(relayConcurrencyTimeLua + `
local counts = {}
for _, key in ipairs(KEYS) do table.insert(counts, redis.call('ZCARD', key)) end
return counts
`)

func relayConcurrencyKeys(userID, tokenID int) []string {
	prefix := fmt.Sprintf("relay:concurrency:{%d}", userID)
	return []string{prefix, prefix + ":token:" + strconv.Itoa(tokenID)}
}

// AcquireRelayConcurrency atomically reserves a slot shared by all a user's
// tokens. Redis errors deliberately fail closed, including for unlimited users,
// so live counts cannot silently diverge between gateway instances.
func AcquireRelayConcurrency(ctx context.Context, userID, tokenID, limit int) (*RelayConcurrencyLease, error) {
	if userID <= 0 || tokenID < 0 || limit < 0 {
		return nil, errors.New("invalid concurrency admission")
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	lease := &RelayConcurrencyLease{userID: userID, tokenID: tokenID, id: uuid.NewString()}
	if common.RedisEnabled {
		if common.RDB == nil {
			return nil, errors.New("concurrency store unavailable")
		}
		lease.redis = common.RDB
		allowed, err := relayConcurrencyAcquireScript.Run(ctx, lease.redis, relayConcurrencyKeys(userID, tokenID), limit, lease.id, relayConcurrencyLeaseTTL.Milliseconds()).Int()
		if err != nil {
			return nil, err
		}
		if allowed == 0 {
			return nil, ErrRelayConcurrencyLimit
		}
		return lease, nil
	}
	relayConcurrencyMemory.Lock()
	defer relayConcurrencyMemory.Unlock()
	leases := relayConcurrencyMemory.users[userID]
	if limit > 0 && len(leases) >= limit {
		return nil, ErrRelayConcurrencyLimit
	}
	if leases == nil {
		leases = make(map[string]int)
		relayConcurrencyMemory.users[userID] = leases
	}
	leases[lease.id] = tokenID
	return lease, nil
}

func (lease *RelayConcurrencyLease) Refresh(ctx context.Context) error {
	if lease.redis == nil {
		return nil
	}
	refreshed, err := relayConcurrencyRefreshScript.Run(ctx, lease.redis, relayConcurrencyKeys(lease.userID, lease.tokenID), lease.id, relayConcurrencyLeaseTTL.Milliseconds()).Int()
	if err != nil {
		return err
	}
	if refreshed == 0 {
		return errors.New("concurrency lease expired")
	}
	return nil
}

// Release is idempotent. Each lease has an independent random ID, so delayed
// cleanup cannot release a newer request that reused the same API token.
func (lease *RelayConcurrencyLease) Release(ctx context.Context) error {
	if lease.redis != nil {
		return relayConcurrencyReleaseScript.Run(ctx, lease.redis, relayConcurrencyKeys(lease.userID, lease.tokenID), lease.id).Err()
	}
	relayConcurrencyMemory.Lock()
	defer relayConcurrencyMemory.Unlock()
	leases := relayConcurrencyMemory.users[lease.userID]
	delete(leases, lease.id)
	if len(leases) == 0 {
		delete(relayConcurrencyMemory.users, lease.userID)
	}
	return nil
}

func GetRelayConcurrency(ctx context.Context, userID int, tokenIDs []int) (RelayConcurrencySnapshot, error) {
	snapshot := RelayConcurrencySnapshot{Tokens: make(map[int]int, len(tokenIDs))}
	if common.RedisEnabled {
		if common.RDB == nil {
			return snapshot, errors.New("concurrency store unavailable")
		}
		keys := []string{fmt.Sprintf("relay:concurrency:{%d}", userID)}
		for _, tokenID := range tokenIDs {
			keys = append(keys, relayConcurrencyKeys(userID, tokenID)[1])
		}
		counts, err := relayConcurrencySnapshotScript.Run(ctx, common.RDB, keys).Int64Slice()
		if err != nil {
			return snapshot, err
		}
		snapshot.Current = int(counts[0])
		for index, tokenID := range tokenIDs {
			snapshot.Tokens[tokenID] = int(counts[index+1])
		}
		return snapshot, nil
	}
	relayConcurrencyMemory.Lock()
	defer relayConcurrencyMemory.Unlock()
	leases := relayConcurrencyMemory.users[userID]
	snapshot.Current = len(leases)
	for _, tokenID := range tokenIDs {
		snapshot.Tokens[tokenID] = 0
	}
	for _, tokenID := range leases {
		if _, ok := snapshot.Tokens[tokenID]; ok {
			snapshot.Tokens[tokenID]++
		}
	}
	return snapshot, nil
}
