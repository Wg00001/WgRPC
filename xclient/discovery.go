package xclient

import (
	"errors"
	"math"
	"math/rand"
	"sync"
	"time"
)

type SelectMode int //不同的负载均衡策略（本项目只实现Random 和 RoundRobin）

const (
	RandomSelect SelectMode = iota
	RoundRobinSelect
)

type Discovery interface {
	Refresh() error                      //从注册中心更新服务表
	Update(servers []string) error       //手动更新服务列表
	Get(mode SelectMode) (string, error) //根据负载均衡策略，选择服务实例
	GetAll() ([]string, error)           //返回所有服务实例
}

// MultiServerDiscovery 一个不需要注册中心，服务列表由手工维护的服务发现结构体
// 用户显示提供服务器地址
type MultiServerDiscovery struct {
	r       *rand.Rand // 用于生成随机数
	mutex   sync.RWMutex
	servers []string
	index   int //用于记录Robin算法的选定位置
}

// NewMultiServerDiscovery 构造函数
func NewMultiServerDiscovery(servers []string) *MultiServerDiscovery {
	m := &MultiServerDiscovery{
		servers: servers,
		//用时间戳设定随机数种子，以免每次生成相同随机数序列
		r: rand.New(rand.NewSource(time.Now().UnixNano())),
	}
	//index初始化时随机设定一个值
	m.index = m.r.Intn(math.MaxInt32 - 1)
	return m
}

// 实现接口(将结构体赋给接口，完成实例化接口的交接仪式)
var _ Discovery = (*MultiServerDiscovery)(nil)

// Refresh 刷新对MultiServerDiscovery没有意义
func (m *MultiServerDiscovery) Refresh() error {
	return nil
}

func (m *MultiServerDiscovery) Update(servers []string) error {
	m.mutex.RLock()
	defer m.mutex.RUnlock()
	m.servers = servers
	return nil
}

func (m *MultiServerDiscovery) Get(mode SelectMode) (string, error) {
	m.mutex.Lock()
	defer m.mutex.Unlock()
	n := len(m.servers)
	if n == 0 {
		return "", errors.New("ERR:xclient.discovery.MultiServerDiscovery.Get: no available servers")
	}
	switch mode {
	case RandomSelect:
		return m.servers[m.r.Intn(n)], nil
	case RoundRobinSelect:
		s := m.servers[m.index%n] //server可能更新，所以%n一下确保安全
		m.index = (m.index + 1) % n
		return s, nil
	default:
		return "", errors.New("ERR:xclient.discovery.MultiServerDiscovery.Get: not supported select mode")
	}
}

func (m *MultiServerDiscovery) GetAll() ([]string, error) {
	m.mutex.Lock()
	defer m.mutex.Unlock()
	servers := make([]string, len(m.servers), len(m.servers))
	copy(servers, m.servers)
	return servers, nil
}
