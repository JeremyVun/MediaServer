package watcher

import (
	"path/filepath"
	"sync"
)

type IgnoreSet struct {
	mu    sync.RWMutex
	paths map[string]int
}

func NewIgnoreSet() *IgnoreSet {
	return &IgnoreSet{paths: make(map[string]int)}
}

func (s *IgnoreSet) Add(path string) func() {
	if s == nil {
		return func() {}
	}
	path = filepath.Clean(path)
	s.mu.Lock()
	s.paths[path]++
	s.mu.Unlock()
	return func() { s.Remove(path) }
}

func (s *IgnoreSet) Remove(path string) {
	if s == nil {
		return
	}
	path = filepath.Clean(path)
	s.mu.Lock()
	defer s.mu.Unlock()
	if n := s.paths[path]; n > 1 {
		s.paths[path] = n - 1
		return
	}
	delete(s.paths, path)
}

func (s *IgnoreSet) Contains(path string) bool {
	if s == nil {
		return false
	}
	path = filepath.Clean(path)
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.paths[path] > 0
}
