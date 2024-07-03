package registry

import (
	"encoding/json"
	"io"
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
2.客户端向注册中心询问，当前哪些服务是可用的，注册中心将可用的服务列表返回客户端
3.客户端根据注册中心得到的服务列表，选择其中一个发起调用
*/

type ServerItem struct {
	Addr  string `json:"address"`
	start time.Time
}

// WgRegistry 注册中心，提供关注的功能
type WgRegistry struct {
	timeout time.Duration
	mutex   sync.Mutex
	servers map[string]*ServerItem //map[服务名]服务列表
}

const (
	defaultPath    = "/wgrpc/registry"
	defaultTimeout = time.Minute * 5
)

var DefaultWgRegistry = NewWgRegistry(defaultTimeout)

func NewWgRegistry(timeout time.Duration) *WgRegistry {
	return &WgRegistry{
		servers: make(map[string]*ServerItem),
		timeout: timeout,
	}
}

// 添加服务实例，若服务存在则更新start
func (w *WgRegistry) putServer(server *ServerItem) {
	w.mutex.Lock()
	defer w.mutex.Unlock()
	s := w.servers[server.Addr]
	if s == nil {
		server.start = time.Now()
		w.servers[server.Addr] = server
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
	// 用HTTP协议提供服务
	switch req.Method {
	//返回所有可用服务列表
	case "GET":
		w.getServer(rw, req)
	//注册服务
	case "POST":
		w.registerServer(rw, req)
	default:
		rw.WriteHeader(http.StatusMethodNotAllowed)
		rw.Write([]byte("Method not allowed"))
	}
}

func (w *WgRegistry) getServer(rw http.ResponseWriter, req *http.Request) {
	resp := strings.Join(w.aliveServers(), ",")
	rw.WriteHeader(http.StatusOK)
	rw.Write([]byte(resp))
}

func (w *WgRegistry) registerServer(rw http.ResponseWriter, req *http.Request) {
	body, err := io.ReadAll(req.Body)
	defer req.Body.Close()
	if err != nil {
		rw.WriteHeader(http.StatusInternalServerError)
		rw.Write([]byte("Failed to read request body"))
		return
	}
	var server *ServerItem
	err = json.Unmarshal(body, &server)
	if err != nil || strings.TrimSpace(server.Addr) == "" {
		rw.WriteHeader(http.StatusInternalServerError)
		rw.Write([]byte("Invalid JSON or missing address field"))
		return
	}
	w.putServer(server)
	rw.WriteHeader(http.StatusOK)
	rw.Write([]byte("Server registered successfully"))
}

// HandleHTTP 在registryPath上为WgRegistry注册HTTP处理程序
func (w *WgRegistry) HandleHTTP(registryPath string) {
	http.Handle(registryPath, w)
	log.Println("rpc registry path:", registryPath)
}

func HandleHTTP() {
	DefaultWgRegistry.HandleHTTP(defaultPath)
}

// Heartbeat 心跳
func Heartbeat(registry, addr string, duration time.Duration) error {
	if duration == 0 {
		duration = defaultTimeout - time.Duration(1)*time.Minute
	}
	var err error
	err = sendHeartbeat(registry, addr)
	if err != nil {
		return err
	}
	go func() {
		t := time.NewTicker(duration)
		for err == nil {
			<-t.C
			err = sendHeartbeat(registry, addr)
			if err != nil {
				log.Println("registry Heartbeat Error:", err)
			}
		}
	}()
	return nil
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
