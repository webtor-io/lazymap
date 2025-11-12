package lazymap

import (
	"errors"
	"fmt"
	"math/rand"
	"sync"
	"testing"
	"time"
)

type TestMap struct {
	sleep time.Duration
	er    float64
	*LazyMap[int]
}

func NewTestMap(c *Config, sleep int, er float64) *TestMap {
	return &TestMap{
		sleep:   time.Duration(sleep) * time.Millisecond,
		LazyMap: New[int](c),
		er:      er,
	}
}

func (s *TestMap) Status(n int) (ItemStatus, bool) {
	return s.LazyMap.Status(fmt.Sprintf("%v", n))
}

func (s *TestMap) Get(n int) (int, error) {
	v, err := s.LazyMap.Get(fmt.Sprintf("%v", n), func() (int, error) {
		<-time.After(s.sleep)
		if rand.Float64() < s.er {
			return 0, errors.New("error")
		}
		return n, nil
	})
	if err != nil {
		return 0, nil
	}
	return v, nil
}

func (s *TestMap) Get2(n int, z int) (int, error) {
	v, err := s.LazyMap.Get(fmt.Sprintf("%v", n), func() (int, error) {
		<-time.After(s.sleep)
		if rand.Float64() < s.er {
			return 0, errors.New("error")
		}
		return z, nil
	})
	if err != nil {
		return 0, nil
	}
	return v, nil
}

func TestStatus(t *testing.T) {
	p := NewTestMap(&Config{}, 0, 0)
	p.Get(1)
	_, ok := p.Status(1)
	if !ok {
		t.Fatalf("p.Has(%v) == %v, expected true", 1, ok)
	}
}
func TestHasWithLongRunningTask(t *testing.T) {
	p := NewTestMap(&Config{
		Concurrency: 1,
	}, 1000, 0)
	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		p.Get(1)
		wg.Done()
	}()
	<-time.After(50 * time.Millisecond)
	go func() {
		p.Get(2)
		wg.Done()
	}()
	<-time.After(50 * time.Millisecond)
	_, ok := p.Status(1)
	if !ok {
		t.Fatalf("p.Has(%v) == %v, expected true", 1, ok)
	}
	_, ok = p.Status(2)
	if !ok {
		t.Fatalf("p.Has(%v) == %v, expected true", 2, ok)
	}
	wg.Wait()
}
func TestSequental(t *testing.T) {
	p := NewTestMap(&Config{}, 0, 0)
	for i := 0; i < 10; i++ {
		v, _ := p.Get(i)
		if v != i {
			t.Fatalf("p.Get(%v) == %v, expected %v", i, v, i)
		}
	}
	if p.Len() != 10 {
		t.Fatalf("len(p,m) == %v, expected %v", p.Len(), 10)
	}
}
func TestExpire(t *testing.T) {
	p := NewTestMap(&Config{
		Concurrency: 1,
		Expire:      10 * time.Millisecond,
	}, 0, 0)
	v, _ := p.Get2(1, 2)
	if v != 2 {
		t.Fatalf("p.Get(%v) == %v, expected %v", 2, v, 2)
	}
	v, _ = p.Get2(1, 5)
	if v != 2 {
		t.Fatalf("p.Get(%v) == %v, expected %v", 2, v, 2)
	}
	<-time.After(5 * time.Millisecond)
	v, _ = p.Get2(1, 5)
	if v != 2 {
		t.Fatalf("p.Get(%v) == %v, expected %v", 2, v, 2)
	}
	<-time.After(10 * time.Millisecond)
	v, _ = p.Get2(1, 5)
	if v != 5 {
		t.Fatalf("p.Get(%v) == %v, expected %v", 5, v, 5)
	}
	<-time.After(5 * time.Millisecond)
	v, _ = p.Get2(1, 7)
	if v != 5 {
		t.Fatalf("p.Get(%v) == %v, expected %v", 5, v, 5)
	}
}

func TestConcurrentWithConcurrency10(t *testing.T) {
	s := time.Now()
	p := NewTestMap(&Config{Concurrency: 10}, 300, 0)
	var wg sync.WaitGroup
	wg.Add(10)
	for i := 0; i < 10; i++ {
		go func(i int) {
			p.Get(i)
			wg.Done()
		}(i)
	}
	wg.Wait()
	if p.Len() != 10 {
		t.Fatalf("len(p,m) == %v, expected %v", p.Len(), 10)
	}
	if time.Since(s) > time.Second {
		t.Fatalf("It takes %v seconds to perform test, expected less than a second", time.Since(s).Seconds())
	}
}

func TestConcurrentWithConcurrency10AndExpire(t *testing.T) {
	p := NewTestMap(&Config{Concurrency: 10, Expire: 10 * time.Millisecond}, 0, 0)
	var wg sync.WaitGroup
	wg.Add(10)
	for i := 0; i < 10; i++ {
		go func(i int) {
			p.Get(i)
			wg.Done()
		}(i)
	}
	wg.Wait()
	<-time.After(100 * time.Millisecond)
	if p.Len() != 0 {
		t.Fatalf("len(p,m) == %v, expected %v", p.Len(), 0)
	}
}

