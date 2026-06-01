package nacos

import (
	"context"
	"fmt"
	"net"
	"strconv"
	"sync"
	"time"

	"github.com/nacos-group/nacos-sdk-go/v2/clients"
	"github.com/nacos-group/nacos-sdk-go/v2/clients/naming_client"
	"github.com/nacos-group/nacos-sdk-go/v2/common/constant"
	"github.com/nacos-group/nacos-sdk-go/v2/vo"
	"github.com/openimsdk/tools/discovery"
	"github.com/openimsdk/tools/errs"
	"github.com/openimsdk/tools/log"
	"github.com/openimsdk/tools/utils/datautil"
	"google.golang.org/grpc"
)

// CfgOption defines a function type for modifying nacos client configuration
type CfgOption func(*constant.ClientConfig)

type addrConn struct {
	conn        *grpc.ClientConn
	addr        string
	isConnected bool
}

// SvcDiscoveryRegistryImpl implementation for Nacos
type SvcDiscoveryRegistryImpl struct {
	client            naming_client.INamingClient
	dialOptions       []grpc.DialOption
	serviceKey        string
	rpcRegisterTarget string
	watchNames        []string

	rootDirectory string

	mu      sync.RWMutex
	connMap map[string][]*addrConn
}

// NewSvcDiscoveryRegistry creates a new service discovery registry implementation using Nacos
func NewSvcDiscoveryRegistry(rootDirectory string, serverConfig []constant.ServerConfig, watchNames []string, clientConfig constant.ClientConfig, options ...CfgOption) (*SvcDiscoveryRegistryImpl, error) {
	// Apply provided options to the client config
	for _, opt := range options {
		opt(&clientConfig)
	}

	// Create naming client
	namingClient, err := clients.CreateNamingClient(map[string]interface{}{
		"serverConfigs": serverConfig,
		"clientConfig":  clientConfig,
	})
	if err != nil {
		return nil, errs.WrapMsg(err, "failed to create nacos naming client")
	}

	s := &SvcDiscoveryRegistryImpl{
		client:        namingClient,
		rootDirectory: rootDirectory,
		connMap:       make(map[string][]*addrConn),
		watchNames:    watchNames,
	}

	// Initialize connection map for watched services
	if err := s.initializeConnMap(); err != nil {
		log.ZWarn(context.Background(), "initializeConnMap err", err)
	}

	// Start watching for service changes
	s.watchServiceChanges()

	return s, nil
}

// WithTimeout sets a custom timeout for the nacos client
func WithTimeout(timeout time.Duration) CfgOption {
	return func(cfg *constant.ClientConfig) {
		cfg.TimeoutMs = uint64(timeout.Milliseconds())
	}
}

// WithUsernameAndPassword sets a username and password for the nacos client
func WithUsernameAndPassword(username, password string) CfgOption {
	return func(cfg *constant.ClientConfig) {
		cfg.Username = username
		cfg.Password = password
	}
}

// WithNamespace sets the namespace for the nacos client
func WithNamespace(namespace string) CfgOption {
	return func(cfg *constant.ClientConfig) {
		cfg.NamespaceId = namespace
	}
}

// WithLogLevel sets the log level for the nacos client
func WithLogLevel(level string) CfgOption {
	return func(cfg *constant.ClientConfig) {
		cfg.LogLevel = level
	}
}

// GetUserIdHashGatewayHost returns the gateway host for a given user ID hash
func (r *SvcDiscoveryRegistryImpl) GetUserIdHashGatewayHost(ctx context.Context, userId string) (string, error) {
	return "", nil
}

// GetConns returns gRPC client connections for a given service name
func (r *SvcDiscoveryRegistryImpl) GetConns(ctx context.Context, serviceName string, opts ...grpc.DialOption) ([]grpc.ClientConnInterface, error) {
	fullServiceKey := fmt.Sprintf("%s/%s", r.rootDirectory, serviceName)
	r.mu.RLock()
	if len(r.connMap) == 0 {
		r.mu.RUnlock()
		if err := r.initializeConnMap(); err != nil {
			return nil, err
		}
		r.mu.RLock()
	}
	defer r.mu.RUnlock()
	return datautil.Batch(func(t *addrConn) grpc.ClientConnInterface { return t.conn }, r.connMap[fullServiceKey]), nil
}

