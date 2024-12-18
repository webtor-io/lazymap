package lazymap

import (
	"math"
	"sort"
	"sync"
	"time"
)

type ItemStatus int

const (
	None ItemStatus = iota
	Enqueued
	Running
	Done
	Failed
	Canceled
)

type LazyMap[T any] struct {
	mux            sync.RWMutex
	m              map[string]*lazyMapItem[T]
	expire         time.Duration
	errorExpire    time.Duration
	storeErrors    bool
	initExpire     time.Duration
	c              chan bool
	capacity       int
	cleanThreshold float64
	cleanRatio     float64
	cleaning       bool
	evictNotInited bool
}

type Config struct {
	Concurrency    int
	Expire         time.Duration
	ErrorExpire    time.Duration
	StoreErrors    bool
	InitExpire     time.Duration
	Capacity       int
	CleanThreshold float64
	CleanRatio     float64
	EvictNotInited bool
}

type EvictedError struct{}

func (s *EvictedError) Error() string {
	return "Evicted"
}

func New[T any](cfg *Config) LazyMap[T] {
	capacity := cfg.Capacity
	concurrency := 10
	if cfg.Concurrency != 0 {
		concurrency = cfg.Concurrency
	}
	expire := cfg.Expire
	errorExpire := expire
	if cfg.ErrorExpire != 0 {
		errorExpire = cfg.ErrorExpire
	}
	cleanThreshold := 0.9
	if cfg.CleanThreshold != 0 {
		cleanThreshold = cfg.CleanThreshold
	}
	cleanRatio := 0.1
	if cfg.CleanRatio != 0 {
		cleanRatio = cfg.CleanRatio
	}
	c := make(chan bool, concurrency)
	for i := 0; i < concurrency; i++ {
		c <- true
	}
	return LazyMap[T]{
		c:              c,
		expire:         expire,
		errorExpire:    errorExpire,
		storeErrors:    cfg.StoreErrors,
		initExpire:     cfg.InitExpire,
		capacity:       capacity,
		cleanThreshold: cleanThreshold,
		cleanRatio:     cleanRatio,
		evictNotInited: cfg.EvictNotInited,
		m:              make(map[string]*lazyMapItem[T], capacity),
	}
}

type lazyMapItem[T any] struct {
	key     string
	val     T
	f       func() (T, error)
	inited  bool
	err     error
	la      time.Time
	mux     sync.Mutex
	cancel  bool
	t       *time.Timer
	exp     time.Duration
	running bool
}

func (s *lazyMapItem[T]) Touch() {
	if s.t != nil {
		s.t.Reset(s.exp)
	}
	s.la = time.Now()
}

func (s *lazyMapItem[T]) Cancel() {
	if s.cancel {
		return
	}
	if s.t != nil {
		s.t.Stop()
	}
	s.cancel = true
}

func (s *lazyMapItem[T]) doExpire(exp time.Duration) <-chan time.Time {
	if s.t != nil {
		s.t.Stop()
	}
	s.exp = exp
	s.t = time.NewTimer(exp)
	return s.t.C
}

func (s *lazyMapItem[T]) Get() (T, error) {
	s.mux.Lock()
	defer s.mux.Unlock()
	s.la = time.Now()
	if s.inited {
		return s.val, s.err
	}

	if s.t != nil {
		s.t.Stop()
		s.t = nil
	}
	s.running = true
	if s.cancel {
		var zero T
		s.val, s.err = zero, &EvictedError{}
	} else {
		s.val, s.err = s.f()
	}
	s.inited = true
	s.running = false
	return s.val, s.err
}

func (s *LazyMap[T]) doExpire(expire time.Duration, key string, v *lazyMapItem[T]) {
	c := v.doExpire(expire)
	go func() {
		<-c
		s.mux.Lock()
		v.Cancel()
		delete(s.m, key)
		s.mux.Unlock()
	}()
}

func (s *LazyMap[T]) clean() {
	if s.capacity == 0 {
		return
	}
	thr := int(math.Ceil(s.cleanThreshold * float64(s.capacity)))
	if len(s.m) <= thr {
		return
	}
	if s.cleaning {
		return
	}
	s.cleaning = true
	t := make([]*lazyMapItem[T], 0, len(s.m))
	for _, v := range s.m {
		t = append(t, v)
	}
	sort.Slice(t, func(i, j int) bool {
		return t[i].la.Before(t[j].la)
	})
	cq := int(math.Ceil(s.cleanRatio * float64(s.capacity)))
	dc := 0
	for i := 0; i < len(t); i++ {
		if !t[i].running && (t[i].inited || s.evictNotInited) {
			t[i].Cancel()
			delete(s.m, t[i].key)
			dc++
		}
		if dc >= cq {
			break
		}
	}
	s.cleaning = false
}

func (s *lazyMapItem[T]) Status() ItemStatus {
	if s.cancel {
		return Canceled
	}
	if s.running {
		return Running
	}
	if s.inited && s.err != nil {
		return Failed
	}
	if s.inited && s.err == nil {
		return Done
	}
	return Enqueued
}

func (s *LazyMap[T]) Status(key string) (ItemStatus, bool) {
	v, loaded := s.m[key]
	if !loaded {
		return None, false
	}
	return v.Status(), true
}

func (s *LazyMap[T]) Touch(key string) bool {
	v, loaded := s.m[key]
	if loaded {
		v.Touch()
		return true
	}
	return false
}

func (s *LazyMap[T]) Get(key string, f func() (T, error)) (T, error) {
	s.mux.RLock()
	v, loaded := s.m[key]
	if loaded {
		s.mux.RUnlock()
		return v.Get()
	}
	s.mux.RUnlock()
	s.mux.Lock()
	v, loaded = s.m[key]
	if loaded {
		s.mux.Unlock()
		return v.Get()
	}
	v = &lazyMapItem[T]{
		key: key,
		f:   f,
		la:  time.Now(),
	}
	s.m[key] = v
	s.clean()
	s.mux.Unlock()
	if s.initExpire != 0 {
		s.doExpire(s.initExpire, key, v)
	}
	<-s.c
	r, err := v.Get()
	s.c <- true
	if err != nil && !s.storeErrors {
		s.Drop(key)
	} else if err != nil && s.errorExpire != 0 {
		s.doExpire(s.errorExpire, key, v)
	} else if err == nil && s.expire != 0 {
		s.doExpire(s.expire, key, v)
	}
	return r, err
}

func (s *LazyMap[T]) Drop(key string) {
	s.mux.Lock()
	if v, ok := s.m[key]; ok {
		v.Cancel()
		delete(s.m, key)
	}
	s.mux.Unlock()
}
