package service

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/alicebob/miniredis/v2"
	"github.com/go-redis/redis/v8"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestRelayConcurrencySharedCapAndRelease(t *testing.T) {
	for _, backend := range []string{"memory", "redis"} {
		t.Run(backend, func(t *testing.T) {
			oldEnabled, oldRedis := common.RedisEnabled, common.RDB
			common.RedisEnabled = backend == "redis"
			if common.RedisEnabled {
				server := miniredis.RunT(t)
				common.RDB = redis.NewClient(&redis.Options{Addr: server.Addr(), MaxRetries: -1})
				client := common.RDB
				t.Cleanup(func() { _ = client.Close() })
			}
			defer func() { common.RedisEnabled, common.RDB = oldEnabled, oldRedis }()
			ctx := context.Background()
			start := make(chan struct{})
			results := make(chan *RelayConcurrencyLease, 8)
			errors := make(chan error, 8)
			var workers sync.WaitGroup
			for tokenID := range 8 {
				workers.Go(func() {
					<-start
					lease, err := AcquireRelayConcurrency(ctx, 9001, tokenID+1, 3)
					results <- lease
					errors <- err
				})
			}
			close(start)
			workers.Wait()
			close(results)
			close(errors)
			var leases []*RelayConcurrencyLease
			for lease := range results {
				if lease != nil {
					leases = append(leases, lease)
				}
			}
			require.Len(t, leases, 3)
			denied := 0
			for err := range errors {
				if err != nil {
					require.ErrorIs(t, err, ErrRelayConcurrencyLimit)
					denied++
				}
			}
			assert.Equal(t, 5, denied)
			snapshot, err := GetRelayConcurrency(ctx, 9001, []int{1, 2, 3, 4, 5, 6, 7, 8})
			require.NoError(t, err)
			assert.Equal(t, 3, snapshot.Current)
			sum := 0
			for _, count := range snapshot.Tokens {
				sum += count
			}
			assert.Equal(t, 3, sum)
			independent, err := AcquireRelayConcurrency(ctx, 9002, 1, 1)
			require.NoError(t, err)
			require.NoError(t, independent.Release(ctx))
			for _, lease := range leases {
				require.NoError(t, lease.Release(ctx))
				require.NoError(t, lease.Release(ctx))
			}
			snapshot, err = GetRelayConcurrency(ctx, 9001, []int{1, 2, 3})
			require.NoError(t, err)
			assert.Zero(t, snapshot.Current)
			first, err := AcquireRelayConcurrency(ctx, 9001, 1, 0)
			require.NoError(t, err)
			second, err := AcquireRelayConcurrency(ctx, 9001, 1, 0)
			require.NoError(t, err)
			snapshot, err = GetRelayConcurrency(ctx, 9001, []int{1})
			require.NoError(t, err)
			assert.Equal(t, 2, snapshot.Current)
			assert.Equal(t, 2, snapshot.Tokens[1])
			require.NoError(t, first.Release(ctx))
			require.NoError(t, second.Release(ctx))
		})
	}
}

func TestRelayConcurrencyRedisLeaseRecoveryAndFailure(t *testing.T) {
	oldEnabled, oldRedis := common.RedisEnabled, common.RDB
	defer func() { common.RedisEnabled, common.RDB = oldEnabled, oldRedis }()
	server := miniredis.RunT(t)
	common.RedisEnabled = true
	common.RDB = redis.NewClient(&redis.Options{Addr: server.Addr(), MaxRetries: -1})
	defer common.RDB.Close()
	ctx := context.Background()
	lease, err := AcquireRelayConcurrency(ctx, 9101, 9, 1)
	require.NoError(t, err)
	server.FastForward(relayConcurrencyLeaseTTL / 2)
	server.SetTime(time.Now().Add(relayConcurrencyLeaseTTL / 2))
	require.NoError(t, lease.Refresh(ctx))
	server.FastForward(relayConcurrencyLeaseTTL / 2)
	server.SetTime(time.Now().Add(relayConcurrencyLeaseTTL))
	_, err = AcquireRelayConcurrency(ctx, 9101, 10, 1)
	require.ErrorIs(t, err, ErrRelayConcurrencyLimit)
	server.FastForward(relayConcurrencyLeaseTTL)
	server.SetTime(time.Now().Add(2 * relayConcurrencyLeaseTTL))
	replacement, err := AcquireRelayConcurrency(ctx, 9101, 10, 1)
	require.NoError(t, err)
	require.Error(t, lease.Refresh(ctx))
	require.NoError(t, lease.Release(ctx))
	snapshot, err := GetRelayConcurrency(ctx, 9101, []int{9, 10})
	require.NoError(t, err)
	assert.Equal(t, 1, snapshot.Current)
	assert.Zero(t, snapshot.Tokens[9])
	assert.Equal(t, 1, snapshot.Tokens[10])
	require.NoError(t, replacement.Release(ctx))
	server.Close()
	_, err = AcquireRelayConcurrency(ctx, 9101, 9, 0)
	require.Error(t, err)
	_, err = GetRelayConcurrency(ctx, 9101, []int{9})
	require.Error(t, err)
	common.RDB = nil
	_, err = AcquireRelayConcurrency(ctx, 9101, 9, 1)
	require.Error(t, err)
}
