package service

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"net"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
)

var (
	ErrPartnerUnauthorized = errors.New("partner unauthorized")
	ErrRequestExpired      = errors.New("request expired")
	ErrInvalidRequest      = errors.New("invalid partner request")
	ErrInvalidSignature    = errors.New("invalid partner signature")
	ErrNonceReplay         = errors.New("nonce already used")
	ErrIPNotAllowed        = errors.New("client ip not allowed")
	ErrPartnerRateLimited  = errors.New("partner rate limit exceeded")
	ErrRateLimited         = ErrPartnerRateLimited
)

type PartnerRequest struct {
	Context   context.Context
	PartnerID string
	Timestamp int64
	Nonce     string
	Signature string
	Method    string
	Path      string
	Body      []byte
	ClientIP  net.IP
}

const (
	partnerTimestampSkew = 5 * time.Minute
	partnerNonceTTL      = 10 * time.Minute
	partnerMaxNonce      = 128
	partnerMaxLocalKeys  = 10000
)

var partnerNoncePattern = regexp.MustCompile(`^[A-Za-z0-9._~-]+$`)

type partnerLocalEntry struct {
	expires time.Time
	count   int
}

var partnerLocalStore = struct {
	sync.Mutex
	nonces map[string]partnerLocalEntry
	counts map[string]partnerLocalEntry
}{nonces: make(map[string]partnerLocalEntry), counts: make(map[string]partnerLocalEntry)}

func BuildPartnerSignature(method, path string, timestamp int64, nonce string, body []byte, secret []byte) string {
	digest := sha256.Sum256(body)
	canonical := strings.ToUpper(method) + "\n" + path + "\n" + strconv.FormatInt(timestamp, 10) + "\n" + nonce + "\n" + hex.EncodeToString(digest[:])
	mac := hmac.New(sha256.New, secret)
	_, _ = mac.Write([]byte(canonical))
	return hex.EncodeToString(mac.Sum(nil))
}

func VerifyPartnerRequest(ctx context.Context, req PartnerRequest) (*model.RedemptionPartner, error) {
	if ctx == nil {
		ctx = req.Context
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if strings.TrimSpace(req.PartnerID) == "" || req.Timestamp <= 0 || req.Method == "" || req.Path == "" || req.Signature == "" || req.ClientIP == nil {
		return nil, ErrInvalidRequest
	}
	if len(req.Nonce) == 0 || len(req.Nonce) > partnerMaxNonce || !partnerNoncePattern.MatchString(req.Nonce) {
		return nil, ErrInvalidRequest
	}
	now := time.Now().Unix()
	if req.Timestamp > now+int64(partnerTimestampSkew/time.Second) || req.Timestamp < now-int64(partnerTimestampSkew/time.Second) {
		return nil, ErrRequestExpired
	}

	partner, err := model.GetRedemptionPartner(req.PartnerID)
	if err != nil || partner == nil {
		return nil, ErrPartnerUnauthorized
	}
	if !ipAllowed(req.ClientIP, partner.IPAllowlist) {
		return nil, ErrIPNotAllowed
	}
	expected := BuildPartnerSignature(req.Method, req.Path, req.Timestamp, req.Nonce, req.Body, []byte(partner.Secret))
	provided, err := hex.DecodeString(req.Signature)
	if err != nil || !hmac.Equal(provided, mustDecodeHex(expected)) {
		return nil, ErrInvalidSignature
	}

	nonceKey := "partner:" + partner.PartnerID + ":nonce:" + req.Nonce
	used, err := setOnce(ctx, nonceKey, partnerNonceTTL)
	if err != nil {
		return nil, fmt.Errorf("partner nonce store: %w", err)
	}
	if !used {
		return nil, ErrNonceReplay
	}
	if partner.RateLimit > 0 {
		window := time.Now().Unix() / 60
		allowed, err := takeRateLimit(ctx, "partner:"+partner.PartnerID+":rate:"+strconv.FormatInt(window, 10), partner.RateLimit)
		if err != nil {
			return nil, fmt.Errorf("partner rate limit store: %w", err)
		}
		if !allowed {
			return nil, ErrPartnerRateLimited
		}
	}
	return partner, nil
}

func mustDecodeHex(s string) []byte { b, _ := hex.DecodeString(s); return b }

func setOnce(ctx context.Context, key string, ttl time.Duration) (bool, error) {
	if common.RedisEnabled && common.RDB != nil {
		return common.RedisSetNX(key, "1", ttl)
	}
	now := time.Now()
	partnerLocalStore.Lock()
	defer partnerLocalStore.Unlock()
	for k, v := range partnerLocalStore.nonces {
		if !v.expires.After(now) {
			delete(partnerLocalStore.nonces, k)
		}
	}
	if v, ok := partnerLocalStore.nonces[key]; ok && v.expires.After(now) {
		return false, nil
	}
	if len(partnerLocalStore.nonces) >= partnerMaxLocalKeys {
		return false, errors.New("local nonce store is full")
	}
	partnerLocalStore.nonces[key] = partnerLocalEntry{expires: now.Add(ttl), count: 1}
	return true, nil
}

func takeRateLimit(ctx context.Context, key string, max int) (bool, error) {
	if common.RedisEnabled && common.RDB != nil {
		n, err := common.RDB.Incr(ctx, key).Result()
		if err != nil {
			return false, err
		}
		if n == 1 {
			if err = common.RDB.Expire(ctx, key, time.Minute+time.Second).Err(); err != nil {
				return false, err
			}
		}
		return n <= int64(max), nil
	}
	now := time.Now()
	partnerLocalStore.Lock()
	defer partnerLocalStore.Unlock()
	for k, v := range partnerLocalStore.counts {
		if !v.expires.After(now) {
			delete(partnerLocalStore.counts, k)
		}
	}
	entry, ok := partnerLocalStore.counts[key]
	if !ok {
		if len(partnerLocalStore.counts) >= partnerMaxLocalKeys {
			return false, errors.New("local rate store is full")
		}
		partnerLocalStore.counts[key] = partnerLocalEntry{expires: now.Add(time.Minute), count: 1}
		return true, nil
	}
	entry.count++
	partnerLocalStore.counts[key] = entry
	return entry.count <= max, nil
}

func ipAllowed(ip net.IP, allowlist string) bool {
	allowlist = strings.TrimSpace(allowlist)
	if allowlist == "" {
		return true
	}
	for _, item := range strings.Split(allowlist, ",") {
		item = strings.TrimSpace(item)
		if item == "" {
			continue
		}
		if parsed := net.ParseIP(item); parsed != nil && parsed.Equal(ip) {
			return true
		}
		if _, network, err := net.ParseCIDR(item); err == nil && network.Contains(ip) {
			return true
		}
	}
	return false
}
