package nacos

import (
	"context"
	"fmt"
	"github.com/nacos-group/nacos-sdk-go/v2/clients/config_client"
	"github.com/openimsdk/tools/discovery"
	"github.com/openimsdk/tools/errs"
	"net"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/nacos-group/nacos-sdk-go/v2/clients"
	"github.com/nacos-group/nacos-sdk-go/v2/clients/naming_client"
	"github.com/nacos-group/nacos-sdk-go/v2/common/constant"
	"github.com/nacos-group/nacos-sdk-go/v2/vo"
	"github.com/openimsdk/tools/utils/datautil"
	"google.golang.org/grpc"
)

type addrConn struct {
	conn        *grpc.ClientConn
	addr        string
	isConnected bool
}

// NacosSvcDiscoveryRegistryImpl for Nacos
type NacosSvcDiscoveryRegistryImpl struct {
	client            naming_client.INamingClient
	configClient      config_client.IConfigClient
	dialOptions       []grpc.DialOption
	serviceKey        string
	rpcRegisterTarget string

	watchNames []string
	rootGroup  string

	mu      sync.RWMutex
	connMap map[string][]*addrConn
}

// NewSvcDiscoveryRegistry create a new Nacos registry client
func NewSvcDiscoveryRegistry(serverConfigs []constant.ServerConfig, clientConfig constant.ClientConfig,
	rootGroup string, watchNames []string, opts ...grpc.DialOption) (*NacosSvcDiscoveryRegistryImpl, error) {

	client, err := clients.NewNamingClient(
		vo.NacosClientParam{
			ClientConfig:  &clientConfig,
			ServerConfigs: serverConfigs,
		},
	)
	if err != nil {
		return nil, err
	}

	configClient, err := clients.NewConfigClient(
		vo.NacosClientParam{
			ClientConfig:  &clientConfig,
			ServerConfigs: serverConfigs,
		},
	)
	if err != nil {
		return nil, err
	}

	s := &NacosSvcDiscoveryRegistryImpl{
		client:       client,
		configClient: configClient,
		connMap:      make(map[string][]*addrConn),
		watchNames:   watchNames,
		rootGroup:    rootGroup,
		dialOptions:  opts,
	}

	s.watchServiceChanges()
	return s, nil
}

// GetConns returns gRPC connections for a service
func (r *NacosSvcDiscoveryRegistryImpl) GetConns(ctx context.Context, serviceName string, opts ...grpc.DialOption) ([]grpc.ClientConnInterface, error) {
	fullServiceKey := fmt.Sprintf("%s/%s", r.rootGroup, serviceName)

	r.mu.RLock()
	if len(r.connMap) == 0 {
		r.mu.RUnlock()
		if err := r.initializeConnMap(serviceName, opts...); err != nil {
			return nil, err
		}
		r.mu.RLock()
	}
	defer r.mu.RUnlock()

	return datautil.Batch(func(t *addrConn) grpc.ClientConnInterface { return t.conn }, r.connMap[fullServiceKey]), nil
}

// GetConn returns a single gRPC connection
func (r *NacosSvcDiscoveryRegistryImpl) GetConn(ctx context.Context, serviceName string, opts ...grpc.DialOption) (grpc.ClientConnInterface, error) {
	conns, err := r.GetConns(ctx, serviceName, opts...)
	if err != nil {
		return nil, err
	}
	if len(conns) == 0 {
		return nil, fmt.Errorf("no connection available for service %s", serviceName)
	}
	return conns[0], nil
}

// IsSelfNode check if the connection is self
func (r *NacosSvcDiscoveryRegistryImpl) IsSelfNode(cc grpc.ClientConnInterface) bool {
	cli, ok := cc.(*grpc.ClientConn)
	if !ok {
		return false
	}
	return r.rpcRegisterTarget == cli.Target()
}

// AddOption append grpc options
func (r *NacosSvcDiscoveryRegistryImpl) AddOption(opts ...grpc.DialOption) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.dialOptions = append(r.dialOptions, opts...)
	r.resetConnMap()
}

// Register registers service to Nacos
func (r *NacosSvcDiscoveryRegistryImpl) Register(ctx context.Context, serviceName, host string, port int, opts ...grpc.DialOption) error {
	r.serviceKey = fmt.Sprintf("%s/%s", r.rootGroup, serviceName)
	r.rpcRegisterTarget = fmt.Sprintf("%s:%d", host, port)

	_, err := r.client.RegisterInstance(vo.RegisterInstanceParam{
		Ip:          host,
		Port:        uint64(port),
		ServiceName: serviceName,
		GroupName:   r.rootGroup,
		Enable:      true,
		Weight:      1.0,
		Healthy:     true,
	})
	return err
}

