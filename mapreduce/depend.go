package mapreduce

import (
	"context"
	"sync"
	"sync/atomic"
)

type Options struct {
	ctx     context.Context
	workers int
}

func NewOptions() *Options {
	return &Options{
		ctx:     context.Background(),
		workers: defaultWorkers,
	}
}

// WithContext customizes a mapreduce processing accepts a given ctx.
func (o *Options) WithContext(ctx context.Context) *Options {
	return &Options{
		ctx:     ctx,
		workers: o.workers,
	}
}

// WithWorkers customizes a mapreduce processing with given workers.
func (o *Options) WithWorkers(workers int) *Options {
	if workers < minWorkers {
		workers = minWorkers
	}
	return &Options{
		ctx:     o.ctx,
		workers: workers,
	}
}

type onceChan struct {
	channel chan any
	wrote   int32
}

func (oc *onceChan) write(val any) {
	if atomic.CompareAndSwapInt32(&oc.wrote, 0, 1) {
		oc.channel <- val
	}
}

func once(fn func(error)) func(error) {
	once := new(sync.Once)
	return func(err error) {
		once.Do(func() {
			fn(err)
		})
	}
}

// Writer interface wraps Write method.
type Writer[T any] interface {
	Write(v T)
}

type guardedWriter[T any] struct {
	ctx     context.Context
	channel chan<- T
	done    <-chan struct{}
}

func newGuardedWriter[T any](ctx context.Context, channel chan<- T, done <-chan struct{}) guardedWriter[T] {
	return guardedWriter[T]{
		ctx:     ctx,
		channel: channel,
		done:    done,
	}
}

func (gw guardedWriter[T]) Write(v T) {
	select {
	case <-gw.ctx.Done():
	case <-gw.done:
	default:
		gw.channel <- v
	}
}

func buildSource[T any](generate GenerateFunc[T], panicChan *onceChan) chan T {
	source := make(chan T)
	go func() {
		defer func() {
			if r := recover(); r != nil {
				panicChan.write(r)
			}
			close(source)
		}()

		generate(source)
	}()

	return source
}

// drain drains the channel.
func drain[T any](channel <-chan T) {
	// drain the channel
	for range channel {
	}
}
