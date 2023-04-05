package cache

import (
	"fmt"
	"io"
	"net/http"
	"os"
	"sort"
	"strings"

	"github.com/jshufro/beacon-cache-proxy/cache/pb"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"
)

var protoJsonOpts protojson.MarshalOptions = protojson.MarshalOptions{
	EmitUnpopulated: true,
	UseProtoNames:   true,
}

type diskCache struct {
	path string
}

func (d diskCache) fileName(key string) string {
	return d.path + "/" + key + ".pb"
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

func (d diskCache) Get(key string) ([]byte, http.Header, error) {
	fileName := d.fileName(key)
	if _, err := os.Stat(fileName); err != nil {
		if os.IsNotExist(err) {
			return nil, nil, nil
		}

		return nil, nil, err
	}

	f, err := os.Open(fileName)
	if err != nil {
		os.Remove(fileName)
		return nil, nil, err
	}
	defer f.Close()

	// Parse protobuf
	pbData, err := io.ReadAll(f)
	if err != nil {
		os.Remove(fileName)
		return nil, nil, fmt.Errorf("Error reading cached file %s: %w", fileName, err)
	}

	headers := make(map[string][]string)
	headers["Content-Type"] = []string{
		"application/protobuf",
	}

	return pbData, headers, nil
}

func (d diskCache) Set(key string, value []byte) error {
	fileName := d.fileName(key)

	if _, err := os.Stat(fileName); !os.IsNotExist(err) {
		return fmt.Errorf("error while checking for file %s: %w", fileName, err)
	}

	f, err := os.Create(fileName)
	if err != nil {
		return fmt.Errorf("error creating cache file %s: %w", fileName, err)
	}
	defer f.Close()

	// Serialize proto
	m := pb.CommitteesResponse{}
	err = protojson.Unmarshal(value, &m)
	if err != nil {
		return fmt.Errorf("error converting json to protobuf for file %s: %w", fileName, err)
	}

	bytes, err := proto.Marshal(&m)
	if err != nil {
		return fmt.Errorf("error marshalling protobuf for file %s: %w", fileName, err)
	}

	_, err = f.Write(bytes)
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

func Conv(file string) error {
	stat, err := os.Stat(file)
	if err != nil {

		return err
	}

	f, err := os.Open(file)
	if err != nil {
		return err
	}
	defer f.Close()

	file = strings.TrimSuffix(file, ".bin")

	// Convert json to proto
	outFile, err := os.OpenFile(file+".pb", os.O_CREATE|os.O_RDWR, stat.Mode())
	if err != nil {
		return err
	}
	defer outFile.Close()

	value, err := io.ReadAll(f)
	if err != nil {
		return err
	}

	m := pb.CommitteesResponse{}
	err = protojson.Unmarshal(value, &m)
	if err != nil {
		return fmt.Errorf("error converting json to protobuf for file %s: %w", file, err)
	}

	bytes, err := proto.Marshal(&m)
	if err != nil {
		return fmt.Errorf("error marshalling protobuf for file %s: %w", file, err)
	}

	_, err = outFile.Write(bytes)
	return err
}
