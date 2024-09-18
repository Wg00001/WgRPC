package mapreduce

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
)

const (
	defaultWorkers = 16
	minWorkers     = 1
)

type (
	// ForEachFunc is used to do element processing, but no output.
	ForEachFunc[T any] func(item T)
	// GenerateFunc is used to let callers send elements into source.
	GenerateFunc[T any] func(source chan<- T)
	// MapFunc is used to do element processing and write the output to writer.
	MapFunc[T, U any] func(item T, writer Writer[U])
	// MapperFunc is used to do element processing and write the output to writer,
	// use cancel func to cancel the processing.
	MapperFunc[T, U any] func(item T, writer Writer[U], cancel func(error))
	// ReducerFunc is used to reduce all the mapping output and write to writer,
	// use cancel func to cancel the processing.
	ReducerFunc[U, V any] func(pipe <-chan U, writer Writer[V], cancel func(error))
	// VoidReducerFunc is used to reduce all the mapping output, but no output.
	// Use cancel func to cancel the processing.
	VoidReducerFunc[U any] func(pipe <-chan U, cancel func(error))

	mapperContext[T, U any] struct {
		ctx       context.Context
		mapper    MapFunc[T, U]
		source    <-chan T
		panicChan *onceChan
		collector chan<- U
		doneChan  <-chan struct{}
		workers   int
	}

	// Writer interface wraps Write method.
	Writer[T any] interface {
		Write(v T)
	}

	MapReduce[T any, U any, V any] struct {
		mapper   MapperFunc[T, U]
		reducer  ReducerFunc[U, V]
		generate GenerateFunc[T]
		options  *mapReduceOptions
	}
)

func NewMapReduce[T any, U any, V any](generate GenerateFunc[T], mapper MapperFunc[T, U], reducer ReducerFunc[U, V], opts ...Option) *MapReduce[T, U, V] {
	options := buildOptions(opts...)
	return &MapReduce[T, U, V]{
		//ctx:      options.ctx,
		//workers:  options.workers,
		mapper:   mapper,
		reducer:  reducer,
		generate: generate,
		options:  options,
	}
}

func (mr *MapReduce[T, U, V]) Run() (V, error) {
	panicChan := &onceChan{channel: make(chan any)}
	source := buildSource(mr.generate, panicChan)

	output := make(chan V)
	defer func() {
		// 确保 reducer 只向 output 通道写入一个结果，多写则 panic
		for range output {
			panic("more than one element written in reducer")
		}
	}()

	collector := make(chan U, mr.options.workers)
	done := make(chan struct{})
	writer := newGuardedWriter(mr.options.ctx, output, done)
	var closeOnce sync.Once
	var retErr error
	finish := func() {
		closeOnce.Do(func() {
			close(done)
			close(output)
		})
	}
	cancel := once(func(err error) {
		if err != nil {
			retErr = err
		} else {
			retErr = errors.New("CancelWithNil")
		}
		drain(source)
		finish()
	})

	go func() {
		defer func() {
			drain(collector)
			if r := recover(); r != nil {
				panicChan.write(r)
			}
			finish()
		}()
		mr.reducer(collector, writer, cancel)
	}()

	go executeMappers(mapperContext[T, U]{
		ctx: mr.options.ctx,
		mapper: func(item T, w Writer[U]) {
			mr.mapper(item, w, cancel)
		},
		source:    source,
		panicChan: panicChan,
		collector: collector,
		doneChan:  done,
		workers:   mr.options.workers,
	})

	select {
	case <-mr.options.ctx.Done():
		cancel(context.DeadlineExceeded)
		return *new(V), context.DeadlineExceeded
	case v := <-panicChan.channel:
		drain(output)
		panic(v)
	case v, ok := <-output:
		if retErr != nil {
			return *new(V), retErr
		} else if ok {
			return v, nil
		} else {
			return *new(V), errors.New("ReduceNoOutput")
		}
	}
}

func executeMappers[T, U any](mCtx mapperContext[T, U]) {
	var wg sync.WaitGroup
	defer func() {
		wg.Wait()
		close(mCtx.collector)
		drain(mCtx.source)
	}()

	var failed int32
	pool := make(chan struct{}, mCtx.workers)
	writer := newGuardedWriter(mCtx.ctx, mCtx.collector, mCtx.doneChan)
	for atomic.LoadInt32(&failed) == 0 {
		select {
		case <-mCtx.ctx.Done():
			return
		case <-mCtx.doneChan:
			return
		case pool <- struct{}{}:
			item, ok := <-mCtx.source
			if !ok {
				<-pool
				return
			}

			wg.Add(1)
			go func() {
				defer func() {
					if r := recover(); r != nil {
						atomic.AddInt32(&failed, 1)
						mCtx.panicChan.write(r)
					}
					wg.Done()
					<-pool
				}()

				mCtx.mapper(item, writer)
			}()
		}
	}
}

func (mr *MapReduce[T, U, V]) WithWorkers(workers int) *MapReduce[T, U, V] {
	if workers < minWorkers {
		workers = minWorkers
	}
	mr.options.workers = workers
	return mr
}

func (mr *MapReduce[T, U, V]) WithContext(ctx context.Context) *MapReduce[T, U, V] {
	mr.options.ctx = ctx
	return mr
}
