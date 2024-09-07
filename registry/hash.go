package registry

import (
	"hash/crc32"
	"log"
	"sort"
	"strconv"
	"time"
)

/**
 * Package registry
 * @File : hash.go
 * @Author : Wg
 */
/*
Chord算法实现一致性哈希
*/

const defaultReplicas = 3

type ServerItem struct {
	Addr  string
	start time.Time
}

type Hash func(data []byte) uint32

// ServerMap 路由表
type ServerMap struct {
	hash     Hash //依赖注入，允许用于替换成自定义的Hash函数，默认为crc32.ChecksumIEEE算法
	replicas int  //虚拟节点倍数，虚拟节点用于扩充节点数量以解决数据倾斜问题
	keys     []int
	hashMap  map[int]*ServerItem
	timeout  time.Duration
}

// NewServerMap 虚拟节点倍数需要自定义
func NewServerMap(replicas int) *ServerMap {
	return &ServerMap{
		replicas: replicas,
		hash:     crc32.ChecksumIEEE,
		hashMap:  make(map[int]*ServerItem),
	}
}

func (m *ServerMap) SetHashFunc(hash Hash) {
	if hash == nil {
		log.Printf("hash func can't not be nil")
		return
	}
	m.hash = hash
}

// Add 增加节点，根据数字+地址进行hash计算
func (m *ServerMap) Add(addrs ...string) {
	for _, addr := range addrs {
		item := &ServerItem{addr, time.Now()}
		for i := 0; i < m.replicas; i++ {
			//通过序号+地址进行hash
			hash := int(m.hash([]byte(strconv.Itoa(i) + addr)))
			m.keys = append(m.keys, hash)
			m.hashMap[hash] = item
		}
	}
	sort.Ints(m.keys)
}

// Get 选择节点
func (m *ServerMap) Get(key string) string {
	if len(m.keys) == 0 {
		return ""
	}
	hash := int(m.hash([]byte(key)))

	//使用二分查询搜索合适的节点
	idx := sort.Search(len(m.keys), func(i int) bool {
		return m.keys[i] >= hash
	})
	//检查心跳过期
	for m.hashMap[m.keys[idx%len(m.keys)]].start.Add(m.timeout).Before(time.Now()) {
		//todo:删除key
		delete(m.hashMap, m.keys[idx%len(m.keys)])
		idx = sort.Search(len(m.keys), func(i int) bool {
			return m.keys[i] >= hash
		})
	}
	return m.hashMap[m.keys[idx%len(m.keys)]].Addr
}

func (m *ServerMap) Del(addr string) {
	//todo:删除Key
	delete(m.hashMap, int(m.hash([]byte(addr))))
}

func (m *ServerMap) heatBeat() error {
	//todo：心跳
	return nil
}
