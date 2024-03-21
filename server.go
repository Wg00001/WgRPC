package wgRPC

import (
	"WgRPC/codec"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"reflect"
	"strings"
	"sync"
	"time"
)

const (
	MagicNumber      = 0x03719666 //检验用的神奇妙妙数字
	connected        = "200 Connected to Wg RPC"
	defaultRPCPath   = "/wgrpc"
	defaultDebugPath = "/debug/wgrpc"
)

/**
本客户端采用JSON编码的Option，后续header和body的编码方式由Option中的CodeType指定
服务端先用JSON解码Option，再解码剩余内容
*/

type Option struct {
	MagicNumber    int32
	CodecType      codec.Type
	ConnectTimeout time.Duration //time.Duration用于表示持续时间`
	HandleTimeout  time.Duration
}

var DefaultOption = &Option{
	MagicNumber:    MagicNumber,
	CodecType:      codec.GobType,
	ConnectTimeout: time.Second * 10, //设置默认值为10s
	//HandleTimeout不设置默认值，即为0秒
}

type Server struct {
	serviceMap sync.Map
}

func NewServer() *Server {
	return &Server{}
}

var DefaultServer = NewServer()

func Accept(listener net.Listener) { DefaultServer.Accept(listener) }

func (server *Server) Accept(listner net.Listener) {
	//建立socket连接
	for {
		conn, err := listner.Accept()
		if err != nil {
			log.Println("server.Accept:", err)
			return
		}
		//开启子携程处理连接
		go server.ServeConn(conn)
	}
}

// ServeConn 连接服务器
func (server *Server) ServeConn(conn io.ReadWriteCloser) {
	defer conn.Close()
	var opt Option
	//根据CodeType得到对应的消息编解码器
	if err := json.NewDecoder(conn).Decode(&opt); err != nil {
		log.Println("server.ServeConn: option ERR:", err)
		return
	}
	//验证妙妙数字
	if opt.MagicNumber != MagicNumber {
		log.Printf("server.ServeConn: magic number ERR:%x \n", opt.MagicNumber)
		return
	}
	//创建map并验证数据类型
	f := codec.NewCodecFuncMap[opt.CodecType]
	if f == nil {
		log.Printf("server.ServeConn: invalid codec type ERR: %s \n", opt.CodecType)
		return
	}
	server.serveCodec(f(conn), &opt)
}

// 如果发生错误则发送这个空body给客户端
var invalidRequest = struct{}{}

func (server *Server) serveCodec(codec codec.Codec, opt *Option) {
	sending := new(sync.Mutex)
	wg := new(sync.WaitGroup) //等待所有请求被处理完，
	//循环直到发生错误，这使得一次链接可以接收多个请求
	for {
		//读取请求
		req, err := server.readRequest(codec)
		if err != nil {
			if req == nil {
				break
			}
			req.header.Error = err.Error()
			//发送请求
			server.sendResponse(codec, req.header, invalidRequest, sending)
			continue
		}
		wg.Add(1)
		//处理请求
		go server.handleRequest(codec, req, sending, wg, opt.HandleTimeout)
	}
	wg.Wait()
	codec.Close()
}

func (server *Server) Register(rcvr interface{}) error {
	s := newService(rcvr)
	if _, dup := server.serviceMap.LoadOrStore(s.name, s); dup {
		return errors.New("server.Register: service already defined: " + s.name)
	}
	return nil
}

// Register 公共接口，用于注册方法
func Register(rcvr interface{}) error {
	return DefaultServer.Register(rcvr)
}

// 通过serviceMethod从serviceMap中找到对应service
func (server *Server) findService(serviceMethod string) (svc *service, mtype *methodType, err error) {
	dot := strings.LastIndex(serviceMethod, ".")
	if dot < 0 {
		err = errors.New("server.Register: service/method request ill-formed: " + serviceMethod)
		return
	}
	serviceName, methodName := serviceMethod[:dot], serviceMethod[dot+1:]
	//读取对应的service
	svci, ok := server.serviceMap.Load(serviceName)
	if !ok {
		err = errors.New("server.findService: can't find service: " + serviceName)
		return
	}
	svc = svci.(*service)
	mtype = svc.method[methodName]
	if mtype == nil {
		err = errors.New("server.findService: can't find method: " + methodName)
	}
	return
}

