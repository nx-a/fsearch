// Пакет lru реализует потокобезопасный LRU-кеш с учётом частоты. Сохраняются
// только ключи, запрошенные не менее MinHits раз, поэтому кеш хранит
// действительно частые запросы, а не разовые обращения.
package lru

import (
	"container/list"
	"sync"
)

type entry[K comparable, V any] struct {
	key K
	val V
}

// Cache — LRU-кеш с учётом частоты.
type Cache[K comparable, V any] struct {
	mu      sync.Mutex
	cap     int
	minHits int
	ll      *list.List
	items   map[K]*list.Element
	freq    map[K]int
	maxFreq int
}

// New создаёт кеш на capacity записей. Значение кешируется только после того,
// как ключ предъявлен minHits раз (minHits <= 1 кеширует сразу).
func New[K comparable, V any](capacity, minHits int) *Cache[K, V] {
	if capacity < 1 {
		capacity = 1
	}
	if minHits < 1 {
		minHits = 1
	}
	return &Cache[K, V]{
		cap:     capacity,
		minHits: minHits,
		ll:      list.New(),
		items:   make(map[K]*list.Element, capacity),
		freq:    make(map[K]int),
		maxFreq: capacity * 16,
	}
}

// Get возвращает кешированное значение для key, если оно есть.
func (c *Cache[K, V]) Get(key K) (V, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if el, ok := c.items[key]; ok {
		c.ll.MoveToFront(el)
		return el.Value.(*entry[K, V]).val, true
	}
	var zero V
	return zero, false
}

// Add регистрирует запрос по key с вычисленным значением. Значение сохраняется
// только после достижения key minHits запросов; иначе отслеживается только
// его частота. Если key уже в кеше, его значение обновляется.
func (c *Cache[K, V]) Add(key K, val V) {
	c.mu.Lock()
	defer c.mu.Unlock()

	if el, ok := c.items[key]; ok {
		el.Value.(*entry[K, V]).val = val
		c.ll.MoveToFront(el)
		return
	}

	c.freq[key]++
	if c.freq[key] < c.minHits {
		if len(c.freq) > c.maxFreq {
			c.freq = make(map[K]int)
		}
		return
	}

	el := c.ll.PushFront(&entry[K, V]{key: key, val: val})
	c.items[key] = el
	if c.ll.Len() > c.cap {
		c.evictOldest()
	}
}

func (c *Cache[K, V]) evictOldest() {
	el := c.ll.Back()
	if el == nil {
		return
	}
	c.ll.Remove(el)
	k := el.Value.(*entry[K, V]).key
	delete(c.items, k)
	delete(c.freq, k)
}

// Purge очищает все записи кеша и счётчики частоты.
func (c *Cache[K, V]) Purge() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.ll.Init()
	c.items = make(map[K]*list.Element, c.cap)
	c.freq = make(map[K]int)
}

// Len возвращает текущее количество кешированных записей.
func (c *Cache[K, V]) Len() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.ll.Len()
}
