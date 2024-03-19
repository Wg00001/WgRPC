package wgRPC

import (
	"WgRPC/codec"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"sync"
	"time"
)

type Call struct {
	Seq           uint64
	ServiceMethod string
	Args          interface{} //函数的参数
	Reply         interface{} //回复
	Error         error
	Done          chan *Call //Call完成时放入chan中
}

// 调用结束后调用此函数通知调用方。用于支持异步调用。
func (call *Call) done() {
	call.Done <- call
}

type Client struct {
	c        codec.Codec //消息编解码器
	opt      *Option
	header   codec.Header //请求头
	sending  sync.Mutex   //保证请求有序发送
	mutex    sync.Mutex
	seq      uint64           //请求编号
	pending  map[uint64]*Call //未处理完的请求（key：编号，val：Call实例）
	closing  bool             //用户决定停止
	shutdown bool             //服务器通知停止
}

// 用于超时处理功能
type clientResult struct {
	client *Client
	err    error
}

//type newClientFunc func(conn net.Conn, option *Option) (client *Client, err error)

var _ io.Closer = (*Client)(nil)

var ErrShutdown = errors.New("connection is shut down")

// 关闭连接
func (client *Client) Close() error {
	client.mutex.Lock()
	defer client.mutex.Unlock()
	if client.closing {
		return ErrShutdown
	}
	client.closing = true
	return client.c.Close()
}

// 判断是否还在工作(是则返回True)
func (client *Client) IsAvailable() bool {
	client.mutex.Lock()
	defer client.mutex.Unlock()
	return !client.shutdown && !client.closing
}

// 将参数call添加到client.pending中，并更新client.seq
func (client *Client) registerCall(call *Call) (uint64, error) {
	client.mutex.Lock()
	defer client.mutex.Unlock()
	if client.closing || client.shutdown {
		return 0, ErrShutdown
	}
	call.Seq = client.seq
	client.pending[call.Seq] = call
	client.seq++
	return call.Seq, nil
}

// 根据seq从pending中获取对应的call
func (client *Client) removeCall(seq uint64) *Call {
	client.mutex.Lock()
	defer client.mutex.Unlock()
	call := client.pending[seq]
	delete(client.pending, seq)
	return call
}

// 客户端或服务端发生错误时调用，将客户端shutdown然后通知所有pending状态的call
func (client *Client) terminateCalls(err error) {
	client.sending.Lock()
	defer client.sending.Unlock()
	client.mutex.Lock()
	defer client.mutex.Unlock()
	client.shutdown = true
	for _, call := range client.pending {
		call.Error = err
		call.done()
	}
}

// 接收响应
func (client *Client) receive() {
	var err error
	for err == nil {
		var header codec.Header
		if err = client.c.ReadHeader(&header); err != nil {
			break
		}
		call := client.removeCall(header.Seq)
		switch {
		case call == nil: //call不存在
			err = client.c.ReadBody(nil)
		case header.Error != "": //call存在但是服务端处理错误，即header.Error不为空
			call.Error = fmt.Errorf(header.Error)
			err = client.c.ReadBody(nil)
			call.done()
		default: //服务端处理正常
			err = client.c.ReadBody(call.Reply)
			if err != nil {
				call.Error = errors.New("reading body" + err.Error())
			}
			call.done()
		}
	}
	//发生错误，则关掉pending中的所有Call
	client.terminateCalls(err)
}

// NewClient 创建Client实例
func NewClient(conn net.Conn, option *Option) (*Client, error) {
	//协议交换（发送Option信息给服务端，协商编解码方式）
	f := codec.NewCodecFuncMap[option.CodecType]
	if f == nil {
		err := fmt.Errorf("invalid codec type %s", option.CodecType)
		log.Println("client.NewClient: codec ERR: ", err)
		conn.Close()
		return nil, err
	}
	if err := json.NewEncoder(conn).Encode(option); err != nil {
		log.Println("client.NewClient: options ERR: ", err)
		conn.Close()
		return nil, err
	}
	client := &Client{
		c:       f(conn),
		opt:     option,
		seq:     1,
		pending: make(map[uint64]*Call),
	}
	//创建协程调用receive()接收响应
	go client.receive()
	return client, nil
}

// Dial 便于用户传入服务端地址
// 通过...*Option将Option实现为可选参数
func Dial(network, address string, options ...*Option) (client *Client, err error) {
	//解析option
	option, err := parseOptions(options...)
	if err != nil {
		return nil, err
	}
	//用net.DialTimeout防止超时（传入设置的时间）
	conn, err := net.DialTimeout(network, address, option.ConnectTimeout)
	if err != nil {
		return nil, err
	}
	defer func() {
		if err != nil {
			conn.Close()
		}
	}()
	ch := make(chan clientResult)
	go func() {
		client, err := NewClient(conn, option)
		ch <- clientResult{
			client: client,
			err:    err,
		}
	}()
	if option.ConnectTimeout == 0 {
		result := <-ch
		return result.client, result.err
	}
	select {
	case <-time.After(option.ConnectTimeout):
		return nil, fmt.Errorf("client.Dial: connect timeout: expect within %s", option.ConnectTimeout)
	case result := <-ch:
		return result.client, result.err
	}
}

// 解析option
func parseOptions(options ...*Option) (*Option, error) {
	if len(options) == 0 || options[0] == nil {
		return DefaultOption, nil
	}
	if len(options) != 1 {
		return nil, errors.New("number of options is more than 1")
	}
	option := options[0]
	option.MagicNumber = DefaultOption.MagicNumber
	if option.CodecType == "" {
		option.CodecType = DefaultOption.CodecType
	}
	return option, nil
}

// 发送请求
func (client *Client) send(call *Call) {
	client.sending.Lock()
	defer client.sending.Unlock()
	seq, err := client.registerCall(call)
	if err != nil {
		call.Error = err
		call.done()
		return
	}
	//组装header
	client.header.ServiceMethod = call.ServiceMethod
	client.header.Seq = seq
	client.header.Error = ""
	//编码并发送请求
	if err := client.c.Write(&client.header, call.Args); err != nil {
		call := client.removeCall(seq)
		if call != nil {
			call.Error = err
			call.done()
		}
	}
}

// Go RPC服务调用接口，异步接口，返回Call实例
func (client *Client) Go(serviceMethod string, args, reply interface{}, done chan *Call) *Call {
	if done == nil {
		done = make(chan *Call, 10)
	} else if cap(done) == 0 {
		log.Panic("client.go: done channel")
	}
	call := &Call{
		ServiceMethod: serviceMethod,
		Args:          args,
		Reply:         reply,
		Done:          done,
	}
	client.send(call)
	return call
}

// Call 对Go的封装，是一个同步接口：阻塞call.Done，等待响应返回协程调用go
func (client *Client) Call(serviceMethod string, args, reply interface{}, ctx ...context.Context) error {
	call := client.Go(serviceMethod, args, reply, make(chan *Call, 1))
	//用context包实现超时处理，控制权交给用户
	c := *new(context.Context)
	if len(ctx) != 0 {
		if len(ctx) != 1 {
			log.Println("WARING:client.call: Only the first context(time) will take effect")
		}
		c = ctx[0]
	} else {
		//默认为一秒，可以设置
		c, _ = context.WithTimeout(context.Background(), time.Second)
	}
	select {
	case <-c.Done():
		client.removeCall(call.Seq)
		return errors.New("client.Call: call failed: " + c.Err().Error())
	case call := <-call.Done:
		return call.Error
	}
}
