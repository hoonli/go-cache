package cache

import (
	"encoding/gob"
	"fmt"
	"io"
	"os"
	"runtime"
	"sync"
	"time"
)

type Item struct {
	Object     interface{}
	Expiration int64
}

// Returns true if the item has expired. 过期返回true
func (item Item) Expired() bool {
	if item.Expiration == 0 {
		return false
	}
	//如果 左>右 返回true，否则返回false  当前时间比过期时间大
	return time.Now().UnixNano() > item.Expiration
}

const (
	// For use with functions that take an expiration time.用于需要过期时间的函数
	NoExpiration time.Duration = -1
	// For use with functions that take an expiration time. Equivalent to
	// passing in the same expiration duration as was given to New() or
	// NewFrom() when the cache was created (e.g. 5 minutes.)
	//当创建缓存调用New、NewFrom 时传入相同的参数时会
	DefaultExpiration time.Duration = 0
)

type Cache struct {
	*cache
	// If this is confusing, see the comment at the bottom of New()
}

type cache struct {
	defaultExpiration time.Duration
	items             map[string]Item
	mu                sync.RWMutex //锁
	onEvicted         func(string, interface{})
	janitor           *janitor
}

// Add an item to the cache, replacing any existing item. If the duration is 0
// (DefaultExpiration), the cache's default expiration time is used. If it is -1
// (NoExpiration), the item never expires.

//添加项目到缓存，替换任何现有的项目，如果缓存时间为0 ，使用默认缓存，如果是-1，则永不过期
func (c *cache) Set(k string, x interface{}, d time.Duration) {
	// "Inlining" of set
	var e int64
	if d == DefaultExpiration {
		d = c.defaultExpiration
	}
	if d > 0 {
		e = time.Now().Add(d).UnixNano()
	}
	c.mu.Lock()
	c.items[k] = Item{
		Object:     x,
		Expiration: e,
	}
	// TODO: Calls to mu.Unlock are currently not deferred because defer
	// adds ~200 ns (as of go1.)

	//对当前 mu.unlock 的调用没有推迟因为defer

	c.mu.Unlock()
}

//上面大写，这个小写
func (c *cache) set(k string, x interface{}, d time.Duration) {
	var e int64
	if d == DefaultExpiration {
		d = c.defaultExpiration
	}
	if d > 0 {
		e = time.Now().Add(d).UnixNano()
	}
	c.items[k] = Item{
		Object:     x,
		Expiration: e,
	}
}

// Add an item to the cache, replacing any existing item, using the default
// expiration.

//使用默认值增加缓存项，代替已经任何存在的项目
func (c *cache) SetDefault(k string, x interface{}) {
	c.Set(k, x, DefaultExpiration)
}

// Add an item to the cache only if an item doesn't already exist for the given
//当给的这个项目不存在时，才会把这项加入缓存
// key, or if the existing item has expired. Returns an error otherwise.
//除非存在的项过期了，否则返回错误
func (c *cache) Add(k string, x interface{}, d time.Duration) error {
	c.mu.Lock()
	_, found := c.get(k)
	//如果存在
	if found {
		c.mu.Unlock()
		return fmt.Errorf("Item %s already exists", k)
	}
	c.set(k, x, d)
	c.mu.Unlock()
	return nil
}

// Set a new value for the cache key only if it already exists, and the existing
// item hasn't expired. Returns an error otherwise.

//当key存在且没有过期的时候，会设置一个新的缓存之，否则返回要给错误
func (c *cache) Replace(k string, x interface{}, d time.Duration) error {
	c.mu.Lock()
	_, found := c.get(k)
	if !found {
		c.mu.Unlock()
		return fmt.Errorf("Item %s doesn't exist", k)
	}
	//TODO:为什么没有看有没有过期
	c.set(k, x, d)
	c.mu.Unlock()
	return nil
}

// Get an item from the cache. Returns the item or nil, and a bool indicating
// whether the key was found.
func (c *cache) Get(k string) (interface{}, bool) {
	c.mu.RLock()
	// "Inlining" of get and Expired
	item, found := c.items[k]
	//如果不存在
	if !found {
		c.mu.RUnlock()
		return nil, false
	}
	//如果过期时间大于0（没有过期）
	if item.Expiration > 0 {
		if time.Now().UnixNano() > item.Expiration {
			c.mu.RUnlock()
			return nil, false
		}
	}
	c.mu.RUnlock()
	return item.Object, true
}

