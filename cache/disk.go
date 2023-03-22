package cache

import (
	"errors"
	"fmt"
	"io"
	"os"
	"sort"

	"github.com/klauspost/compress/zstd"
)

type diskCache struct {
	path string
}

func (d diskCache) fileName(key string) string {
	return d.path + "/" + key + ".zstd"
}

func (d diskCache) Peek(key string) (bool, error) {
	fileName := d.fileName(key)
	if _, err := os.Stat(fileName); err != nil {
		if os.IsNotExist(err) {
			return false, nil
		}

		return false, err
	}

	return true, nil
}

func (d diskCache) Get(key string) ([]byte, error) {
	fileName := d.fileName(key)
	if _, err := os.Stat(fileName); err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}

		return nil, err
	}

	f, err := os.Open(fileName)
	if err != nil {
		os.Remove(fileName)
		return nil, err
	}

	decoder, err := zstd.NewReader(f)
	if err != nil {
		os.Remove(fileName)
		return nil, err
	}

	return io.ReadAll(decoder)
}

func (d diskCache) Set(key string, value []byte) error {
	fileName := d.fileName(key)

	if _, err := os.Stat(fileName); !os.IsNotExist(err) {
		return fmt.Errorf("error while checking for file %s: %v", fileName, err)
	}

	f, err := os.Create(fileName)
	if err != nil {
		return fmt.Errorf("error creating cache file %s: %v", fileName, err)
	}
	defer f.Close()

	compressor, err := zstd.NewWriter(f, zstd.WithEncoderLevel(zstd.SpeedFastest))
	if err != nil {
		return errors.New("unable to create zstd compressor")
	}
	defer compressor.Close()

	_, err = compressor.Write(value)
	return err
}

// Delete everything except n-newest cached objects.
// Return the number of files deleted
func (d diskCache) Prune(n uint) (uint, error) {
	cacheDir, err := os.Open(d.path)
	if err != nil {
		return 0, err
	}

	stat, err := cacheDir.Stat()
	if err != nil {
		return 0, err
	}

	if !stat.IsDir() {
		return 0, fmt.Errorf("%s is not a directory", d.path)
	}

	dirents, err := cacheDir.ReadDir(-1)
	if err != nil {
		return 0, err
	}

	if uint(len(dirents)) <= n {
		return 0, nil
	}

	fileNames := make([]string, 0, len(dirents))
	for _, dirent := range dirents {
		fileNames = append(fileNames, dirent.Name())
	}

	sort.Strings(fileNames)

	// fileNames is now sorted oldest to newest.
	// grab the len()-n oldest.
	toDelete := fileNames[:uint(len(fileNames))-n+1]

	reclaimed := uint(0)
	for _, fileName := range toDelete {
		fullName := fmt.Sprintf("%s/%s", d.path, fileName)
		err := os.Remove(fullName)
		if err != nil {
			return 0, err
		}
		reclaimed += 1
	}

	return reclaimed, nil
}

func NewDiskCache(path string) (diskCache, error) {
	var d diskCache
	d.path = path

	// Check if the path exists
	_, err := os.Stat(path)
	if err == nil {
		return d, nil
	}

	if !os.IsNotExist(err) {
		return diskCache{}, err
	}

	if err := os.Mkdir(path, os.ModePerm); err != nil {
		return diskCache{}, err
	}

	return d, nil
}