// GetConn returns a single gRPC client connection for a given service name
func (r *SvcDiscoveryRegistryImpl) GetConn(ctx context.Context, serviceName string, opts ...grpc.DialOption) (grpc.ClientConnInterface, error) {
	// For Nacos, we'll use the direct connection approach
	dialOpts := append(r.dialOptions, opts...)

	// Get service instances
	instances, err := r.client.SelectInstances(vo.SelectInstancesParam{
		ServiceName: r.rootDirectory + "/" + serviceName,
		HealthyOnly: true,
	})
	if err != nil {
		return nil, errs.WrapMsg(err, "failed to select instances from nacos")
	}

	if len(instances) == 0 {
		return nil, fmt.Errorf("no healthy instances found for service: %s", serviceName)
	}

	// Use the first healthy instance
	instance := instances[0]
	addr := net.JoinHostPort(instance.Ip, strconv.Itoa(int(instance.Port)))

	return grpc.DialContext(ctx, addr, dialOpts...)
}

// GetSelfConnTarget returns the connection target for the current service
func (r *SvcDiscoveryRegistryImpl) GetSelfConnTarget() string {
	return r.rpcRegisterTarget
}

func (r *SvcDiscoveryRegistryImpl) IsSelfNode(cc grpc.ClientConnInterface) bool {
	cli, ok := cc.(*grpc.ClientConn)
	if !ok {
		return false
	}
	return r.GetSelfConnTarget() == cli.Target()
}

// AddOption appends gRPC dial options to the existing options
func (r *SvcDiscoveryRegistryImpl) AddOption(opts ...grpc.DialOption) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.resetConnMap()
	r.dialOptions = append(r.dialOptions, opts...)
}

// Register registers a new service endpoint with Nacos
func (r *SvcDiscoveryRegistryImpl) Register(ctx context.Context, serviceName, host string, port int, opts ...grpc.DialOption) error {
	r.serviceKey = fmt.Sprintf("%s/%s", r.rootDirectory, serviceName)
	r.rpcRegisterTarget = fmt.Sprintf("%s:%d", host, port)

	// Register service instance
	success, err := r.client.RegisterInstance(vo.RegisterInstanceParam{
		ServiceName: r.serviceKey,
		Ip:          host,
		Port:        uint64(port),
		Weight:      1,
		Enable:      true,
		Healthy:     true,
		Ephemeral:   true,
	})
	if err != nil {
		return errs.WrapMsg(err, "failed to register instance to nacos")
	}
	if !success {
		return fmt.Errorf("failed to register instance to nacos: registration returned false")
	}

	// Nacos v2 SDK handles heartbeats automatically when Ephemeral is true

	return nil
}

// initializeConnMap fetches all existing service instances and populates the local map
func (r *SvcDiscoveryRegistryImpl) initializeConnMap() error {
	r.mu.Lock()
	defer r.mu.Unlock()

	for _, name := range r.watchNames {
		fullServiceKey := fmt.Sprintf("%s/%s", r.rootDirectory, name)

		// Get all instances for the service
		instances, err := r.client.SelectInstances(vo.SelectInstancesParam{
			ServiceName: fullServiceKey,
			HealthyOnly: true,
		})
		if err != nil {
			return errs.WrapMsg(err, "failed to select instances for service "+name)
		}

		oldList := r.connMap[fullServiceKey]
		addrMap := make(map[string]*addrConn, len(oldList))
		for _, conn := range oldList {
			addrMap[conn.addr] = conn
		}

		newList := make([]*addrConn, 0, len(oldList))
		for _, instance := range instances {
			addr := net.JoinHostPort(instance.Ip, strconv.Itoa(int(instance.Port)))

			if _, _, err = net.SplitHostPort(addr); err != nil {
				continue
			}

			if conn, ok := addrMap[addr]; ok {
				conn.isConnected = true
				continue
			}

			dialOpts := append(r.dialOptions)
			conn, err := grpc.DialContext(context.Background(), addr, dialOpts...)
			if err != nil {
				log.ZWarn(context.Background(), "failed to dial instance", err, "addr", addr)
				continue
			}
			newList = append(newList, &addrConn{conn: conn, addr: addr, isConnected: false})
		}

		// Handle old connections that are no longer present
		for _, conn := range oldList {
			if conn.isConnected {
				conn.isConnected = false
				newList = append(newList, conn)
				continue
			}
			if err = conn.conn.Close(); err != nil {
				log.ZWarn(context.Background(), "close conn err", err)
			}
		}

		r.connMap[fullServiceKey] = newList
	}

	return nil
}

