package main

import (
	"bytes"
	"context"
	"flag"
	"fmt"
	"io/ioutil"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"os"
	"os/signal"
	"strconv"
	"sync"
	"time"

	"github.com/gorilla/mux"
	"github.com/jshufro/beacon-cache-proxy/cache"
	"go.uber.org/zap"
)

var logger *zap.Logger
var globalwg sync.WaitGroup
var c cache.Cache
var proxy *httputil.ReverseProxy
var cachingProxy *httputil.ReverseProxy

func initLogger(debug *bool) error {
	var cfg zap.Config
	var err error

	if *debug {
		cfg = zap.NewDevelopmentConfig()
	} else {
		cfg = zap.NewProductionConfig()
	}
	logger, err = cfg.Build()
	return err
}

func onCachingProxyResponse(resp *http.Response) error {
	vars := mux.Vars(resp.Request)
	serialized, err := ioutil.ReadAll(resp.Body)
	if err != nil {
		logger.Warn("Error reading response while caching", zap.Error(err))
		return nil
	}
	resp.Body = ioutil.NopCloser(bytes.NewReader(serialized))
	// Save the response asynchronously so the client is waiting
	defer func() {
		e, ok := vars["epoch"]
		if !ok {
			return
		}
		if _, err := strconv.ParseUint(e, 10, 64); err != nil {
			// Epoch is non-numeric, skip caching
			return
		}
		err := c.Set(e, serialized)
		if err != nil {
			logger.Warn("Error caching response", zap.Error(err))
		}
	}()
	return nil
}

func cachedQuery(w http.ResponseWriter, r *http.Request) {
	var cached []byte
	var err error
	var headers http.Header
	logger.Debug("Proxying committees query", zap.String("path", r.URL.String()))
	vars := mux.Vars(r)
	e, ok := vars["epoch"]
	if !ok {
		// Epoch not found, proxy request
		logger.Debug("no epoch parameter passed")
		goto pass
	}

	if _, err := strconv.ParseUint(e, 10, 64); err != nil {
		// Epoch is non-numeric, proxy request
		logger.Debug("non-numeric epoch parameter passed")
		goto pass
	}

	cached, headers, err = c.Get(e)
	if err != nil {
		logger.Warn("Error querying cache", zap.Error(err))
		goto pass
	}

	if cached != nil {
		// We already have the data for this, short-circuit the proxy
		for k, v := range headers {
			for _, vv := range v {
				w.Header().Set(k, vv)
			}
		}
		w.WriteHeader(http.StatusOK)
		w.Write(cached)
		logger.Debug("cache hit", zap.String("epoch", e), zap.String("Content-Type", w.Header().Get("Content-Type")))
		return
	}

	// We don't have the data, so proxy now
pass:
	cachingProxy.ServeHTTP(w, r)
}

func serve(listener net.Listener) http.Server {
	server := http.Server{}
	router := mux.NewRouter()

	router.Path("/eth/v1/beacon/states/head/committees").
		Queries("epoch", "{epoch}").
		HandlerFunc(cachedQuery)
	router.PathPrefix("/").HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		proxy.ServeHTTP(w, r)
	})

	http.Handle("/", router)

	globalwg.Add(1)
	go func() {
		if err := server.Serve(listener); err != nil {
			logger.Info("Stopping server", zap.Error(err))
			globalwg.Done()
		}
	}()
	return server
}

func prune(count uint) {
	if count == 0 {
		logger.Debug("pruning disabled, skipping")
		return
	}
	logger.Debug("Pruning started", zap.Uint("retain", count))
	deleted, err := c.Prune(count)
	if err != nil {
		logger.Warn("error pruning cache", zap.Error(err))
		return
	}

	logger.Debug("pruned cache", zap.Uint("files", deleted))
}

func main() {
	bnURLFlag := flag.String("bn-url", "", "URL to the beacon node to proxy, eg, http://0.0.0.0:5052")
	addrURLFlag := flag.String("addr", "127.0.0.1:55052", "Address for the proxy to listen on")
	dataDirFlag := flag.String("data-dir", "/tmp/treegen-proxy", "Path in which to save cache data")
	retain := flag.Uint("retain", 7000, "Number of epochs to retain in the cache")
	debug := flag.Bool("debug", false, "Whether to enable debug logging")
	conv := flag.String("conv", "", "File to convert to pb")

	flag.Parse()

	if *conv != "" {
		err := cache.Conv(*conv)
		if err != nil {
			panic(err)
		}
		return
	}

	if *bnURLFlag == "" {
		fmt.Fprintf(os.Stderr, "Invalid -bn-url:\n")
		flag.PrintDefaults()
		os.Exit(1)
	}

	if err := initLogger(debug); err != nil {
		fmt.Fprintf(os.Stderr, "Error initializing logger: %v\n", err)
		os.Exit(1)
	}
	defer logger.Sync()

	base, err := url.Parse(*bnURLFlag)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Invalid -bn-url: %v\n", err)
		os.Exit(1)
	}
	logger.Debug("Parsed -bn-url", zap.String("url", *bnURLFlag))

	// Init the cache
	c, err = cache.NewDiskCache(*dataDirFlag)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Unable to initialize cache: %v\n", err)
		os.Exit(1)
	}

	ticker := time.NewTicker(60 * time.Minute)
	done := make(chan bool, 1)
	// Set up the periodic prune
	go func() {
		// Prune on startup
		prune(*retain)
		for {
			select {
			case <-done:
				return
			case <-ticker.C:
				prune(*retain)
				continue
			}
		}
	}()

	// Set up the reverse proxy
	proxy = httputil.NewSingleHostReverseProxy(base)
	cachingProxy = httputil.NewSingleHostReverseProxy(base)
	cachingProxy.ModifyResponse = onCachingProxyResponse

	// Set up the webserver
	listener, err := net.Listen("tcp", *addrURLFlag)
	if err != nil {
		logger.Fatal("error starting webserver", zap.Error(err))
	}
	server := serve(listener)

	// Set up the tickler to keep the cache warm
	tickler := tickler{
		c:      c,
		proxy:  cachingProxy,
		logger: logger,
	}

	tickler.start()

	// Wait for signals
	c := make(chan os.Signal, 1)
	signal.Notify(c, os.Interrupt)
	<-c
	signal.Reset()
	close(c)
	ticker.Stop()
	done <- true
	close(done)
	tickler.stop()
	server.Shutdown(context.Background())
	listener.Close()
	globalwg.Wait()

	logger.Debug("Bye")
}