// GetWithExpiration returns an item and its expiration time from the cache.
// It returns the item or nil, the expiration time if one is set (if the item
// never expires a zero value for time.Time is returned), and a bool indicating
// whether the key was found.
//返回缓存值、返回过期时间，
func (c *cache) GetWithExpiration(k string) (interface{}, time.Time, bool) {
	c.mu.RLock()
	// "Inlining" of get and Expired
	item, found := c.items[k]
	if !found {
		c.mu.RUnlock()
		//不存在返回空、空时间，false
		return nil, time.Time{}, false
	}

	//如果过期时间大于0，且当前时间过期了，返回nil，空时间，false
	if item.Expiration > 0 {
		if time.Now().UnixNano() > item.Expiration {
			c.mu.RUnlock()
			return nil, time.Time{}, false
		}

		//如果过期时间大于0，且当前时间没过期，返回object，空时间，true
		// Return the item and the expiration time
		c.mu.RUnlock()
		return item.Object, time.Unix(0, item.Expiration), true
	}

	// If expiration <= 0 (i.e. no expiration time set) then return the item
	// and a zeroed time.Time
	//这个情况就是永不过期的情况
	c.mu.RUnlock()
	return item.Object, time.Time{}, true
}

//拿缓存，不存在返回false，存在，且过期，返回false，存在且没过期返回值，true
func (c *cache) get(k string) (interface{}, bool) {
	item, found := c.items[k]
	if !found {
		return nil, false
	}
	// "Inlining" of Expired
	if item.Expiration > 0 {
		if time.Now().UnixNano() > item.Expiration {
			return nil, false
		}
	}
	return item.Object, true
}

//Increment an item of type int, int8, int16, int32, int64, uintptr, uint,
//uint8, uint32, or uint64, float32 or float64 by n. Returns an error if the
//item's value is not an integer, if it was not found, or if it is not
//possible to increment it by n. To retrieve the incremented value, use one
//of the specialized methods, e.g. IncrementInt64.
func (c *cache) Increment(k string, n int64) error {
	c.mu.Lock()
	v, found := c.items[k]
	//如果不存在或者已经过期
	if !found || v.Expired() {
		c.mu.Unlock()
		return fmt.Errorf("Item %s not found", k)
	}

	switch v.Object.(type) {
	case int:
		v.Object = v.Object.(int) + int(n)
	case int8:
		v.Object = v.Object.(int8) + int8(n)
	case int16:
		v.Object = v.Object.(int16) + int16(n)
	case int32:
		v.Object = v.Object.(int32) + int32(n)
	case int64:
		v.Object = v.Object.(int64) + n
	case uint:
		v.Object = v.Object.(uint) + uint(n)
	case uintptr:
		v.Object = v.Object.(uintptr) + uintptr(n)
	case uint8:
		v.Object = v.Object.(uint8) + uint8(n)
	case uint16:
		v.Object = v.Object.(uint16) + uint16(n)
	case uint32:
		v.Object = v.Object.(uint32) + uint32(n)
	case uint64:
		v.Object = v.Object.(uint64) + uint64(n)
	case float32:
		v.Object = v.Object.(float32) + float32(n)
	case float64:
		v.Object = v.Object.(float64) + float64(n)
	default:
		c.mu.Unlock()
		return fmt.Errorf("The value for %s is not an integer", k)
	}
	c.items[k] = v
	c.mu.Unlock()
	return nil
}

// Increment an item of type float32 or float64 by n. Returns an error if the
// item's value is not floating point, if it was not found, or if it is not
// possible to increment it by n. Pass a negative number to decrement the
// value. To retrieve the incremented value, use one of the specialized methods,
// e.g. IncrementFloat64.
func (c *cache) IncrementFloat(k string, n float64) error {
	c.mu.Lock()
	v, found := c.items[k]
	if !found || v.Expired() {
		c.mu.Unlock()
		return fmt.Errorf("Item %s not found", k)
	}
	switch v.Object.(type) {
	case float32:
		v.Object = v.Object.(float32) + float32(n)
	case float64:
		v.Object = v.Object.(float64) + n
	default:
		c.mu.Unlock()
		return fmt.Errorf("The value for %s does not have type float32 or float64", k)
	}
	c.items[k] = v
	c.mu.Unlock()
	return nil
}

// Increment an item of type int by n. Returns an error if the item's value is
// not an int, or if it was not found. If there is no error, the incremented
// value is returned.
func (c *cache) IncrementInt(k string, n int) (int, error) {
	c.mu.Lock()
	v, found := c.items[k]
	if !found || v.Expired() {
		c.mu.Unlock()
		return 0, fmt.Errorf("Item %s not found", k)
	}
	rv, ok := v.Object.(int)
	if !ok {
		c.mu.Unlock()
		return 0, fmt.Errorf("The value for %s is not an int", k)
	}
	nv := rv + n
	v.Object = nv
	c.items[k] = v
	c.mu.Unlock()
	return nv, nil
}

