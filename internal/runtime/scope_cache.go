// Copyright (c) 2026 OceanBase.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
// http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package runtime

import (
	"container/list"
	"context"
	"errors"
	"sync"
)

const DefaultScopeCacheSize = 128

type (
	ScopeCacheObserver func(cached, active int)
	ScopeEvictor       func(scopeID string)
)

type RuntimeOptions struct {
	ScopeCacheSize int
	ScopeEvictor   ScopeEvictor
	ScopeObserver  ScopeCacheObserver
	Tracing        StageTracing
}

type scopeEntry struct {
	key       string
	semaphore semaphore
	leases    int
	recency   *list.Element
}

type scopeCache struct {
	mu       sync.Mutex
	capacity int
	entries  map[string]*scopeEntry
	recency  list.List
	evictor  ScopeEvictor
	observer ScopeCacheObserver
}

type scopeLease struct {
	cache *scopeCache
	entry *scopeEntry
}

func newScopeCache(capacity int, evictor ScopeEvictor, observer ScopeCacheObserver) *scopeCache {
	cache := &scopeCache{
		capacity: capacity, entries: make(map[string]*scopeEntry),
		evictor: evictor, observer: observer,
	}
	cache.observe(0, 0)
	return cache
}

func (c *scopeCache) lease(key string) (*scopeLease, func()) {
	c.mu.Lock()
	entry := c.entries[key]
	var evicted []string
	if entry == nil {
		evicted = c.makeRoomLocked()
		entry = &scopeEntry{key: key, semaphore: newSemaphore()}
		entry.recency = c.recency.PushBack(entry)
		c.entries[key] = entry
	}
	entry.leases++
	c.recency.MoveToBack(entry.recency)
	cached, active := c.countsLocked()
	c.mu.Unlock()
	c.notifyEvictions(evicted)
	c.observe(cached, active)

	lease := &scopeLease{cache: c, entry: entry}
	var once sync.Once
	return lease, func() { once.Do(func() { c.release(entry) }) }
}

func (c *scopeCache) acquire(ctx context.Context, key string) (func(), error) {
	lease, releaseLease := c.lease(key)
	releaseToken, err := lease.acquire(ctx)
	if err != nil {
		releaseLease()
		return nil, err
	}
	var once sync.Once
	return func() {
		once.Do(func() {
			releaseToken()
			releaseLease()
		})
	}, nil
}

func (l *scopeLease) contended() bool {
	return l != nil && l.entry != nil && len(l.entry.semaphore.token) == 0
}

func (l *scopeLease) acquire(ctx context.Context) (func(), error) {
	if l == nil || l.entry == nil {
		return nil, errors.New("runtime: scope cache lease is invalid")
	}
	return l.entry.semaphore.acquire(ctx)
}

func (c *scopeCache) release(entry *scopeEntry) {
	c.mu.Lock()
	current := c.entries[entry.key]
	if current != entry || entry.leases < 1 {
		c.mu.Unlock()
		panic("runtime: scope cache lease invariant violated")
	}
	entry.leases--
	c.recency.MoveToBack(entry.recency)
	evicted := c.trimLocked()
	cached, active := c.countsLocked()
	c.mu.Unlock()
	c.notifyEvictions(evicted)
	c.observe(cached, active)
}

func (c *scopeCache) clear() error {
	c.mu.Lock()
	for _, entry := range c.entries {
		if entry.leases > 0 {
			c.mu.Unlock()
			return errors.New("runtime: cannot clear active scope cache entries")
		}
	}
	evicted := make([]string, 0, len(c.entries))
	for element := c.recency.Front(); element != nil; element = element.Next() {
		evicted = append(evicted, element.Value.(*scopeEntry).key)
	}
	c.entries = make(map[string]*scopeEntry)
	c.recency.Init()
	c.mu.Unlock()
	c.notifyEvictions(evicted)
	c.observe(0, 0)
	return nil
}

func (c *scopeCache) makeRoomLocked() []string {
	var evicted []string
	for len(c.entries) >= c.capacity {
		entry := c.oldestInactiveLocked()
		if entry == nil {
			break
		}
		evicted = append(evicted, c.evictLocked(entry))
	}
	return evicted
}

func (c *scopeCache) trimLocked() []string {
	var evicted []string
	for len(c.entries) > c.capacity {
		entry := c.oldestInactiveLocked()
		if entry == nil {
			break
		}
		evicted = append(evicted, c.evictLocked(entry))
	}
	return evicted
}

func (c *scopeCache) oldestInactiveLocked() *scopeEntry {
	for element := c.recency.Front(); element != nil; element = element.Next() {
		entry := element.Value.(*scopeEntry)
		if entry.leases == 0 {
			return entry
		}
	}
	return nil
}

func (c *scopeCache) evictLocked(entry *scopeEntry) string {
	delete(c.entries, entry.key)
	c.recency.Remove(entry.recency)
	return entry.key
}

func (c *scopeCache) countsLocked() (cached, active int) {
	for _, entry := range c.entries {
		if entry.leases == 0 {
			cached++
		} else {
			active++
		}
	}
	return cached, active
}

func (c *scopeCache) notifyEvictions(scopeIDs []string) {
	if c.evictor == nil {
		return
	}
	for _, scopeID := range scopeIDs {
		func() {
			defer func() { _ = recover() }()
			c.evictor(scopeID)
		}()
	}
}

func (c *scopeCache) observe(cached, active int) {
	if c.observer == nil {
		return
	}
	defer func() { _ = recover() }()
	c.observer(cached, active)
}
