package rpc

import (
	"net"
	"net/rpc"
	"sync/atomic"

	"github.com/spiral/endure"
	"github.com/spiral/errors"
	goridgeRpc "github.com/spiral/goridge/v3/pkg/rpc"
	"github.com/spiral/roadrunner/v2/plugins/config"
	"github.com/spiral/roadrunner/v2/plugins/logger"
)

// PluginName contains default plugin name.
const PluginName = "RPC"

type pluggable struct {
	service RPCer
	name    string
}

// Plugin is RPC service.
type Plugin struct {
	cfg Config
	log logger.Logger
	rpc *rpc.Server
	// set of the plugins, which are implement RPCer interface and can be plugged into the RR via RPC
	plugins  []pluggable
	listener net.Listener
	closed   *uint32
}

// Init rpc service. Must return true if service is enabled.
func (s *Plugin) Init(cfg config.Configurer, log logger.Logger) error {
	const op = errors.Op("rpc_plugin_init")
	if !cfg.Has(PluginName) {
		return errors.E(op, errors.Disabled)
	}

	err := cfg.UnmarshalKey(PluginName, &s.cfg)
	if err != nil {
		return errors.E(op, errors.Disabled, err)
	}
	s.cfg.InitDefaults()

	s.log = log
	state := uint32(0)
	s.closed = &state
	atomic.StoreUint32(s.closed, 0)

	return s.cfg.Valid()
}

// Serve serves the service.
func (s *Plugin) Serve() chan error {
	const op = errors.Op("rpc_plugin_serve")
	errCh := make(chan error, 1)

	s.rpc = rpc.NewServer()

	services := make([]string, 0, len(s.plugins))

	// Attach all services
	for i := 0; i < len(s.plugins); i++ {
		err := s.Register(s.plugins[i].name, s.plugins[i].service.RPC())
		if err != nil {
			errCh <- errors.E(op, err)
			return errCh
		}

		services = append(services, s.plugins[i].name)
	}

	var err error
	s.listener, err = s.cfg.Listener()
	if err != nil {
		errCh <- err
		return errCh
	}

	s.log.Debug("Started RPC service", "address", s.cfg.Listen, "services", services)

	go func() {
		for {
			conn, err := s.listener.Accept()
			if err != nil {
				if atomic.LoadUint32(s.closed) == 1 {
					// just log and continue, this is not a critical issue, we just called Stop
					s.log.Warn("listener accept error, connection closed", "error", err)
					return
				}

				s.log.Error("listener accept error", "error", err)
				errCh <- errors.E(errors.Op("listener accept"), errors.Serve, err)
				return
			}

			go s.rpc.ServeCodec(goridgeRpc.NewCodec(conn))
		}
	}()

	return errCh
}

// Stop stops the service.
func (s *Plugin) Stop() error {
	const op = errors.Op("rpc_plugin_stop")
	// store closed state
	atomic.StoreUint32(s.closed, 1)
	err := s.listener.Close()
	if err != nil {
		return errors.E(op, err)
	}
	return nil
}

// Name contains service name.
func (s *Plugin) Name() string {
	return PluginName
}

// Depends declares services to collect for RPC.
func (s *Plugin) Collects() []interface{} {
	return []interface{}{
		s.RegisterPlugin,
	}
}

// RegisterPlugin registers RPC service plugin.
func (s *Plugin) RegisterPlugin(name endure.Named, p RPCer) {
	s.plugins = append(s.plugins, pluggable{
		service: p,
		name:    name.Name(),
	})
}

// Register publishes in the server the set of methods of the
// receiver value that satisfy the following conditions:
//	- exported method of exported type
//	- two arguments, both of exported type
//	- the second argument is a pointer
//	- one return value, of type error
// It returns an error if the receiver is not an exported type or has
// no suitable methods. It also logs the error using package log.
func (s *Plugin) Register(name string, svc interface{}) error {
	if s.rpc == nil {
		return errors.E("RPC service is not configured")
	}

	return s.rpc.RegisterName(name, svc)
}

// Client creates new RPC client.
func (s *Plugin) Client() (*rpc.Client, error) {
	conn, err := s.cfg.Dialer()
	if err != nil {
		return nil, err
	}

	return rpc.NewClientWithCodec(goridgeRpc.NewClientCodec(conn)), nil
}
