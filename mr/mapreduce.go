package mr

import (
	"context"
	"errors"
	"fmt"
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
)

type MapReduce[T any, U any, V any] struct {
	mapper   MapperFunc[T, U]
	reducer  ReducerFunc[U, V]
	generate GenerateFunc[T]
	options  Options
}

func NewMapReduce[T any, U any, V any](generate GenerateFunc[T], mapper MapperFunc[T, U], reducer ReducerFunc[U, V], options Options) *MapReduce[T, U, V] {
	//Options := buildOptions(opts...)
	return &MapReduce[T, U, V]{
		mapper:   mapper,
		reducer:  reducer,
		generate: generate,
		options:  options,
	}
}

func New[T any, U any, V any]() *MapReduce[T, U, V] {
	return &MapReduce[T, U, V]{}
}

func (mr *MapReduce[T, U, V]) Copy() *MapReduce[T, U, V] {
	// 创建新的 MapReduce 实例
	return &MapReduce[T, U, V]{
		generate: mr.generate,
		mapper:   mr.mapper,
		reducer:  mr.reducer,
		options: Options{
			ctx:     mr.options.ctx,     // 复制上下文
			workers: mr.options.workers, // 复制工作线程数
		},
	}
}

func (mr *MapReduce[T, U, V]) Generate(generate GenerateFunc[T]) *MapReduce[T, U, V] {
	nx := mr.Copy()
	nx.generate = generate
	return nx
}

func (mr *MapReduce[T, U, V]) Mapper(mapper MapperFunc[T, U]) *MapReduce[T, U, V] {
	nx := mr.Copy()
	nx.mapper = mapper
	return nx
}

func (mr *MapReduce[T, U, V]) Reducer(reducer ReducerFunc[U, V]) *MapReduce[T, U, V] {
	nx := mr.Copy()
	nx.reducer = reducer
	return nx
}

func (mr *MapReduce[T, U, V]) Options(opts Options) *MapReduce[T, U, V] {
	nx := mr.Copy()
	nx.options = opts
	return nx
}

func (mr *MapReduce[T, U, V]) WithWorkers(workers int) *MapReduce[T, U, V] {
	mr.options.WithWorkers(workers)
	return mr
}

func (mr *MapReduce[T, U, V]) WithContext(ctx context.Context) *MapReduce[T, U, V] {
	mr.options.WithContext(ctx)
	return mr
}

func (mr *MapReduce[T, U, V]) Run() (V, error) {
	if mr.generate == nil || mr.mapper == nil || mr.reducer == nil {
		return *new(V), fmt.Errorf("generate, mapper or reducer not set")
	}
	panicChan := &onceChan{channel: make(chan any)}
	source := buildSource(mr.generate, panicChan)
	// 执行 MapReduce 操作
	return mapReduceWithPanicChan(source, panicChan, mr.mapper, mr.reducer, mr.options)
}

func MapReduceFunc[T, U, V any](generate GenerateFunc[T], mapper MapperFunc[T, U], reducer ReducerFunc[U, V], opts Options) (V, error) {
	panicChan := &onceChan{channel: make(chan any)}
	source := buildSource(generate, panicChan)
	return mapReduceWithPanicChan(source, panicChan, mapper, reducer, opts)
}

func MapReduceChan[T, U, V any](source <-chan T, mapper MapperFunc[T, U], reducer ReducerFunc[U, V], opts Options) (V, error) {
	panicChan := &onceChan{channel: make(chan any)}
	return mapReduceWithPanicChan(source, panicChan, mapper, reducer, opts)
}

