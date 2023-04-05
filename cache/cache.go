package cache

import "net/http"

type Cache interface {
	Get(key string) ([]byte, http.Header, error)
	Set(key string, value []byte) error
	Prune(n uint) (uint, error)
	Peek(key string) (bool, error)
}
