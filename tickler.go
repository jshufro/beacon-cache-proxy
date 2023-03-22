package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httputil"
	"strconv"
	"time"

	"github.com/jshufro/beacon-cache-proxy/cache"
	"go.uber.org/zap"
)

type tickler struct {
	c      cache.Cache
	proxy  *httputil.ReverseProxy
	logger *zap.Logger

	ticker    *time.Ticker
	done      chan bool
	lastEpoch uint64
}

type response struct {
	body    []byte
	status  int
	headers http.Header
}

func (r *response) Write(in []byte) (int, error) {
	r.body = append(r.body, in...)
	return len(in), nil
}

func (r *response) WriteHeader(statusCode int) {
	r.status = statusCode
}

func (r *response) Header() http.Header {
	return r.headers
}

type Finalities struct {
	Data struct {
		Finalized struct {
			Epoch string `json:"epoch"`
		} `json:"finalized"`
	} `json:"data"`
}

func (t *tickler) tickle() {
	// Query the upstream bn for the latest finalized epoch
	req, err := http.NewRequest("GET", "http://0.0.0.0:1234/eth/v1/beacon/states/head/finality_checkpoints", nil)
	if err != nil {
		t.logger.Warn("error getting finalized epoch in tickler", zap.Error(err))
		return
	}

	resp := &response{
		headers: make(http.Header),
	}

	t.proxy.ServeHTTP(resp, req)

	if resp.status != http.StatusOK {
		t.logger.Warn("http error tickling cache", zap.Int("status", resp.status))
		return
	}

	// parse resp.body as json
	finalities := &Finalities{}
	json.Unmarshal(resp.body, finalities)

	epoch, err := strconv.ParseUint(finalities.Data.Finalized.Epoch, 10, 64)
	if err != nil {
		t.logger.Warn("error parsing finalized epoch", zap.String("epoch", finalities.Data.Finalized.Epoch), zap.Error(err))
		return
	}

	if epoch <= t.lastEpoch {
		return
	}

	// Don't overwrite existing cached keys
	cached, err := t.c.Peek(finalities.Data.Finalized.Epoch)
	if err != nil {
		t.logger.Warn("error peeking at the cache", zap.Error(err))
		return
	}

	if cached {
		t.logger.Debug("tickler not overwriting cached value")
		return
	}

	url := fmt.Sprintf("http://0.0.0.0:1234/eth/v1/beacon/states/head/committees?epoch=%d", epoch)

	t.logger.Debug("Tickling the cache", zap.Uint64("epoch", epoch), zap.String("url", url))

	resp = &response{
		headers: make(http.Header),
	}

	req, err = http.NewRequest("GET", url, nil)
	if err != nil {
		t.logger.Warn("error tickling", zap.String("url", url), zap.Error(err))
		return
	}
	t.proxy.ServeHTTP(resp, req)

	if resp.status != http.StatusOK {
		t.logger.Warn("http error tickling cache", zap.Int("status", resp.status))
		return
	}

	if len(resp.body) == 0 {
		t.logger.Warn("tickler refusing to cache 0-byte response")
		return
	}
	// Save the result
	t.c.Set(finalities.Data.Finalized.Epoch, resp.body)
	t.lastEpoch = epoch
}

func (t *tickler) start() {
	t.ticker = time.NewTicker(3 * time.Minute)
	t.done = make(chan bool, 1)
	go func() {
		t.tickle()
		for {
			select {
			case <-t.done:
				return
			case <-t.ticker.C:
				t.tickle()
				continue
			}
		}
	}()
}

func (t *tickler) stop() {
	t.ticker.Stop()
	t.done <- true
}
