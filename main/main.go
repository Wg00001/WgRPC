package main

import (
	wgRPC "WgRPC"
	"log"
	"net"
	"net/http"
	"sync"
	"time"
)

type Foo int

type Args struct{ Num1, Num2 int }

func (f Foo) Sum(args Args, reply *int) error {
	*reply = args.Num1 + args.Num2
	return nil
}

func startServer(l net.Listener) {
	var foo Foo
	_ = wgRPC.Register(&foo)
	wgRPC.HandleHTTP()
	_ = http.Serve(l, nil)

}
func call(l net.Listener) {
	client, _ := wgRPC.DialHTTP("tcp", l.Addr().String())
	defer func() { _ = client.Close() }()

	time.Sleep(time.Second)
	// send request & receive response
	var wg sync.WaitGroup
	for i := 0; i < 5; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			args := &Args{Num1: i, Num2: i * i}
			var reply int
			if err := client.Call("Foo.Sum", args, &reply); err != nil {
				log.Fatal("call Foo.Sum error:", err)
			}
			log.Printf("%d + %d = %d", args.Num1, args.Num2, reply)
		}(i)
	}
	wg.Wait()
}

func main() {
	log.SetFlags(0)
	l, _ := net.Listen("tcp", ":9999")
	go call(l)
	startServer(l)
}