// Increment an item of type int8 by n. Returns an error if the item's value is
// not an int8, or if it was not found. If there is no error, the incremented
// value is returned.
func (c *cache) IncrementInt8(k string, n int8) (int8, error) {
	c.mu.Lock()
	v, found := c.items[k]
	if !found || v.Expired() {
		c.mu.Unlock()
		return 0, fmt.Errorf("Item %s not found", k)
	}
	rv, ok := v.Object.(int8)
	if !ok {
		c.mu.Unlock()
		return 0, fmt.Errorf("The value for %s is not an int8", k)
	}
	nv := rv + n
	v.Object = nv
	c.items[k] = v
	c.mu.Unlock()
	return nv, nil
}

// Increment an item of type int16 by n. Returns an error if the item's value is
// not an int16, or if it was not found. If there is no error, the incremented
// value is returned.
func (c *cache) IncrementInt16(k string, n int16) (int16, error) {
	c.mu.Lock()
	v, found := c.items[k]
	if !found || v.Expired() {
		c.mu.Unlock()
		return 0, fmt.Errorf("Item %s not found", k)
	}
	rv, ok := v.Object.(int16)
	if !ok {
		c.mu.Unlock()
		return 0, fmt.Errorf("The value for %s is not an int16", k)
	}
	nv := rv + n
	v.Object = nv
	c.items[k] = v
	c.mu.Unlock()
	return nv, nil
}

// Increment an item of type int32 by n. Returns an error if the item's value is
// not an int32, or if it was not found. If there is no error, the incremented
// value is returned.
func (c *cache) IncrementInt32(k string, n int32) (int32, error) {
	c.mu.Lock()
	v, found := c.items[k]
	if !found || v.Expired() {
		c.mu.Unlock()
		return 0, fmt.Errorf("Item %s not found", k)
	}
	rv, ok := v.Object.(int32)
	if !ok {
		c.mu.Unlock()
		return 0, fmt.Errorf("The value for %s is not an int32", k)
	}
	nv := rv + n
	v.Object = nv
	c.items[k] = v
	c.mu.Unlock()
	return nv, nil
}

// Increment an item of type int64 by n. Returns an error if the item's value is
// not an int64, or if it was not found. If there is no error, the incremented
// value is returned.
func (c *cache) IncrementInt64(k string, n int64) (int64, error) {
	c.mu.Lock()
	v, found := c.items[k]
	if !found || v.Expired() {
		c.mu.Unlock()
		return 0, fmt.Errorf("Item %s not found", k)
	}
	rv, ok := v.Object.(int64)
	if !ok {
		c.mu.Unlock()
		return 0, fmt.Errorf("The value for %s is not an int64", k)
	}
	nv := rv + n
	v.Object = nv
	c.items[k] = v
	c.mu.Unlock()
	return nv, nil
}

// Increment an item of type uint by n. Returns an error if the item's value is
// not an uint, or if it was not found. If there is no error, the incremented
// value is returned.
func (c *cache) IncrementUint(k string, n uint) (uint, error) {
	c.mu.Lock()
	v, found := c.items[k]
	if !found || v.Expired() {
		c.mu.Unlock()
		return 0, fmt.Errorf("Item %s not found", k)
	}
	rv, ok := v.Object.(uint)
	if !ok {
		c.mu.Unlock()
		return 0, fmt.Errorf("The value for %s is not an uint", k)
	}
	nv := rv + n
	v.Object = nv
	c.items[k] = v
	c.mu.Unlock()
	return nv, nil
}

// Increment an item of type uintptr by n. Returns an error if the item's value
// is not an uintptr, or if it was not found. If there is no error, the
// incremented value is returned.
func (c *cache) IncrementUintptr(k string, n uintptr) (uintptr, error) {
	c.mu.Lock()
	v, found := c.items[k]
	if !found || v.Expired() {
		c.mu.Unlock()
		return 0, fmt.Errorf("Item %s not found", k)
	}
	rv, ok := v.Object.(uintptr)
	if !ok {
		c.mu.Unlock()
		return 0, fmt.Errorf("The value for %s is not an uintptr", k)
	}
	nv := rv + n
	v.Object = nv
	c.items[k] = v
	c.mu.Unlock()
	return nv, nil
}

// Increment an item of type uint8 by n. Returns an error if the item's value
// is not an uint8, or if it was not found. If there is no error, the
// incremented value is returned.
func (c *cache) IncrementUint8(k string, n uint8) (uint8, error) {
	c.mu.Lock()
	v, found := c.items[k]
	if !found || v.Expired() {
		c.mu.Unlock()
		return 0, fmt.Errorf("Item %s not found", k)
	}
	rv, ok := v.Object.(uint8)
	if !ok {
		c.mu.Unlock()
		return 0, fmt.Errorf("The value for %s is not an uint8", k)
	}
	nv := rv + n
	v.Object = nv
	c.items[k] = v
	c.mu.Unlock()
	return nv, nil
}