func TestConcurrentWithConcurrency10AndInitExpire(t *testing.T) {
	p := NewTestMap(&Config{Concurrency: 1, InitExpire: 10 * time.Millisecond}, 200, 0)
	var wg sync.WaitGroup
	wg.Add(10)
	for i := 0; i < 10; i++ {
		go func(i int) {
			p.Get(i)
			wg.Done()
		}(i)
	}
	<-time.After(50 * time.Millisecond)
	if p.Len() != 1 {
		t.Fatalf("len(p,m) == %v, expected %v", p.Len(), 1)
	}
}

func TestConcurrentWithConcurrency1(t *testing.T) {
	s := time.Now()
	p := NewTestMap(&Config{
		Concurrency: 1,
	}, 300, 0)
	var wg sync.WaitGroup
	wg.Add(10)
	for i := 0; i < 10; i++ {
		go func(i int) {
			p.Get(i)
			wg.Done()
		}(i)
	}
	wg.Wait()
	if p.Len() != 10 {
		t.Fatalf("len(p,m) == %v, expected %v", p.Len(), 10)
	}
	if time.Since(s) < 3*time.Second {
		t.Fatalf("It takes %v seconds to perform test, expected more than 3 second", time.Since(s).Seconds())
	}
}

func TestOutOfCapacity1WithConcurrency1(t *testing.T) {
	p := NewTestMap(&Config{Concurrency: 1, Capacity: 1}, 1, 0)
	var wg sync.WaitGroup
	wg.Add(10)
	for i := 0; i < 10; i++ {
		go func(i int) {
			p.Get(i)
			wg.Done()
		}(i)
	}
	wg.Wait()
	if p.Len() != 1 {
		t.Fatalf("len(p,m) == %v, expected %v", p.Len(), 1)
	}
}

func TestOutOfCapacity1WithConcurrency1WithEviction(t *testing.T) {
	p := NewTestMap(&Config{Concurrency: 1, Capacity: 1, EvictNotInited: true}, 1, 0)
	var wg sync.WaitGroup
	wg.Add(10)
	for i := 0; i < 10; i++ {
		go func(i int) {
			p.Get(i)
			wg.Done()
		}(i)
	}
	wg.Wait()
	if p.Len() != 1 {
		t.Fatalf("len(p,m) == %v, expected %v", p.Len(), 1)
	}
}

func TestOutOfCapacity1WithConcurrency10(t *testing.T) {
	p := NewTestMap(&Config{Capacity: 1}, 0, 0)
	var wg sync.WaitGroup
	wg.Add(10)
	for i := 0; i < 10; i++ {
		go func(i int) {
			p.Get(i)
			wg.Done()
		}(i)
	}
	wg.Wait()
	if p.Len() == 10 {
		t.Fatalf("len(p,m) == %v, expected less", p.Len())
	}
}

func TestOutOfCapacity100WithConcurrency10(t *testing.T) {
	p := NewTestMap(&Config{Capacity: 100}, 0, 0)
	var wg sync.WaitGroup
	wg.Add(1000)
	for i := 0; i < 1000; i++ {
		go func(i int) {
			p.Get(i)
			wg.Done()
		}(i)
	}
	wg.Wait()
	keys := p.Keys()
	if len(keys) >= 100 {
		t.Fatalf("len(keys) == %v, expected less", len(keys))
	}
	//for _, v := range keys {
	//	j := p.m[v].val
	//	if j < 800 {
	//		t.Fatalf("j == %v, expected more than 800", j)
	//	}
	//}
}

func BenchmarkCapacity100Concurrency10(b *testing.B) {
	p := NewTestMap(&Config{Capacity: 100}, 0, 0)
	var wg sync.WaitGroup
	wg.Add(b.N)
	for i := 0; i < b.N; i++ {
		go func() {
			i := rand.Intn(1000)
			p.Get(i)
			wg.Done()
		}()
	}
	wg.Wait()
}

func BenchmarkCapacity1000Concurrency100(b *testing.B) {
	p := NewTestMap(&Config{Capacity: 1000, Concurrency: 100}, 0, 0)
	var wg sync.WaitGroup
	wg.Add(b.N)
	for i := 0; i < b.N; i++ {
		go func() {
			i := rand.Intn(10000)
			p.Get(i)
			wg.Done()
		}()
	}
	wg.Wait()
}

func BenchmarkCapacity1000WConcurrency100Expire(b *testing.B) {
	p := NewTestMap(&Config{Capacity: 1000, Concurrency: 100, Expire: 10 * time.Millisecond, ErrorExpire: 5 * time.Millisecond}, 10, 0.1)
	var wg sync.WaitGroup
	wg.Add(b.N)
	for i := 0; i < b.N; i++ {
		go func() {
			i := rand.Intn(10000)
			p.Get(i)
			wg.Done()
		}()
	}
	wg.Wait()
}

func BenchmarkCapacity1000WConcurrency100ExpireAndInitExpire(b *testing.B) {
	p := NewTestMap(&Config{Capacity: 1000, Concurrency: 100, InitExpire: 10 * time.Millisecond, Expire: 10 * time.Millisecond, ErrorExpire: 5 * time.Millisecond}, 10, 0.1)
	var wg sync.WaitGroup
	wg.Add(b.N)
	for i := 0; i < b.N; i++ {
		go func() {
			i := rand.Intn(10000)
			p.Get(i)
			wg.Done()
		}()
	}
	wg.Wait()
}
