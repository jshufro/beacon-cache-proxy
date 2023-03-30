package cache

type Cache interface {
	Get(key string) ([]byte, error)
	Set(key string, value []byte) error
	Prune(n uint) (uint, error)
	Peek(key string) (bool, error)
}
