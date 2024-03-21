package registry

import (
	"log"
	"net/http"
	"sort"
	"strings"
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

type ServerItem struct {
	Addr  string
	start time.Time
}

// WgRegistry 注册中心，提供关注的功能
type WgRegistry struct {
	timeout time.Duration
	mutex   sync.Mutex
	servers map[string]*ServerItem //服务器
}

const (
	defaultPath    = "/wgrpc/registry"
	defaultTimeout = time.Minute * 5
)

func NewWgRegistry(timeout time.Duration) *WgRegistry {
	return &WgRegistry{
		servers: make(map[string]*ServerItem),
		timeout: timeout,
	}
}

var DefaultWgRegister = NewWgRegistry(defaultTimeout)

// 添加服务实例，若服务存在则更新start
func (w *WgRegistry) putServer(addr string) {
	w.mutex.Lock()
	defer w.mutex.Unlock()
	s := w.servers[addr]
	if s == nil {
		w.servers[addr] = &ServerItem{
			Addr:  addr,
			start: time.Now(),
		}
	} else {
		s.start = time.Now() //存在的话，更新start time
	}
}

// 返回还活着的可用的服务列表，若存在超时的服务则删除
func (w *WgRegistry) aliveServers() []string {
	w.mutex.Lock()
	defer w.mutex.Unlock()
	var alive []string
	for addr, s := range w.servers {
		if w.timeout == 0 || s.start.Add(w.timeout).After(time.Now()) {
			alive = append(alive, addr)
		} else {
			delete(w.servers, addr)
		}
	}
	sort.Strings(alive)
	return alive
}

func (w *WgRegistry) ServeHTTP(rw http.ResponseWriter, req *http.Request) {
	// 为了更简洁，用HTTP协议提供服务，将所有信息承载于HTTP Header
	switch req.Method {
	case "GET":
		rw.Header().Set("X-Wgrpc-Servers", strings.Join(w.aliveServers(), ","))
	case "POST":
		addr := req.Header.Get("X-Wgrpc-Server")
		if addr == "" {
			rw.WriteHeader(http.StatusInternalServerError)
			return
		}
		w.putServer(addr)
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
