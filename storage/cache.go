package storage

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io/fs"
	"log"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"sync"
	"time"
)

const cacheBlockSize = 1 << 20

type BlockCache struct {
	dir      string
	maxBytes int64

	mu       sync.Mutex
	size     int64
	evicting bool
}

var Cache *BlockCache

func NewBlockCache(dir string, maxBytes int64) (*BlockCache, error) {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}
	c := &BlockCache{dir: dir, maxBytes: maxBytes}
	err := filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		if info, err := d.Info(); err == nil {
			c.size += info.Size()
		}
		return nil
	})
	return c, err
}

func (c *BlockCache) blockPath(key string, block int64) string {
	sum := sha256.Sum256([]byte(key))
	name := hex.EncodeToString(sum[:])
	return filepath.Join(c.dir, name[:2], name+"-"+strconv.FormatInt(block, 10))
}

func (c *BlockCache) block(ctx context.Context, b S3Bucket, key string, block int64) ([]byte, error) {
	path := c.blockPath(key, block)
	if data, err := os.ReadFile(path); err == nil {
		now := time.Now()
		os.Chtimes(path, now, now)
		return data, nil
	}
	data, err := b.GetRange(ctx, key, block*cacheBlockSize, cacheBlockSize)
	if err != nil {
		return nil, err
	}
	c.store(path, data)
	return data, nil
}

func (c *BlockCache) store(path string, data []byte) {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		log.Println("cache:", err)
		return
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".tmp-*")
	if err != nil {
		log.Println("cache:", err)
		return
	}
	_, werr := tmp.Write(data)
	cerr := tmp.Close()
	if werr != nil || cerr != nil {
		os.Remove(tmp.Name())
		return
	}
	if err := os.Rename(tmp.Name(), path); err != nil {
		os.Remove(tmp.Name())
		return
	}
	c.mu.Lock()
	c.size += int64(len(data))
	over := c.size > c.maxBytes && !c.evicting
	if over {
		c.evicting = true
	}
	c.mu.Unlock()
	if over {
		go c.evict()
	}
}

func (c *BlockCache) evict() {
	defer func() {
		c.mu.Lock()
		c.evicting = false
		c.mu.Unlock()
	}()
	type entry struct {
		path string
		size int64
		mod  time.Time
	}
	var entries []entry
	var total int64
	filepath.WalkDir(c.dir, func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return nil
		}
		if info, err := d.Info(); err == nil {
			entries = append(entries, entry{path, info.Size(), info.ModTime()})
			total += info.Size()
		}
		return nil
	})
	sort.Slice(entries, func(i, j int) bool { return entries[i].mod.Before(entries[j].mod) })
	target := c.maxBytes * 9 / 10
	for _, e := range entries {
		if total <= target {
			break
		}
		if os.Remove(e.path) == nil {
			total -= e.size
		}
	}
	c.mu.Lock()
	c.size = total
	c.mu.Unlock()
}

func (c *BlockCache) ReadRange(ctx context.Context, b S3Bucket, key string, off, n int64) ([]byte, error) {
	out := make([]byte, 0, n)
	for pos := off; pos < off+n; {
		block := pos / cacheBlockSize
		data, err := c.block(ctx, b, key, block)
		if err != nil {
			return nil, err
		}
		start := pos - block*cacheBlockSize
		if start >= int64(len(data)) {
			return nil, fmt.Errorf("cache: read past end of %s", key)
		}
		end := min(int64(len(data)), start+(off+n-pos))
		out = append(out, data[start:end]...)
		pos += end - start
	}
	return out, nil
}

func ReadRange(ctx context.Context, key string, off, n int64) ([]byte, error) {
	data, err := readRange(ctx, FilesBucket, key, off, n)
	if err != nil && Replicated() && replicaClass != GlacierClass {
		if copied, rerr := readRange(ctx, ReplicaBucket, key, off, n); rerr == nil {
			return copied, nil
		}
	}
	return data, err
}

func readRange(ctx context.Context, b S3Bucket, key string, off, n int64) ([]byte, error) {
	if Cache != nil {
		return Cache.ReadRange(ctx, b, key, off, n)
	}
	return b.GetRange(ctx, key, off, n)
}

func (c *BlockCache) Forget(key string) {
	sum := sha256.Sum256([]byte(key))
	name := hex.EncodeToString(sum[:])
	blocks, _ := filepath.Glob(filepath.Join(c.dir, name[:2], name+"-*"))
	var freed int64
	for _, path := range blocks {
		if info, err := os.Stat(path); err == nil && os.Remove(path) == nil {
			freed += info.Size()
		}
	}
	c.mu.Lock()
	c.size -= freed
	c.mu.Unlock()
}

func Delete(ctx context.Context, key string) error {
	if Cache != nil {
		Cache.Forget(key)
	}
	return FilesBucket.EraseAll(ctx, key)
}
