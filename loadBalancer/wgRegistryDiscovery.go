package loadBalancer

import (
	"log"
	"net/http"
	"strings"
	"time"
)

/**
 * @description: 连接注册中心的负载均衡服务端
 * @author Wg
 * @date 2024/7/2
 */

// WgRegistryDiscovery 嵌套了MultiServersDiscovery，很多能力可以复用
type WgRegistryDiscovery struct {
	*MultiServerDiscovery
	registry   string        //注册中心地址
	timeout    time.Duration //服务列表的过期时间
	lastUpdate time.Time     //最后从注册中心更新服务列表的时间
}

// 默认十秒过期
const defaultUpdateTimeout = time.Second * 10

func NewWgRegistryDiscovery(registerAddr string, timeout time.Duration) *WgRegistryDiscovery {
	if timeout == 0 {
		timeout = defaultUpdateTimeout
	}
	d := &WgRegistryDiscovery{
		MultiServerDiscovery: NewMultiServerDiscovery(make([]string, 0)),
		registry:             registerAddr,
		timeout:              timeout,
	}
	return d
}

func (receiver *WgRegistryDiscovery) Update(servers []string) error {
	receiver.mutex.Lock()
	defer receiver.mutex.Unlock()
	receiver.servers = servers
	receiver.lastUpdate = time.Now()
	return nil
}

// Refresh 超时重新获取
func (receiver *WgRegistryDiscovery) Refresh() error {
	receiver.mutex.Lock()
	defer receiver.mutex.Unlock()
	if receiver.lastUpdate.Add(receiver.timeout).After(time.Now()) {
		return nil
	}
	log.Println("ERR: loadBalancer.loadBalancer.Refresh: refresh servers from registry: ", receiver.registry)
	resp, err := http.Get(receiver.registry)
	if err != nil {
		log.Println("ERR: loadBalancer.loadBalancer.Refresh: refresh err: ", err)
		return err
	}
	servers := strings.Split(resp.Header.Get("X-Wgrpc-Servers"), ",")
	receiver.servers = make([]string, 0, len(servers))
	for _, server := range servers {
		if strings.TrimSpace(server) != "" {
			receiver.servers = append(receiver.servers, strings.TrimSpace(server))
		}
	}
	receiver.lastUpdate = time.Now()
	return nil
}

// Get 需先调用Refresh确保服务列表没有过期
func (receiver *WgRegistryDiscovery) Get(mode SelectMode) (string, error) {
	if err := receiver.Refresh(); err != nil {
		return "", err
	}
	return receiver.MultiServerDiscovery.Get(mode)
}

func (receiver *WgRegistryDiscovery) GetAll() ([]string, error) {
	if err := receiver.Refresh(); err != nil {
		return nil, err
	}
	return receiver.MultiServerDiscovery.GetAll()
}