// Increment an item of type uint16 by n. Returns an error if the item's value
// is not an uint16, or if it was not found. If there is no error, the
// incremented value is returned.
func (c *cache) IncrementUint16(k string, n uint16) (uint16, error) {
	c.mu.Lock()
	v, found := c.items[k]
	if !found || v.Expired() {
		c.mu.Unlock()
		return 0, fmt.Errorf("Item %s not found", k)
	}
	rv, ok := v.Object.(uint16)
	if !ok {
		c.mu.Unlock()
		return 0, fmt.Errorf("The value for %s is not an uint16", k)
	}
	nv := rv + n
	v.Object = nv
	c.items[k] = v
	c.mu.Unlock()
	return nv, nil
}

// Increment an item of type uint32 by n. Returns an error if the item's value
// is not an uint32, or if it was not found. If there is no error, the
// incremented value is returned.
func (c *cache) IncrementUint32(k string, n uint32) (uint32, error) {
	c.mu.Lock()
	v, found := c.items[k]
	if !found || v.Expired() {
		c.mu.Unlock()
		return 0, fmt.Errorf("Item %s not found", k)
	}
	rv, ok := v.Object.(uint32)
	if !ok {
		c.mu.Unlock()
		return 0, fmt.Errorf("The value for %s is not an uint32", k)
	}
	nv := rv + n
	v.Object = nv
	c.items[k] = v
	c.mu.Unlock()
	return nv, nil
}

// Increment an item of type uint64 by n. Returns an error if the item's value
// is not an uint64, or if it was not found. If there is no error, the
// incremented value is returned.
func (c *cache) IncrementUint64(k string, n uint64) (uint64, error) {
	c.mu.Lock()
	v, found := c.items[k]
	if !found || v.Expired() {
		c.mu.Unlock()
		return 0, fmt.Errorf("Item %s not found", k)
	}
	rv, ok := v.Object.(uint64)
	if !ok {
		c.mu.Unlock()
		return 0, fmt.Errorf("The value for %s is not an uint64", k)
	}
	nv := rv + n
	v.Object = nv
	c.items[k] = v
	c.mu.Unlock()
	return nv, nil
}

// Increment an item of type float32 by n. Returns an error if the item's value
// is not an float32, or if it was not found. If there is no error, the
// incremented value is returned.
func (c *cache) IncrementFloat32(k string, n float32) (float32, error) {
	c.mu.Lock()
	v, found := c.items[k]
	if !found || v.Expired() {
		c.mu.Unlock()
		return 0, fmt.Errorf("Item %s not found", k)
	}
	rv, ok := v.Object.(float32)
	if !ok {
		c.mu.Unlock()
		return 0, fmt.Errorf("The value for %s is not an float32", k)
	}
	nv := rv + n
	v.Object = nv
	c.items[k] = v
	c.mu.Unlock()
	return nv, nil
}

// Increment an item of type float64 by n. Returns an error if the item's value
// is not an float64, or if it was not found. If there is no error, the
// incremented value is returned.
func (c *cache) IncrementFloat64(k string, n float64) (float64, error) {
	c.mu.Lock()
	v, found := c.items[k]
	if !found || v.Expired() {
		c.mu.Unlock()
		return 0, fmt.Errorf("Item %s not found", k)
	}
	rv, ok := v.Object.(float64)
	if !ok {
		c.mu.Unlock()
		return 0, fmt.Errorf("The value for %s is not an float64", k)
	}
	nv := rv + n
	v.Object = nv
	c.items[k] = v
	c.mu.Unlock()
	return nv, nil
}

// Decrement an item of type int, int8, int16, int32, int64, uintptr, uint,
// uint8, uint32, or uint64, float32 or float64 by n. Returns an error if the
// item's value is not an integer, if it was not found, or if it is not
// possible to decrement it by n. To retrieve the decremented value, use one
// of the specialized methods, e.g. DecrementInt64.
func (c *cache) Decrement(k string, n int64) error {
	// TODO: Implement Increment and Decrement more cleanly.
	// (Cannot do Increment(k, n*-1) for uints.)
	c.mu.Lock()
	v, found := c.items[k]
	if !found || v.Expired() {
		c.mu.Unlock()
		return fmt.Errorf("Item not found")
	}
	switch v.Object.(type) {
	case int:
		v.Object = v.Object.(int) - int(n)
	case int8:
		v.Object = v.Object.(int8) - int8(n)
	case int16:
		v.Object = v.Object.(int16) - int16(n)
	case int32:
		v.Object = v.Object.(int32) - int32(n)
	case int64:
		v.Object = v.Object.(int64) - n
	case uint:
		v.Object = v.Object.(uint) - uint(n)
	case uintptr:
		v.Object = v.Object.(uintptr) - uintptr(n)
	case uint8:
		v.Object = v.Object.(uint8) - uint8(n)
	case uint16:
		v.Object = v.Object.(uint16) - uint16(n)
	case uint32:
		v.Object = v.Object.(uint32) - uint32(n)
	case uint64:
		v.Object = v.Object.(uint64) - uint64(n)
	case float32:
		v.Object = v.Object.(float32) - float32(n)
	case float64:
		v.Object = v.Object.(float64) - float64(n)
	default:
		c.mu.Unlock()
		return fmt.Errorf("The value for %s is not an integer", k)
	}
	c.items[k] = v
	c.mu.Unlock()
	return nil
}

