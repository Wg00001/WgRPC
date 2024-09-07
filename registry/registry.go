package registry

import (
	"log"
	"net/http"
	"sync"
	"time"
)

/*
* 注册中心
客户端和服务端只需要感知注册中心的存在
1.服务端启动后，向注册中心发送注册消息，通知注册中心自己已经启动。服务端定时向注册中心发送心跳。
2.客户端向注册中心询问，当前哪天是可用的，注册中心将可用的服务列表返回客户端
3.客户端根据注册中心得到的服务列表，选择其中一个发起调用
*/

// WgRegistry 注册中心，提供关注的功能
type WgRegistry struct {
	timeout  time.Duration
	mutex    sync.Mutex
	servers  map[string]*ServerMap //服务名:服务map
	replicas int
}

const (
	defaultPath    = "/wgrpc/registry"
	defaultTimeout = time.Minute * 5
)

func NewWgRegistry(timeout time.Duration, replicas int) *WgRegistry {
	return &WgRegistry{
		servers:  make(map[string]*ServerMap),
		timeout:  timeout,
		replicas: replicas,
	}
}

var DefaultWgRegister = NewWgRegistry(defaultTimeout, 3)

// 添加服务实例
func (w *WgRegistry) putServer(servername, addr string) error {
	w.mutex.Lock()
	defer w.mutex.Unlock()
	if _, ok := w.servers[servername]; !ok {
		w.servers[servername] = NewServerMap(w.replicas)
	} else {
		if err := w.servers[servername].heatBeat(addr); err != nil {
			return err
		}
	}
	w.servers[servername].Add(addr)
	return nil
}

// 通过负载均衡策略找出可用实例，发送地址给客户端
func (w *WgRegistry) aliveServers(servername, remoteAddr string) string {
	w.mutex.Lock()
	defer w.mutex.Unlock()
	return w.servers[servername].Get(remoteAddr)
}

// 为了更简洁，用HTTP协议提供服务，将所有信息承载于HTTP Header
func (w *WgRegistry) ServeHTTP(rw http.ResponseWriter, req *http.Request) {

	switch req.Method {
	case "GET":
		rw.Header().Set("X-WgRPC-Server-Addr",
			w.aliveServers(
				//请求头中存放需要的服务名，根据请求的ip进行hash
				req.Header.Get("X-WgRPC-Server-Name"),
				req.RemoteAddr+req.Header.Get("X-Real-IP"),
			),
		)
	case "POST":
		addr := req.Header.Get("X-WgRPC-Server-Addr")
		servername := req.Header.Get("X-WgRPC-Server-Name")
		if addr == "" {
			rw.WriteHeader(http.StatusInternalServerError)
			return
		}
		w.putServer(servername, addr)
	default:
		rw.WriteHeader(http.StatusMethodNotAllowed)
	}
}

// HandleHTTP 在registryPath上为WgRegistry注册HTTP处理程序
func (w *WgRegistry) HandleHTTP(registryPath string) {
	http.Handle(registryPath, w)
	log.Println("rpc registry path:", registryPath)
}

func HandleHTTP() {
	DefaultWgRegister.HandleHTTP(defaultPath)
}

// Heartbeat 心跳
func Heartbeat(registry, addr string, duration time.Duration) {
	if duration == 0 {
		duration = defaultTimeout - time.Duration(1)*time.Minute
	}
	var err error
	err = sendHeartbeat(registry, addr)
	go func() {
		t := time.NewTicker(duration)
		for err == nil {
			<-t.C
			err = sendHeartbeat(registry, addr)
		}
	}()
}

func sendHeartbeat(registry, addr string) error {
	log.Println(addr, " send heart beat to registry", registry)
	httpClient := &http.Client{}
	req, _ := http.NewRequest("POST", registry, nil)
	req.Header.Set("X-Wgrpc-Server", addr)
	if _, err := httpClient.Do(req); err != nil {
		log.Println("ERR: registry.registry.sendHeartbeat: ", err)
		return err
	}
	return nil
}

//todo:超时处理