// UnRegister unregisters service from Nacos
func (r *NacosSvcDiscoveryRegistryImpl) UnRegister() error {
	if r.serviceKey == "" {
		return fmt.Errorf("serviceKey is empty")
	}
	host, portStr, err := net.SplitHostPort(r.rpcRegisterTarget)
	if err != nil {
		return err
	}
	port, _ := strconv.Atoi(portStr)

	success, err := r.client.DeregisterInstance(vo.DeregisterInstanceParam{
		Ip:          host,
		Port:        uint64(port),
		ServiceName: strings.TrimPrefix(r.serviceKey, r.rootGroup+"/"),
		GroupName:   r.rootGroup,
	})

	if !success {
		return fmt.Errorf("failed to unregister instance")
	}
	if err != nil {
		return err
	}
	return nil
}

// Close clears connections (Nacos client does not need explicit close)
func (r *NacosSvcDiscoveryRegistryImpl) Close() {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.resetConnMap()
}

// initializeConnMap fetches all instances and dials them
func (r *NacosSvcDiscoveryRegistryImpl) initializeConnMap(serviceName string, opts ...grpc.DialOption) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	resp, err := r.client.SelectInstances(vo.SelectInstancesParam{
		ServiceName: serviceName,
		GroupName:   r.rootGroup,
		HealthyOnly: true,
	})
	if err != nil {
		return err
	}

	newList := make([]*addrConn, 0, len(resp))
	for _, ins := range resp {
		addr := fmt.Sprintf("%s:%d", ins.Ip, ins.Port)
		dialOpts := append(r.dialOptions, opts...)
		conn, err := grpc.Dial(addr, dialOpts...)
		if err != nil {
			continue
		}
		newList = append(newList, &addrConn{conn: conn, addr: addr})
	}

	r.connMap[r.rootGroup+"/"+serviceName] = newList
	return nil
}

// watchServiceChanges sets up watches for all services
func (r *NacosSvcDiscoveryRegistryImpl) watchServiceChanges() {
	for _, s := range r.watchNames {
		go func(serviceName string) {
			for {
				_ = r.initializeConnMap(serviceName)
				time.Sleep(5 * time.Second) // poll every 5s
			}
		}(s)
	}
}

func (r *NacosSvcDiscoveryRegistryImpl) resetConnMap() {
	for _, conns := range r.connMap {
		for _, c := range conns {
			_ = c.conn.Close()
		}
	}
	r.connMap = make(map[string][]*addrConn)
}

func (r *NacosSvcDiscoveryRegistryImpl) GetUserIdHashGatewayHost(ctx context.Context, userId string) (string, error) {
	// TODO: 根据你业务计算hash映射到服务实例
	return "", nil
}

// SetKey 将 key/value 写入 Nacos 配置中心
func (r *NacosSvcDiscoveryRegistryImpl) SetKey(ctx context.Context, key string, data []byte) error {
	success, err := r.configClient.PublishConfig(vo.ConfigParam{
		DataId:  key,
		Group:   r.rootGroup,
		Content: string(data),
	})
	if err != nil {
		return errs.WrapMsg(err, "nacos publish config error")
	}
	if !success {
		return errs.New("nacos publish config failed", "key", key)
	}
	return nil
}

// SetWithLease Nacos 配置中心没有 TTL，直接调用 SetKey
func (r *NacosSvcDiscoveryRegistryImpl) SetWithLease(ctx context.Context, key string, val []byte, ttl int64) error {
	return r.SetKey(ctx, key, val)
}

// GetKey 获取 key 对应的值
func (r *NacosSvcDiscoveryRegistryImpl) GetKey(ctx context.Context, key string) ([]byte, error) {
	content, err := r.configClient.GetConfig(vo.ConfigParam{
		DataId: key,
		Group:  r.rootGroup,
	})
	if err != nil {
		return nil, errs.WrapMsg(err, "nacos get config error")
	}
	if content == "" {
		return nil, nil
	}
	return []byte(content), nil
}

// GetKeyWithPrefix Nacos 配置中心不支持 prefix 查询，这里只能返回单个值
func (r *NacosSvcDiscoveryRegistryImpl) GetKeyWithPrefix(ctx context.Context, key string) ([][]byte, error) {
	val, err := r.GetKey(ctx, key)
	if err != nil {
		return nil, err
	}
	if val == nil {
		return nil, nil
	}
	return [][]byte{val}, nil
}

// DelData 删除配置中心中的 key
func (r *NacosSvcDiscoveryRegistryImpl) DelData(ctx context.Context, key string) error {
	success, err := r.configClient.DeleteConfig(vo.ConfigParam{
		DataId: key,
		Group:  r.rootGroup,
	})
	if err != nil {
		return errs.WrapMsg(err, "nacos delete config error")
	}
	if !success {
		return errs.New("nacos delete config failed", "key", key)
	}
	return nil
}

// WatchKey 监听配置中心 key 的变化
func (r *NacosSvcDiscoveryRegistryImpl) WatchKey(ctx context.Context, key string, fn discovery.WatchKeyHandler) error {
	err := r.configClient.ListenConfig(vo.ConfigParam{
		DataId: key,
		Group:  r.rootGroup,
		OnChange: func(namespace, group, dataId, data string) {
			_ = fn(&discovery.WatchKey{
				Value: []byte(data),
			})
		},
	})
	if err != nil {
		return errs.WrapMsg(err, "nacos listen config error")
	}
	return nil
}