// Decrement an item of type float32 or float64 by n. Returns an error if the
// item's value is not floating point, if it was not found, or if it is not
// possible to decrement it by n. Pass a negative number to decrement the
// value. To retrieve the decremented value, use one of the specialized methods,
// e.g. DecrementFloat64.
func (c *cache) DecrementFloat(k string, n float64) error {
	c.mu.Lock()
	v, found := c.items[k]
	if !found || v.Expired() {
		c.mu.Unlock()
		return fmt.Errorf("Item %s not found", k)
	}
	switch v.Object.(type) {
	case float32:
		v.Object = v.Object.(float32) - float32(n)
	case float64:
		v.Object = v.Object.(float64) - n
	default:
		c.mu.Unlock()
		return fmt.Errorf("The value for %s does not have type float32 or float64", k)
	}
	c.items[k] = v
	c.mu.Unlock()
	return nil
}

// Decrement an item of type int by n. Returns an error if the item's value is
// not an int, or if it was not found. If there is no error, the decremented
// value is returned.
func (c *cache) DecrementInt(k string, n int) (int, error) {
	c.mu.Lock()
	v, found := c.items[k]
	if !found || v.Expired() {
		c.mu.Unlock()
		return 0, fmt.Errorf("Item %s not found", k)
	}
	rv, ok := v.Object.(int)
	if !ok {
		c.mu.Unlock()
		return 0, fmt.Errorf("The value for %s is not an int", k)
	}
	nv := rv - n
	v.Object = nv
	c.items[k] = v
	c.mu.Unlock()
	return nv, nil
}

// Decrement an item of type int8 by n. Returns an error if the item's value is
// not an int8, or if it was not found. If there is no error, the decremented
// value is returned.
func (c *cache) DecrementInt8(k string, n int8) (int8, error) {
	c.mu.Lock()
	v, found := c.items[k]
	if !found || v.Expired() {
		c.mu.Unlock()
		return 0, fmt.Errorf("Item %s not found", k)
	}
	rv, ok := v.Object.(int8)
	if !ok {
		c.mu.Unlock()
		return 0, fmt.Errorf("The value for %s is not an int8", k)
	}
	nv := rv - n
	v.Object = nv
	c.items[k] = v
	c.mu.Unlock()
	return nv, nil
}

// Decrement an item of type int16 by n. Returns an error if the item's value is
// not an int16, or if it was not found. If there is no error, the decremented
// value is returned.
func (c *cache) DecrementInt16(k string, n int16) (int16, error) {
	c.mu.Lock()
	v, found := c.items[k]
	if !found || v.Expired() {
		c.mu.Unlock()
		return 0, fmt.Errorf("Item %s not found", k)
	}
	rv, ok := v.Object.(int16)
	if !ok {
		c.mu.Unlock()
		return 0, fmt.Errorf("The value for %s is not an int16", k)
	}
	nv := rv - n
	v.Object = nv
	c.items[k] = v
	c.mu.Unlock()
	return nv, nil
}

// Decrement an item of type int32 by n. Returns an error if the item's value is
// not an int32, or if it was not found. If there is no error, the decremented
// value is returned.
func (c *cache) DecrementInt32(k string, n int32) (int32, error) {
	c.mu.Lock()
	v, found := c.items[k]
	if !found || v.Expired() {
		c.mu.Unlock()
		return 0, fmt.Errorf("Item %s not found", k)
	}
	rv, ok := v.Object.(int32)
	if !ok {
		c.mu.Unlock()
		return 0, fmt.Errorf("The value for %s is not an int32", k)
	}
	nv := rv - n
	v.Object = nv
	c.items[k] = v
	c.mu.Unlock()
	return nv, nil
}

// Decrement an item of type int64 by n. Returns an error if the item's value is
// not an int64, or if it was not found. If there is no error, the decremented
// value is returned.
func (c *cache) DecrementInt64(k string, n int64) (int64, error) {
	c.mu.Lock()
	v, found := c.items[k]
	if !found || v.Expired() {
		c.mu.Unlock()
		return 0, fmt.Errorf("Item %s not found", k)
	}
	rv, ok := v.Object.(int64)
	if !ok {
		c.mu.Unlock()
		return 0, fmt.Errorf("The value for %s is not an int64", k)
	}
	nv := rv - n
	v.Object = nv
	c.items[k] = v
	c.mu.Unlock()
	return nv, nil
}

