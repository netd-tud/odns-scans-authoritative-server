package server

import (
	"bytes"
	"fmt"
	"io/ioutil"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
)

type archiveCompressor struct {
	mu      sync.Mutex
	pending []string
	wake    chan struct{}
	done    chan struct{}
}

func startArchiveCompressor(diagnostics *log.Logger) *archiveCompressor {
	zstd, err := exec.LookPath("zstd")
	if err != nil {
		diagnostics.Printf("query log compression unavailable: %v", err)
		return nil
	}
	c := &archiveCompressor{wake: make(chan struct{}, 1), done: make(chan struct{})}
	go func() {
		defer close(c.done)
		for range c.wake {
			c.mu.Lock()
			paths := c.pending
			c.pending = nil
			c.mu.Unlock()
			for _, path := range paths {
				if err := compressArchive(zstd, path); err != nil {
					diagnostics.Printf("compress query archive %q: %v", path, err)
				}
			}
		}
	}()
	return c
}

func (c *archiveCompressor) enqueue(path string) {
	c.mu.Lock()
	c.pending = append(c.pending, path)
	c.mu.Unlock()
	select {
	case c.wake <- struct{}{}:
	default:
	}
}

func (c *archiveCompressor) close() {
	close(c.wake)
	<-c.done
}

func compressArchive(zstd, path string) error {
	source, err := os.Open(path)
	if err != nil {
		return err
	}
	defer source.Close()
	info, err := source.Stat()
	if err != nil {
		return err
	}

	temp, err := ioutil.TempFile(filepath.Dir(path), ".rr-mirror-zstd-*")
	if err != nil {
		return err
	}
	defer os.Remove(temp.Name())
	defer temp.Close()

	var stderr bytes.Buffer
	cmd := exec.Command(zstd, "-q", "-T1", "-c")
	cmd.Stdin, cmd.Stdout, cmd.Stderr = source, temp, &stderr
	if err := cmd.Run(); err != nil {
		if detail := strings.TrimSpace(stderr.String()); detail != "" {
			return fmt.Errorf("zstd: %w: %s", err, detail)
		}
		return fmt.Errorf("zstd: %w", err)
	}
	if err := source.Close(); err != nil {
		return err
	}
	if err := temp.Chmod(info.Mode().Perm()); err != nil {
		return err
	}
	if err := temp.Sync(); err != nil {
		return err
	}
	if err := temp.Close(); err != nil {
		return err
	}

	// Publish only a complete archive and leave any existing destination in place.
	if err := os.Link(temp.Name(), path+".zst"); err != nil {
		return err
	}
	return os.Remove(path)
}
