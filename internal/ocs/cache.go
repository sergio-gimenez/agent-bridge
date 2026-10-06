package ocs

import (
	"bufio"
	"bytes"
	"encoding/gob"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"sync"
	"syscall"
)

// Bump whenever a parser changes what it extracts, so stale records are thrown
// away instead of being served as if the new parser had produced them.
const cacheVersion = 3

type scannedFile struct {
	Path  string
	Size  int64
	ModNs int64
	Inode uint64
}

func (file scannedFile) ModMs() float64 {
	return float64(file.ModNs) / 1e6
}

func statFile(path string, entry fs.DirEntry) (scannedFile, bool) {
	info, err := entry.Info()
	if err != nil {
		return scannedFile{}, false
	}
	file := scannedFile{Path: path, Size: info.Size(), ModNs: info.ModTime().UnixNano()}
	if stat, ok := info.Sys().(*syscall.Stat_t); ok {
		file.Inode = uint64(stat.Ino)
	}
	return file, true
}

type cachedFile struct {
	Size  int64
	ModNs int64
	Inode uint64
	// Where the reader stopped. A file that holds nothing worth listing is
	// remembered too, so an empty transcript is not re-read on every launch.
	State *parseState
}

type cachedOpencode struct {
	TimeUpdated int64
	User        []string
	Assistant   []string
}

type cacheData struct {
	Version    int
	Files      map[string]cachedFile
	OpencodeDB string
	Opencode   map[string]cachedOpencode
}

// Cache remembers what each transcript parsed to, keyed by path and checked
// against size and mtime, so a launch only re-reads the sessions that changed
// since the last one.
type Cache struct {
	path string
	// previous is decoded in the background, overlapping the directory walks
	// and the OpenCode query; loaded closes once it is safe to read.
	previous cacheData
	loaded   chan struct{}
	mu       sync.Mutex
	next     cacheData
	dirty    bool
}

// last waits for the on-disk cache to finish loading, then returns it.
func (cache *Cache) last() *cacheData {
	<-cache.loaded
	return &cache.previous
}

func CachePath() string {
	if path := os.Getenv("AGB_CACHE_PATH"); path != "" {
		return path
	}
	if path := os.Getenv("OCS_CACHE_PATH"); path != "" {
		return path
	}
	dir, err := os.UserCacheDir()
	if err != nil {
		dir = filepath.Join(homeDir(), ".cache")
	}
	return preferredPath(
		filepath.Join(dir, "agentbridge", "index.gob"),
		filepath.Join(dir, "ocs", "index.gob"),
	)
}

func emptyCacheData() cacheData {
	return cacheData{
		Version:  cacheVersion,
		Files:    map[string]cachedFile{},
		Opencode: map[string]cachedOpencode{},
	}
}

// OpenCache loads the cache at path. A fresh cache ignores what is on disk and
// rebuilds it from scratch; an empty path gives one that is never written.
func OpenCache(path string, fresh bool) *Cache {
	cache := &Cache{
		path:     path,
		previous: emptyCacheData(),
		loaded:   make(chan struct{}),
		next:     emptyCacheData(),
		dirty:    fresh,
	}
	if path == "" || fresh {
		close(cache.loaded)
		return cache
	}
	go cache.load()
	return cache
}

func (cache *Cache) load() {
	defer close(cache.loaded)

	file, err := os.Open(cache.path)
	if err != nil {
		return
	}
	defer file.Close()

	var loaded cacheData
	if gob.NewDecoder(bufio.NewReader(file)).Decode(&loaded) != nil || loaded.Version != cacheVersion {
		cache.mu.Lock()
		cache.dirty = true
		cache.mu.Unlock()
		return
	}
	if loaded.Files == nil {
		loaded.Files = map[string]cachedFile{}
	}
	if loaded.Opencode == nil {
		loaded.Opencode = map[string]cachedOpencode{}
	}
	cache.previous = loaded
}