// 请求体
type request struct {
	header       *codec.Header
	argv, replyV reflect.Value //反射获得类型
	mtype        *methodType
	svc          *service //服务注册
}

// 读取请求头
func (server *Server) readRequestHeader(c codec.Codec) (*codec.Header, error) {
	var header codec.Header
	if err := c.ReadHeader(&header); err != nil {
		if err != io.EOF && err != io.ErrUnexpectedEOF {
			log.Println("server.readRequestHeader: read header ERR:", err)
		}
		return nil, err
	}
	return &header, nil
}

// 读取请求
func (server *Server) readRequest(c codec.Codec) (*request, error) {
	header, err := server.readRequestHeader(c)
	if err != nil {
		return nil, err
	}
	//从server中读取出request
	req := &request{
		header: header,
	}
	req.svc, req.mtype, err = server.findService(header.ServiceMethod)
	if err != nil {
		return req, err
	}
	//创建入参实例
	req.argv = req.mtype.newArgv()
	req.replyV = req.mtype.newReplyv()
	argvi := req.argv.Interface()
	//确保argvi是指针类型
	if req.argv.Type().Kind() != reflect.Ptr {
		argvi = req.argv.Addr().Interface()
	}
	//将请求报文反序列化为第一个入参argv
	err = c.ReadBody(argvi)
	if err != nil {
		log.Println("server.readRequest: read body ERR: ", err)
		return req, err
	}
	return req, nil
}

// 发送响应
func (server *Server) sendResponse(c codec.Codec, header *codec.Header, body interface{}, sending *sync.Mutex) {
	sending.Lock()
	defer sending.Unlock()
	if err := c.Write(header, body); err != nil {
		log.Println("server.sendResponse: write response ERR: ", err)
	}
}

// 处理请求
func (server *Server) handleRequest(c codec.Codec, req *request, sending *sync.Mutex, wg *sync.WaitGroup, timeout time.Duration) {
	defer wg.Done()
	//将过程拆为call和sent两个阶段，以确保sendResponse仅调用一次
	called := make(chan struct{})
	sent := make(chan struct{})
	go func() {
		err := req.svc.call(req.mtype, req.argv, req.replyV)
		called <- struct{}{}
		if err != nil {
			req.header.Error = err.Error()
			server.sendResponse(c, req.header, invalidRequest, sending)
			sent <- struct{}{}
			return
		}
		server.sendResponse(c, req.header, req.replyV.Interface(), sending)
		sent <- struct{}{}
	}()
	if timeout == 0 {
		<-called
		<-sent
		return
	}
	select {
	//处理超时，则阻塞called和sent，调用sendResponse
	case <-time.After(timeout):
		req.header.Error = fmt.Sprintf("server.handleRequest: request handle timeout: expect within %s", timeout)
		server.sendResponse(c, req.header, invalidRequest, sending)
	case <-called:
		<-sent
	}
}

/**
支持HTTP协议
客户端向RPC服务器发送CONNECT请求，RPC应该返回HTTP200状态码表示连接建立
客户端用创建好的连接发送RPC报文，先发送Option，再发送N个请求报文，服务端处理RPC请求并响应
*/

// 绑定地址
func (server *Server) HandleHTTP() {
	http.Handle(defaultRPCPath, server)
	http.Handle(defaultDebugPath, debugHTTP{server})
	log.Println("server.HandleHTTP: server debug path: ", defaultDebugPath)
}

func HandleHTTP() {
	DefaultServer.HandleHTTP()
}

// 实现一个http.Handler将requests发送给RPC
func (server *Server) ServeHTTP(w http.ResponseWriter, req *http.Request) {
	if req.Method != "CONNECT" {
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		w.WriteHeader(http.StatusMethodNotAllowed)
		io.WriteString(w, "405 must CONNECT\n")
		return
	}
	conn, _, err := w.(http.Hijacker).Hijack()
	if err != nil {
		log.Print("server.serveHTTP: hijacking ERR: ", req.RemoteAddr, ": ", err.Error())
		return
	}
	io.WriteString(conn, "HTTP/1.0 "+connected+"\n\n")
	server.ServeConn(conn)
}
