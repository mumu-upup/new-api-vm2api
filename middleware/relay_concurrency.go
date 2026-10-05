package middleware

import (
	"bufio"
	"context"
	"errors"
	"net"
	"net/http"
	"sync"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/service"
	"github.com/gin-gonic/gin"
)

// A lost Redis lease must terminate upgraded connections too; cancelling the
// HTTP request alone does not interrupt a WebSocket blocked in ReadMessage.
type concurrencyResponseWriter struct {
	gin.ResponseWriter
	mu         sync.Mutex
	connection net.Conn
	cancelled  bool
}

func (writer *concurrencyResponseWriter) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	connection, buffer, err := writer.ResponseWriter.Hijack()
	if err != nil {
		return nil, nil, err
	}
	writer.mu.Lock()
	defer writer.mu.Unlock()
	writer.connection = connection
	if writer.cancelled {
		_ = connection.Close()
		return nil, nil, context.Canceled
	}
	return connection, buffer, nil
}

func (writer *concurrencyResponseWriter) cancelConnection() {
	writer.mu.Lock()
	defer writer.mu.Unlock()
	writer.cancelled = true
	if writer.connection != nil {
		_ = writer.connection.Close()
	}
}

// RelayConcurrency belongs immediately after authentication and before channel
// selection. A single admission spans retries, SSE, and the entire WebSocket.
func RelayConcurrency() gin.HandlerFunc {
	return func(c *gin.Context) {
		userID := c.GetInt("id")
		if userID <= 0 {
			abortWithOpenAiMessage(c, http.StatusUnauthorized, "authentication required")
			return
		}
		limit, err := model.GetUserConcurrencyLimit(userID)
		if err != nil {
			abortWithOpenAiMessage(c, http.StatusServiceUnavailable, "concurrency limit unavailable")
			return
		}
		lease, err := service.AcquireRelayConcurrency(c.Request.Context(), userID, c.GetInt("token_id"), limit)
		if err != nil {
			if errors.Is(err, service.ErrRelayConcurrencyLimit) {
				c.Header("Retry-After", "1")
				abortWithOpenAiMessage(c, http.StatusTooManyRequests, "user concurrent request limit reached")
				return
			}
			abortWithOpenAiMessage(c, http.StatusServiceUnavailable, "concurrency store unavailable")
			return
		}
		ctx, cancel := context.WithCancel(c.Request.Context())
		c.Request = c.Request.WithContext(ctx)
		writer := &concurrencyResponseWriter{ResponseWriter: c.Writer}
		c.Writer = writer
		done := make(chan struct{})
		heartbeatDone := make(chan struct{})
		defer func() {
			close(done)
			cancel()
			<-heartbeatDone
			releaseCtx, releaseCancel := context.WithTimeout(context.Background(), 3*time.Second)
			defer releaseCancel()
			if err := lease.Release(releaseCtx); err != nil {
				common.SysError("failed to release relay concurrency lease: " + err.Error())
			}
		}()
		go func() {
			defer close(heartbeatDone)
			ticker := time.NewTicker(service.RelayConcurrencyHeartbeatInterval)
			defer ticker.Stop()
			requestDone := ctx.Done()
			for {
				select {
				case <-done:
					return
				case <-requestDone:
					writer.cancelConnection()
					requestDone = nil
				case <-ticker.C:
					refreshCtx, refreshCancel := context.WithTimeout(context.Background(), 3*time.Second)
					err := lease.Refresh(refreshCtx)
					refreshCancel()
					if err != nil {
						cancel()
						writer.cancelConnection()
						common.SysError("relay concurrency lease lost: " + err.Error())
						return
					}
				}
			}
		}()
		c.Next()
	}
}
