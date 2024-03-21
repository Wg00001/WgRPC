package xclient

import (
	. "WgRPC"
	"context"
	"io"
	"reflect"
	"runtime"
	"sync"
)

/**
一个支持负载均衡的客户端XClient
*/

type XClient struct {
	discovery Discovery
	mode      SelectMode
	opt       *Option
	mutex     sync.Mutex
	//保存创建好的Client实例，以复用socket
	clients map[string]*Client //key:rpcAddr，val:*Client
}

func NewXClient(discovery Discovery, mode SelectMode, option *Option) *XClient {
	return &XClient{
		discovery: discovery,
		mode:      mode,
		opt:       option,
		clients:   make(map[string]*Client),
	}
}

// 实现io.Closer接口
var _ io.Closer = (*XClient)(nil)

func (x *XClient) Close() error {
	x.mutex.Lock()
	defer x.mutex.Unlock()
	for k, v := range x.clients {
		//关闭客户端
		v.Close()
		delete(x.clients, k)
	}
	return nil
}

// 从x.clients中取出可用的Client，实现Client的复用
func (x *XClient) dial(rpcAddr string) (*Client, error) {
	x.mutex.Lock()
	defer x.mutex.Unlock()
	client, ok := x.clients[rpcAddr]
	//检查x.clients是否有缓存的Client，有则检查其可用状态
	if ok && !client.IsAvailable() {
		//不可用，从缓存中删除
		client.Close()
		delete(x.clients, rpcAddr)
		client = nil
	}
	if client == nil {
		var err error
		client, err = XDial(rpcAddr, x.opt)
		if err != nil {
			return nil, err
		}
		x.clients[rpcAddr] = client
	}
	//可用，返回缓存的Client
	return client, nil
}

func (x *XClient) call(rpcAddr string, serviceMethod string, args, reply interface{}, ctx ...context.Context) error {
	client, err := x.dial(rpcAddr)
	if err != nil {
		return err
	}
	//调用client.Call
	return client.Call(serviceMethod, args, reply, ctx...)
}

func (x *XClient) Call(serviceMethod string, args, reply interface{}, ctx ...context.Context) error {
	rpcAddr, err := x.discovery.Get(x.mode)
	if err != nil {
		return err
	}
	return x.call(rpcAddr, serviceMethod, args, reply, ctx...)
}

// Broadcast 将请求广播到所有的server
// 如果有实例发生错误则返回其中一个错误；调用成功则返回其中一个结果
// 请求是并发的，使用互斥锁保证error和reply被正确赋值
func (x *XClient) Broadcast(serviceMethod string, args, reply interface{}, ctxs ...context.Context) error {
	servers, err := x.discovery.GetAll()
	if err != nil {
		return err
	}
	var wg sync.WaitGroup
	var mu sync.Mutex
	var e error
	replyDone := reply == nil //如果reply==nil，replyDone=true
	//context.WithCancel 确保有错误发生时，快速失败。
	ctx, _ := context.WithCancel(ctxs[0])
	for _, rpcAddr := range servers {
		wg.Add(1)
		go func(rpcAddr string) {
			defer wg.Done()
			var clonedReply interface{}
			if reply != nil {
				clonedReply = reflect.New(reflect.ValueOf(reply).Elem().Type()).Interface()
			}
			err := x.call(rpcAddr, serviceMethod, args, clonedReply, ctx)
			mu.Lock()
			if err != nil && e == nil {
				e = err
				//cancel()
				runtime.Goexit()
			}
			if err == nil && !replyDone {
				reflect.ValueOf(reply).Elem().Set(reflect.ValueOf(clonedReply).Elem())
				replyDone = true
			}
			mu.Unlock()
		}(rpcAddr)
	}
	wg.Wait()
	return e
}
