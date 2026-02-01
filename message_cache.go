// Copyright 2026 Team 254. All Rights Reserved.
// Author: pat@patfairbank.com (Patrick Fairbank)
//
// In-memory cache for message deduplication.

package main

import "sync"

// MessageCache tracks seen messages by their unique identifier to prevent duplicate processing.
type MessageCache struct {
	seen map[string]bool
	mu   sync.Mutex
}

// NewMessageCache creates a new empty message cache.
func NewMessageCache() *MessageCache {
	return &MessageCache{
		seen: make(map[string]bool),
	}
}

// IsDuplicate returns true if the key has been seen before, otherwise adds the key and returns false.
func (cache *MessageCache) IsDuplicate(key string) bool {
	cache.mu.Lock()
	defer cache.mu.Unlock()

	if cache.seen[key] {
		return true
	}
	cache.seen[key] = true
	return false
}