/*
mapReduceWithPanicChan 对源数据进行 MapReduce 操作，并处理可能的 panic。
入参:
 1. source: 输入数据源的通道
 2. panicChan: 处理 panic 的通道
 3. mapper: 用于将源数据映射为中间结果的函数
 4. reducer: 用于将中间结果汇总成最终结果的函数
 5. options: 配置选项，包括上下文和并发工作线程数

出参:
 1. val: 最终的结果值
 2. err: 可能出现的错误
*/
func mapReduceWithPanicChan[T, U, V any](source <-chan T, panicChan *onceChan, mapper MapperFunc[T, U],
	reducer ReducerFunc[U, V], options Options) (val V, err error) {
	// 结果输出通道，用于接收 reducer 的最终结果
	output := make(chan V)
	defer func() {
		// 确保 reducer 只能写入一个结果，如果写入多个结果则 panic
		for range output {
			panic("more than one element written in reducer")
		}
	}()

	// 收集器通道，用于接收 mapper 生成的中间结果
	collector := make(chan U, options.workers)
	// done 通道,用于通知所有 mapper 和 reducer 停止处理
	done := make(chan struct{})
	// 创建一个线程安全的 writer，用于将结果写入 output
	writer := newGuardedWriter(options.ctx, output, done)
	var closeOnce sync.Once
	// 原子类型，用于避免数据竞争
	var retErr error

	// 关闭 done 和 output 通道
	finish := func() {
		closeOnce.Do(func() {
			close(done)
			close(output)
		})
	}
	// cancel 函数用于处理取消操作，设置错误并关闭通道
	cancel := once(func(err error) {
		if err != nil {
			retErr = err
		} else {
			retErr = errors.New("CancelWithNil")
		}
		drain(source)
		finish()
	})

	// 启动一个 goroutine 执行 reducer 操作
	go func() {
		defer func() {
			// 确保 collector 通道的所有数据都被处理
			drain(collector)
			// 捕获可能的 panic，并将其写入 panicChan
			if r := recover(); r != nil {
				panicChan.write(r)
			}
			// 关闭 done 和 output 通道
			finish()
		}()

		// 执行 reducer 操作
		reducer(collector, writer, cancel)
	}()

	// 启动一个 goroutine 执行 mapper 操作
	go executeMappers(mapperContext[T, U]{
		ctx: options.ctx,
		mapper: func(item T, w Writer[U]) {
			mapper(item, w, cancel)
		},
		source:    source,
		panicChan: panicChan,
		collector: collector,
		doneChan:  done,
		workers:   options.workers,
	})

	// 等待 select 的结果
	select {
	case <-options.ctx.Done():
		// 如果上下文已取消，调用 cancel 并设置错误
		cancel(context.DeadlineExceeded)
		err = context.DeadlineExceeded
	case v := <-panicChan.channel:
		// 如果 panicChan 中有 panic 事件，先处理 output 通道，然后 panic
		drain(output)
		panic(v)
	case v, ok := <-output:
		if retErr != nil {
			// 如果有错误，返回错误
			err = retErr
		} else if ok {
			// 如果 output 通道有值，返回该值
			val = v
		} else {
			// 如果 output 通道关闭且没有值，返回错误
			err = errors.New("ReduceNoOutput")
		}
	}

	return
}

// executeMappers 启动多个 goroutine 来并发地处理 source 中的元素，并将结果写入 collector。根据worker自动限制mapper协程数
func executeMappers[T, U any](mCtx mapperContext[T, U]) {
	// 使用 WaitGroup 来等待所有 goroutine 完成
	var wg sync.WaitGroup
	defer func() {
		wg.Wait()
		close(mCtx.collector)
		// 读取并丢弃 source 通道中的所有数据
		drain(mCtx.source)
	}()

	// 用于标记是否出现了错误
	var failed int32
	// 用于限制并发的缓冲通道，容量为 workers 数量
	pool := make(chan struct{}, mCtx.workers)
	// 创建一个安全的 writer 来写入 collector
	writer := newGuardedWriter(mCtx.ctx, mCtx.collector, mCtx.doneChan)

	// 当 failed 为 0 时持续执行,使用标准库中的原子读
	for atomic.LoadInt32(&failed) == 0 {
		select {
		case <-mCtx.ctx.Done(): // 上下文取消时返回
			return
		case <-mCtx.doneChan: // doneChan 被关闭时返回，表示需要终止处理
			return
		default:
		}
		select {
		case <-mCtx.ctx.Done(): // 上下文取消时返回
			return
		case <-mCtx.doneChan: // doneChan 被关闭时返回，表示需要终止处理
			return
		case pool <- struct{}{}: // 从 pool 通道中获取一个 token，表示有一个 goroutine 空闲
			item, ok := <-mCtx.source // 从 source 通道中获取下一个 item
			// 如果 source 通道已关闭，则释放一个 pool token 并返回
			if !ok {
				<-pool
				return
			}

			// 增加 WaitGroup 计数器
			wg.Add(1)
			// 启动一个 goroutine 处理 item
			go func() {
				defer func() {
					// 捕获 panic
					if r := recover(); r != nil {
						atomic.AddInt32(&failed, 1) // 标记为失败
						mCtx.panicChan.write(r)     // 将 panic 写入 panicChan
					}
					wg.Done()
					<-pool // 释放一个 pool token
				}()

				mCtx.mapper(item, writer) // 处理 item，并将结果写入 writer

			}()
		}
	}
}
