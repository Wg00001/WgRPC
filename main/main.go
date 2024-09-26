package main

import (
	wgRPC "WgRPC"
	"WgRPC/mapreduce"
	"WgRPC/registry"
	"WgRPC/xclient"
	"bytes"
	"context"
	"encoding/gob"
	"fmt"
	"log"
	"net"
	"net/http"
	"reflect"
	"sync"
	"time"
)

type Foo int

type Args struct{ Num1, Num2 int }

func (f Foo) Sum(args Args, reply *int) error {
	*reply = args.Num1 + args.Num2
	return nil
}

func (f Foo) Sub(args Args, reply *int) error {
	*reply = args.Num1 - args.Num2
	return nil
}
func foo(xc *xclient.XClient, ctx context.Context, typ, serviceMethod string, args *Args) {
	var reply int
	var err error
	switch typ {
	case "call":
		err = xc.Call(serviceMethod, args, &reply, ctx)
	case "broadcast":
		err = xc.Broadcast(serviceMethod, args, &reply, ctx)
	}
	if err != nil {
		log.Printf("%s %s error: %v", typ, serviceMethod, err)
	} else {
		log.Printf("%s %s success: %d + %d = %d", typ, serviceMethod, args.Num1, args.Num2, reply)
	}
}

func startRegistry(wg *sync.WaitGroup) {
	l, _ := net.Listen("tcp", ":9999")
	registry.HandleHTTP()
	wg.Done()
	_ = http.Serve(l, nil)
}

func startServer(registryAddr string, wg *sync.WaitGroup) {
	var foo Foo
	l, _ := net.Listen("tcp", ":0")
	server := wgRPC.NewServer()
	_ = server.Register(&foo)
	registry.Heartbeat(registryAddr, "tcp@"+l.Addr().String(), 0)
	wg.Done()
	var nw bytes.Buffer
	gob.NewEncoder(&nw)
	server.Accept(l)
}
func call(registry string) {
	d := xclient.NewWgRegistryDiscovery(registry, 0)
	xc := xclient.NewXClient(d, xclient.RandomSelect, nil)
	defer func() { _ = xc.Close() }()
	// send request & receive response
	var wg sync.WaitGroup
	for i := 0; i < 5; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			foo(xc, context.Background(), "call", "Foo.Sum", &Args{Num1: i, Num2: i * i})
		}(i)
	}
	wg.Wait()
}

func broadcast(registry string) {
	d := xclient.NewWgRegistryDiscovery(registry, 0)
	xc := xclient.NewXClient(d, xclient.RandomSelect, nil)
	defer func() { _ = xc.Close() }()
	var wg sync.WaitGroup
	for i := 0; i < 5; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			foo(xc, context.Background(), "broadcast", "Foo.Sum", &Args{Num1: i, Num2: i * i})
			// expect 2 - 5 timeout
			ctx, _ := context.WithTimeout(context.Background(), time.Second*2)
			foo(xc, ctx, "broadcast", "Foo.Sleep", &Args{Num1: i, Num2: i * i})
		}(i)
	}
	wg.Wait()
}

func testRegister() {
	log.SetFlags(0)
	registryAddr := "http://localhost:9999/wgrpc/registry"
	var wg sync.WaitGroup
	wg.Add(1)
	go startRegistry(&wg)
	wg.Wait()

	time.Sleep(time.Second)
	wg.Add(2)
	go startServer(registryAddr, &wg)
	go startServer(registryAddr, &wg)
	wg.Wait()

	time.Sleep(time.Second)
	call(registryAddr)
	broadcast(registryAddr)
}

func ExampleMr() *mapreduce.MapReduce[int, int, int] {
	return mapreduce.New[int, int, int]().
		Generate(func(source chan<- int) {
			for i := 0; i <= 100; i++ {
				source <- i
			}
		}).
		Mapper(func(item int, writer mapreduce.Writer[int], cancel func(error)) {
			fmt.Println("mapper:", item)
			time.Sleep(time.Second)
			writer.Write(item)
		}).
		Reducer(func(pipe <-chan int, writer mapreduce.Writer[int], cancel func(error)) {
			res := 0
			for v := range pipe {
				res += v
				fmt.Println("reducer:", v)
			}
			writer.Write(res)
		}).
		WithWorkers(101)
}

func testMrStruct() {
	mr := ExampleMr()
	res, err := mr.Run()
	if err != nil {
		fmt.Println(err)
		return
	}
	fmt.Println(res)
}

func testNoMr() {
	wg := sync.WaitGroup{}
	ch := make(chan int)
	wg.Add(1)
	go func() {
		for i := 0; i < 100; i++ {
			ch <- i
		}
		close(ch)
		wg.Done()
	}()
	pipe := make(chan int)
	wg.Add(1)
	go func() {
		wg2 := sync.WaitGroup{}
		for i := range ch {
			wg2.Add(1)
			go func(i int) {
				fmt.Println("mapper:", i)
				time.Sleep(time.Second)
				pipe <- i
				wg2.Done()
			}(i)
		}
		go func() {
			wg2.Wait()
			close(pipe)
		}()
		wg.Done()
	}()
	wg.Add(1)
	go func() {
		for i := range pipe {
			fmt.Println("reducer:", i)
		}
		wg.Done()
	}()
	wg.Wait()
}

func main() {
	st := time.Now()
	defer func() {
		fmt.Println("spend time : ", time.Now().Sub(st))
	}()
	b1 := b[int]{}
	b2 := b[string]{}
	c1 := c{Arr: []a{b1, b2}}
	c1.print()
}

type a interface {
	Print()
}
type b[T any] struct {
	data T
}

func (receiver b[T]) Print() {
	fmt.Println(reflect.TypeOf(receiver.data))
}

type c struct {
	Arr []a
}

func (r c) print() {
	for _, v := range r.Arr {
		v.Print()
	}
}

func Add[T any](cs c, bs b[T]) {
	cs.Arr = append(cs.Arr, bs)
}
