package main

import (
	"WgRPC"
	"WgRPC/codec"
	"encoding/json"
	"fmt"
	"log"
	"net"
	"time"
)

func startServer(addr chan string) {
	//检测一手
	l, err := net.Listen("tcp", ":0")
	if err != nil {
		log.Fatal("main.startServer: network ERR: ", err)
	}
	addr <- l.Addr().String()
	wgRPC.Accept(l)
	log.Println("start rpc server on ", l.Addr())
}

func main() {
	addr := make(chan string)
	go startServer(addr)

	conn, _ := net.Dial("tcp", <-addr)
	defer conn.Close()

	time.Sleep(time.Second)
	//发送Option进行协议交换
	json.NewEncoder(conn).Encode(wgRPC.DefaultOption)
	c := codec.NewGobCodec(conn)
	for i := 0; i < 5; i++ {
		//发送消息头
		h := &codec.Header{
			ServiceMethod: "Foo.Sum",
			Seq:           uint64(i),
		}
		//发送消息体
		c.Write(h, fmt.Sprintf("geerpc req %d", h.Seq))
		c.ReadHeader(h)
		//解析响应并答应
		var reply string
		c.ReadBody(&reply)
		log.Println("reply:", reply)
	}
}