// Decrement an item of type uint by n. Returns an error if the item's value is
// not an uint, or if it was not found. If there is no error, the decremented
// value is returned.
func (c *cache) DecrementUint(k string, n uint) (uint, error) {
	c.mu.Lock()
	v, found := c.items[k]
	if !found || v.Expired() {
		c.mu.Unlock()
		return 0, fmt.Errorf("Item %s not found", k)
	}
	rv, ok := v.Object.(uint)
	if !ok {
		c.mu.Unlock()
		return 0, fmt.Errorf("The value for %s is not an uint", k)
	}
	nv := rv - n
	v.Object = nv
	c.items[k] = v
	c.mu.Unlock()
	return nv, nil
}

// Decrement an item of type uintptr by n. Returns an error if the item's value
// is not an uintptr, or if it was not found. If there is no error, the
// decremented value is returned.
func (c *cache) DecrementUintptr(k string, n uintptr) (uintptr, error) {
	c.mu.Lock()
	v, found := c.items[k]
	if !found || v.Expired() {
		c.mu.Unlock()
		return 0, fmt.Errorf("Item %s not found", k)
	}
	rv, ok := v.Object.(uintptr)
	if !ok {
		c.mu.Unlock()
		return 0, fmt.Errorf("The value for %s is not an uintptr", k)
	}
	nv := rv - n
	v.Object = nv
	c.items[k] = v
	c.mu.Unlock()
	return nv, nil
}

// Decrement an item of type uint8 by n. Returns an error if the item's value is
// not an uint8, or if it was not found. If there is no error, the decremented
// value is returned.
func (c *cache) DecrementUint8(k string, n uint8) (uint8, error) {
	c.mu.Lock()
	v, found := c.items[k]
	if !found || v.Expired() {
		c.mu.Unlock()
		return 0, fmt.Errorf("Item %s not found", k)
	}
	rv, ok := v.Object.(uint8)
	if !ok {
		c.mu.Unlock()
		return 0, fmt.Errorf("The value for %s is not an uint8", k)
	}
	nv := rv - n
	v.Object = nv
	c.items[k] = v
	c.mu.Unlock()
	return nv, nil
}

// Decrement an item of type uint16 by n. Returns an error if the item's value
// is not an uint16, or if it was not found. If there is no error, the
// decremented value is returned.
func (c *cache) DecrementUint16(k string, n uint16) (uint16, error) {
	c.mu.Lock()
	v, found := c.items[k]
	if !found || v.Expired() {
		c.mu.Unlock()
		return 0, fmt.Errorf("Item %s not found", k)
	}
	rv, ok := v.Object.(uint16)
	if !ok {
		c.mu.Unlock()
		return 0, fmt.Errorf("The value for %s is not an uint16", k)
	}
	nv := rv - n
	v.Object = nv
	c.items[k] = v
	c.mu.Unlock()
	return nv, nil
}

// Decrement an item of type uint32 by n. Returns an error if the item's value
// is not an uint32, or if it was not found. If there is no error, the
// decremented value is returned.
func (c *cache) DecrementUint32(k string, n uint32) (uint32, error) {
	c.mu.Lock()
	v, found := c.items[k]
	if !found || v.Expired() {
		c.mu.Unlock()
		return 0, fmt.Errorf("Item %s not found", k)
	}
	rv, ok := v.Object.(uint32)
	if !ok {
		c.mu.Unlock()
		return 0, fmt.Errorf("The value for %s is not an uint32", k)
	}
	nv := rv - n
	v.Object = nv
	c.items[k] = v
	c.mu.Unlock()
	return nv, nil
}

// Decrement an item of type uint64 by n. Returns an error if the item's value
// is not an uint64, or if it was not found. If there is no error, the
// decremented value is returned.
func (c *cache) DecrementUint64(k string, n uint64) (uint64, error) {
	c.mu.Lock()
	v, found := c.items[k]
	if !found || v.Expired() {
		c.mu.Unlock()
		return 0, fmt.Errorf("Item %s not found", k)
	}
	rv, ok := v.Object.(uint64)
	if !ok {
		c.mu.Unlock()
		return 0, fmt.Errorf("The value for %s is not an uint64", k)
	}
	nv := rv - n
	v.Object = nv
	c.items[k] = v
	c.mu.Unlock()
	return nv, nil
}