// Save writes the cache if anything changed. Files this run did not look at
// (another account's, say, with a different config) are kept while they still
// exist; files that are gone drop out. It writes to a temporary file and
// renames it into place, so a reader never sees half a cache.
func (cache *Cache) Save() error {
	previous := cache.last()
	cache.mu.Lock()
	defer cache.mu.Unlock()

	if cache.path == "" {
		return nil
	}
	for path, entry := range previous.Files {
		if _, seen := cache.next.Files[path]; seen {
			continue
		}
		if _, err := os.Stat(path); err == nil {
			cache.next.Files[path] = entry
		} else {
			cache.dirty = true
		}
	}
	if !cache.dirty {
		return nil
	}

	if err := os.MkdirAll(filepath.Dir(cache.path), 0o755); err != nil {
		return err
	}
	temp, err := os.CreateTemp(filepath.Dir(cache.path), ".index-*.gob")
	if err != nil {
		return err
	}
	defer os.Remove(temp.Name())

	writer := bufio.NewWriter(temp)
	if err := gob.NewEncoder(writer).Encode(cache.next); err != nil {
		temp.Close()
		return err
	}
	if err := writer.Flush(); err != nil {
		temp.Close()
		return err
	}
	if err := temp.Close(); err != nil {
		return err
	}
	return os.Rename(temp.Name(), cache.path)
}

// readRecord brings one file's parse up to date. An unchanged file costs
// nothing; a file that grew is read from where the last run stopped; anything
// else (new, shrunk, rewritten) is read from the start.
func readRecord(file scannedFile, cached *cachedFile, format transcriptFormat) (*parseState, *record, error) {
	if cached != nil && cached.State != nil && cached.Size == file.Size &&
		cached.ModNs == file.ModNs && cached.State.Offset == file.Size {
		return cached.State, format.record(cached.State), nil
	}

	handle, err := os.Open(file.Path)
	if err != nil {
		return nil, nil, err
	}
	defer handle.Close()

	state := &parseState{}
	if cached != nil && cached.State != nil && cached.Inode == file.Inode && appendedTo(handle, file, cached.State) {
		state = cached.State.clone()
	}
	if _, err := handle.Seek(state.Offset, io.SeekStart); err != nil {
		return nil, nil, err
	}

	state, rec, err := resume(handle, state, format)
	if err != nil {
		return nil, nil, err
	}
	if len(state.Head) == 0 && state.Offset > 0 {
		head := make([]byte, min(int64(tailSize), state.Offset))
		if _, err := handle.ReadAt(head, 0); err == nil {
			state.Head = head
		}
	}
	return state, rec, nil
}

// appendedTo is true when the file still starts with what was read last time,
// judged by its first bytes and the bytes just before the saved offset.
func appendedTo(handle *os.File, file scannedFile, state *parseState) bool {
	if state.Offset == 0 || file.Size < state.Offset || len(state.Head) == 0 || len(state.Tail) == 0 {
		return false
	}
	matches := func(want []byte, at int64) bool {
		got := make([]byte, len(want))
		_, err := handle.ReadAt(got, at)
		return err == nil && bytes.Equal(got, want)
	}
	return matches(state.Head, 0) && matches(state.Tail, state.Offset-int64(len(state.Tail)))
}

// records returns the parsed record for every file, in parallel. A file that
// cannot be read is skipped, as it would be without the cache.
func (cache *Cache) records(files []scannedFile, format transcriptFormat) []*record {
	results := make([]*record, len(files))
	previous := cache.last()

	jobs := make(chan int)
	var wg sync.WaitGroup
	for range min(runtime.NumCPU(), max(1, len(files))) {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := range jobs {
				file := files[i]
				var cached *cachedFile
				if entry, ok := previous.Files[file.Path]; ok {
					cached = &entry
				}
				state, rec, err := readRecord(file, cached, format)
				if err != nil {
					continue
				}
				results[i] = rec
				cache.keepFile(file.Path, cachedFile{Size: file.Size, ModNs: file.ModNs, Inode: file.Inode, State: state},
					cached == nil || cached.State != state)
			}
		}()
	}
	for i := range files {
		jobs <- i
	}
	close(jobs)
	wg.Wait()

	return results
}

func (cache *Cache) keepFile(path string, entry cachedFile, changed bool) {
	cache.mu.Lock()
	defer cache.mu.Unlock()
	cache.next.Files[path] = entry
	if changed {
		cache.dirty = true
	}
}
