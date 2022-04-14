package xclient

import (
	"errors"
	"math"
	"math/rand"
	"sync"
	"time"
)

// SelectMode 负载均衡策略
type SelectMode int

const (
	RandomSelect SelectMode = iota
	RoundRobinSelect
)

// Discovery 服务发现接口
type Discovery interface {
	Refresh() error                      // 从注册中心更新服务列表
	Update(servers []string) error       // 手动更新服务列表
	Get(mode SelectMode) (string, error) // 根据负载均衡策略选择服务实例
	GetAll() ([]string, error)           // 返回所有服务实例
}

// MultiServersDiscovery 手动维护server列表
type MultiServersDiscovery struct {
	r       *rand.Rand
	mu      sync.RWMutex
	servers []string
	index   int
}

// Refresh 没有注册中心的情况下无意义，不实现
func (m *MultiServersDiscovery) Refresh() error {
	return nil
}

// Update 更新服务列表
func (m *MultiServersDiscovery) Update(servers []string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.servers = servers
	return nil
}

// Get 获取server地址
func (m *MultiServersDiscovery) Get(mode SelectMode) (string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	n := len(m.servers)
	if n == 0 {
		return "", errors.New("rpc discovery: no available servers")
	}
	switch mode {
	case RandomSelect:
		{
			return m.servers[m.r.Intn(n)], nil
		}
	case RoundRobinSelect:
		{
			s := m.servers[m.index%n]
			m.index = (m.index + 1) % n
			return s, nil
		}
	default:
		{
			return "", errors.New("rpc discovery: not supported select mode")
		}
	}
}

func (m *MultiServersDiscovery) GetAll() ([]string, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	servers := make([]string, len(m.servers), len(m.servers))
	copy(servers, m.servers)
	return servers, nil
}

func NewMultiServerDiscovery(servers []string) *MultiServersDiscovery {
	d := &MultiServersDiscovery{
		servers: servers,
		r:       rand.New(rand.NewSource(time.Now().UnixNano())),
	}
	d.index = d.r.Intn(math.MaxInt32 - 1)
	return d
}

var _ Discovery = (*MultiServersDiscovery)(nil)