// Decrement an item of type float32 by n. Returns an error if the item's value
// is not an float32, or if it was not found. If there is no error, the
// decremented value is returned.
func (c *cache) DecrementFloat32(k string, n float32) (float32, error) {
	c.mu.Lock()
	v, found := c.items[k]
	if !found || v.Expired() {
		c.mu.Unlock()
		return 0, fmt.Errorf("Item %s not found", k)
	}
	rv, ok := v.Object.(float32)
	if !ok {
		c.mu.Unlock()
		return 0, fmt.Errorf("The value for %s is not an float32", k)
	}
	nv := rv - n
	v.Object = nv
	c.items[k] = v
	c.mu.Unlock()
	return nv, nil
}

// Decrement an item of type float64 by n. Returns an error if the item's value
// is not an float64, or if it was not found. If there is no error, the
// decremented value is returned.
func (c *cache) DecrementFloat64(k string, n float64) (float64, error) {
	c.mu.Lock()
	v, found := c.items[k]
	if !found || v.Expired() {
		c.mu.Unlock()
		return 0, fmt.Errorf("Item %s not found", k)
	}
	rv, ok := v.Object.(float64)
	if !ok {
		c.mu.Unlock()
		return 0, fmt.Errorf("The value for %s is not an float64", k)
	}
	nv := rv - n
	v.Object = nv
	c.items[k] = v
	c.mu.Unlock()
	return nv, nil
}

// Delete an item from the cache. Does nothing if the key is not in the cache.
func (c *cache) Delete(k string) {
	c.mu.Lock()
	v, evicted := c.delete(k)
	c.mu.Unlock()
	if evicted {
		c.onEvicted(k, v)
	}
}

func (c *cache) delete(k string) (interface{}, bool) {
	if c.onEvicted != nil {
		if v, found := c.items[k]; found {
			delete(c.items, k)
			return v.Object, true
		}
	}
	delete(c.items, k)
	return nil, false
}

type keyAndValue struct {
	key   string
	value interface{}
}

// Delete all expired items from the cache.
//删除过期的缓存
func (c *cache) DeleteExpired() {
	var evictedItems []keyAndValue
	now := time.Now().UnixNano()
	c.mu.Lock()
	for k, v := range c.items {
		// "Inlining" of expired
		if v.Expiration > 0 && now > v.Expiration {
			ov, evicted := c.delete(k)
			if evicted {
				evictedItems = append(evictedItems, keyAndValue{k, ov})
			}
		}
	}
	c.mu.Unlock()
	for _, v := range evictedItems {
		c.onEvicted(v.key, v.value)
	}
}

//Sets an (optional) function that is called with the key and value when an
//item is evicted from the cache. (Including when it is deleted manually, but
//not when it is overwritten.) Set to nil to disable.
//设置一个可选函数，当缓存驱逐或手动删除时回调使用，设置空可以禁用
func (c *cache) OnEvicted(f func(string, interface{})) {
	c.mu.Lock()
	c.onEvicted = f
	c.mu.Unlock()
}

//Write the cache's items (using Gob) to an io.Writer.
//
//NOTE: This method is deprecated in favor of c.Items() and NewFrom() (see the
//documentation for NewFrom().)
//将缓存项写通过 Gob 写入io.writer，注意：这个方法被启用，取而代之的是c.Items() NewFrom
func (c *cache) Save(w io.Writer) (err error) {
	enc := gob.NewEncoder(w)
	defer func() {
		if x := recover(); x != nil {
			err = fmt.Errorf("Error registering item types with Gob library")
		}
	}()
	c.mu.RLock()
	defer c.mu.RUnlock()
	for _, v := range c.items {
		gob.Register(v.Object)
	}
	err = enc.Encode(&c.items)
	return
}

// Save the cache's items to the given filename, creating the file if it
// doesn't exist, and overwriting it if it does.
//
// NOTE: This method is deprecated in favor of c.Items() and NewFrom() (see the
// documentation for NewFrom().)
func (c *cache) SaveFile(fname string) error {
	fp, err := os.Create(fname)
	if err != nil {
		return err
	}
	err = c.Save(fp)
	if err != nil {
		fp.Close()
		return err
	}
	return fp.Close()
}

// Add (Gob-serialized) cache items from an io.Reader, excluding any items with
// keys that already exist (and haven't expired) in the current cache.
//
// NOTE: This method is deprecated in favor of c.Items() and NewFrom() (see the
// documentation for NewFrom().)
func (c *cache) Load(r io.Reader) error {
	dec := gob.NewDecoder(r)
	items := map[string]Item{}
	err := dec.Decode(&items)
	if err == nil {
		c.mu.Lock()
		defer c.mu.Unlock()
		for k, v := range items {
			ov, found := c.items[k]
			if !found || ov.Expired() {
				c.items[k] = v
			}
		}
	}
	return err
}