// watchServiceChanges watches for changes in the service directory
func (r *SvcDiscoveryRegistryImpl) watchServiceChanges() {
	// Nacos SDK automatically handles service updates through client-side caching
	// We'll periodically refresh the connection map
	ticker := time.NewTicker(10 * time.Second)
	go func() {
		for range ticker.C {
			if err := r.initializeConnMap(); err != nil {
				log.ZWarn(context.Background(), "initializeConnMap in watch err", err)
			}
		}
	}()
}

// UnRegister removes the service endpoint from Nacos
func (r *SvcDiscoveryRegistryImpl) UnRegister() error {
	if r.serviceKey == "" {
		return fmt.Errorf("no service registered")
	}

	// Parse host and port from the registered target
	host, portStr, err := net.SplitHostPort(r.rpcRegisterTarget)
	if err != nil {
		return errs.WrapMsg(err, "failed to parse registered target")
	}
	port, err := strconv.Atoi(portStr)
	if err != nil {
		return errs.WrapMsg(err, "failed to convert port to int")
	}

	// Deregister the service instance
	success, err := r.client.DeregisterInstance(vo.DeregisterInstanceParam{
		ServiceName: r.serviceKey,
		Ip:          host,
		Port:        uint64(port),
		Ephemeral:   true,
	})
	if err != nil {
		return errs.WrapMsg(err, "failed to deregister instance from nacos")
	}
	if !success {
		return fmt.Errorf("failed to deregister instance from nacos: deregistration returned false")
	}

	return nil
}

// Close closes the nacos client connection
func (r *SvcDiscoveryRegistryImpl) Close() {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.resetConnMap()
	// Nacos client doesn't need explicit close in the v2 SDK
}

func (r *SvcDiscoveryRegistryImpl) resetConnMap() {
	ctx := context.Background()
	for _, conn := range r.connMap {
		for _, c := range conn {
			if err := c.conn.Close(); err != nil {
				log.ZWarn(ctx, "failed to close conn", err)
			}
		}
	}
	r.connMap = make(map[string][]*addrConn)
}

// SetKey is not supported in Nacos naming client
func (r *SvcDiscoveryRegistryImpl) SetKey(ctx context.Context, key string, data []byte) error {
	return discovery.ErrNotSupported
}

// SetWithLease is not supported in Nacos naming client
func (r *SvcDiscoveryRegistryImpl) SetWithLease(ctx context.Context, key string, val []byte, ttl int64) error {
	return discovery.ErrNotSupported
}

// GetKey is not supported in Nacos naming client
func (r *SvcDiscoveryRegistryImpl) GetKey(ctx context.Context, key string) ([]byte, error) {
	return nil, discovery.ErrNotSupported
}

// GetKeyWithPrefix is not supported in Nacos naming client
func (r *SvcDiscoveryRegistryImpl) GetKeyWithPrefix(ctx context.Context, key string) ([][]byte, error) {
	return nil, discovery.ErrNotSupported
}

// DelData is not supported in Nacos naming client
func (r *SvcDiscoveryRegistryImpl) DelData(ctx context.Context, key string) error {
	return discovery.ErrNotSupported
}

// WatchKey is not supported in Nacos naming client
func (r *SvcDiscoveryRegistryImpl) WatchKey(ctx context.Context, key string, fn discovery.WatchKeyHandler) error {
	return discovery.ErrNotSupported
}