// Load and add cache items from the given filename, excluding any items with
// keys that already exist in the current cache.
//
// NOTE: This method is deprecated in favor of c.Items() and NewFrom() (see the
// documentation for NewFrom().)
func (c *cache) LoadFile(fname string) error {
	fp, err := os.Open(fname)
	if err != nil {
		return err
	}
	err = c.Load(fp)
	if err != nil {
		fp.Close()
		return err
	}
	return fp.Close()
}

// Copies all unexpired items in the cache into a new map and returns it.
//将缓存中所有未过期的项目复制到map中返回
func (c *cache) Items() map[string]Item {
	c.mu.RLock()
	defer c.mu.RUnlock()
	m := make(map[string]Item, len(c.items))
	now := time.Now().UnixNano()
	for k, v := range c.items {
		// "Inlining" of Expired
		if v.Expiration > 0 {
			if now > v.Expiration {
				continue
			}
		}
		m[k] = v
	}
	return m
}

//Returns the number of items in the cache. This may include items that have
//expired, but have not yet been cleaned up.
// 返回缓存中的项目数，包括已过期未清理的
func (c *cache) ItemCount() int {
	c.mu.RLock()
	n := len(c.items)
	c.mu.RUnlock()
	return n
}

// Delete all items from the cache.
// 清空所有缓存
func (c *cache) Flush() {
	c.mu.Lock()
	c.items = map[string]Item{}
	c.mu.Unlock()
}

type janitor struct {
	Interval time.Duration
	stop     chan bool
}

func (j *janitor) Run(c *cache) {
	ticker := time.NewTicker(j.Interval)
	for {
		select {
		case <-ticker.C:
			c.DeleteExpired()
		case <-j.stop:
			ticker.Stop()
			return
		}
	}
}

func stopJanitor(c *Cache) {
	c.janitor.stop <- true
}

func runJanitor(c *cache, ci time.Duration) {
	j := &janitor{
		Interval: ci,
		stop:     make(chan bool),
	}
	c.janitor = j
	go j.Run(c)
}

//返回一个cache对象
func newCache(de time.Duration, m map[string]Item) *cache {
	if de == 0 {
		de = -1
	}
	c := &cache{
		defaultExpiration: de,
		items:             m,
	}
	return c
}


func newCacheWithJanitor(de time.Duration, ci time.Duration, m map[string]Item) *Cache {
	c := newCache(de, m)
	//This trick ensures that the janitor goroutine (which--granted it
	//was enabled--is running DeleteExpired on c forever) does not keep
	//the returned C object from being garbage collected. When it is
	//garbage collected, the finalizer stops the janitor goroutine, after
	//which c can be collected.
	//这个方法确认janitor  go携程（运行deleteExpired的永远运行着），不保留从垃圾回收中返回的对
	C := &Cache{c}
	if ci > 0 {
		runJanitor(c, ci)
		runtime.SetFinalizer(C, stopJanitor)
	}
	return C
}

//Return a new cache with a given default expiration duration and cleanup
//interval. If the expiration duration is less than one (or NoExpiration),
//the items in the cache never expire (by default), and must be deleted
//manually. If the cleanup interval is less than one, expired items are not
//deleted from the cache before calling c.DeleteExpired().
//返回具有给丁默认过期时间和清理的新缓存，如果小于1或者没有时间，则永不过期，必须手动删除，如果清理周期小于1，在调用c.deleteExpired前则永不删除过期的项目，
func New(defaultExpiration, cleanupInterval time.Duration) *Cache {
	items := make(map[string]Item)
	return newCacheWithJanitor(defaultExpiration, cleanupInterval, items)
}

//Return a new cache with a given default expiration duration and cleanup
//interval. If the expiration duration is less than one (or NoExpiration),
//the items in the cache never expire (by default), and must be deleted
//manually. If the cleanup interval is less than one, expired items are not
//deleted from the cache before calling c.DeleteExpired().
//
//NewFrom() also accepts an items map which will serve as the underlying map
//for the cache. This is useful for starting from a deserialized cache
//(serialized using e.g. gob.Encode() on c.Items()), or passing in e.g.
//make(map[string]Item, 500) to improve startup performance when the cache
//is expected to reach a certain minimum size.
//
//Only the cache's methods synchronize access to this map, so it is not
//recommended to keep any references to the map around after creating a cache.
//If need be, the map can be accessed at a later point using c.Items() (subject
//to the same caveat.)
//
//Note regarding serialization: When using e.g. gob, make sure to
//gob.Register() the individual types stored in the cache before encoding a
//map retrieved with c.Items(), and to register those same types before
//decoding a blob containing an items map.
func NewFrom(defaultExpiration, cleanupInterval time.Duration, items map[string]Item) *Cache {
	return newCacheWithJanitor(defaultExpiration, cleanupInterval, items)
}
