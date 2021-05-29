package websockets

import (
	"net/http"
	"sync"
	"sync/atomic"
	"time"

	"github.com/fasthttp/websocket"
	"github.com/google/uuid"
	endure "github.com/spiral/endure/pkg/container"
	"github.com/spiral/errors"
	"github.com/spiral/roadrunner/v2/pkg/pubsub"
	"github.com/spiral/roadrunner/v2/plugins/channel"
	"github.com/spiral/roadrunner/v2/plugins/config"
	"github.com/spiral/roadrunner/v2/plugins/http/attributes"
	"github.com/spiral/roadrunner/v2/plugins/logger"
	"github.com/spiral/roadrunner/v2/plugins/websockets/connection"
	"github.com/spiral/roadrunner/v2/plugins/websockets/executor"
	"github.com/spiral/roadrunner/v2/plugins/websockets/pool"
	"github.com/spiral/roadrunner/v2/plugins/websockets/storage"
	"github.com/spiral/roadrunner/v2/plugins/websockets/validator"
)

const (
	PluginName string = "websockets"
)

type Plugin struct {
	mu sync.RWMutex
	// Collection with all available pubsubs
	pubsubs map[string]pubsub.PubSub

	Config *Config
	log    logger.Logger

	// global connections map
	connections sync.Map
	storage     *storage.Storage

	// GO workers pool
	workersPool *pool.WorkersPool
	stopped     uint64

	hub channel.Hub
}

func (p *Plugin) Init(cfg config.Configurer, log logger.Logger, channel channel.Hub) error {
	const op = errors.Op("websockets_plugin_init")
	if !cfg.Has(PluginName) {
		return errors.E(op, errors.Disabled)
	}

	err := cfg.UnmarshalKey(PluginName, &p.Config)
	if err != nil {
		return errors.E(op, err)
	}

	p.pubsubs = make(map[string]pubsub.PubSub)
	p.log = log
	p.storage = storage.NewStorage()
	p.workersPool = pool.NewWorkersPool(p.storage, &p.connections, log)
	p.hub = channel
	p.stopped = 0

	return nil
}

func (p *Plugin) Serve() chan error {
	errCh := make(chan error)

	// run all pubsubs drivers
	for _, v := range p.pubsubs {
		go func(ps pubsub.PubSub) {
			for {
				data, err := ps.Next()
				if err != nil {
					errCh <- err
					return
				}

				p.workersPool.Queue(data)
			}
		}(v)
	}
	return errCh
}

func (p *Plugin) Stop() error {
	atomic.AddUint64(&p.stopped, 1)
	p.workersPool.Stop()
	return nil
}

func (p *Plugin) Collects() []interface{} {
	return []interface{}{
		p.GetPublishers,
	}
}

func (p *Plugin) Available() {}

func (p *Plugin) RPC() interface{} {
	return &rpc{
		plugin: p,
		log:    p.log,
	}
}

func (p *Plugin) Name() string {
	return PluginName
}

// GetPublishers collects all pubsubs
func (p *Plugin) GetPublishers(name endure.Named, pub pubsub.PubSub) {
	p.pubsubs[name.Name()] = pub
}

func (p *Plugin) Middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != p.Config.Path {
			next.ServeHTTP(w, r)
			return
		}

		if atomic.CompareAndSwapUint64(&p.stopped, 1, 1) {
			// plugin stopped
			return
		}

		r = attributes.Init(r)

		err := validator.NewValidator().AssertServerAccess(p.hub, r)
		if err != nil {
			// show the error to the user
			if av, ok := err.(*validator.AccessValidator); ok {
				av.Copy(w)
			} else {
				w.WriteHeader(400)
				return
			}
		}

		// connection upgrader
		upgraded := websocket.Upgrader{
			HandshakeTimeout:  time.Second * 60,
			ReadBufferSize:    0,
			WriteBufferSize:   0,
			WriteBufferPool:   nil,
			Subprotocols:      nil,
			Error:             nil,
			CheckOrigin:       nil,
			EnableCompression: false,
		}

		// upgrade connection to websocket connection
		_conn, err := upgraded.Upgrade(w, r, nil)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}

		// construct safe connection protected by mutexes
		safeConn := connection.NewConnection(_conn, p.log)
		defer func() {
			// close the connection on exit
			err = safeConn.Close()
			if err != nil {
				p.log.Error("connection close error", "error", err)
			}
		}()

		// generate UUID from the connection
		connectionID := uuid.NewString()
		// store connection
		p.connections.Store(connectionID, safeConn)
		// when exiting - delete the connection
		defer func() {
			p.connections.Delete(connectionID)
		}()

		p.mu.Lock()
		// Executor wraps a connection to have a safe abstraction
		e := executor.NewExecutor(safeConn, p.log, p.storage, connectionID, p.pubsubs, p.hub, r)
		p.mu.Unlock()

		p.log.Info("websocket client connected", "uuid", connectionID)

		defer e.CleanUp()

		err = e.StartCommandLoop()
		if err != nil {
			p.log.Error("command loop error", "error", err.Error())
			return
		}
	})
}

// Publish is an entry point to the websocket PUBSUB
func (p *Plugin) Publish(msg []*pubsub.Message) error {
	p.mu.Lock()
	defer p.mu.Unlock()

	for i := 0; i < len(msg); i++ {
		for j := 0; j < len(msg[i].Topics); j++ {
			if br, ok := p.pubsubs[msg[i].Broker]; ok {
				err := br.Publish(msg)
				if err != nil {
					return errors.E(err)
				}
			} else {
				p.log.Warn("no such broker", "available", p.pubsubs, "requested", msg[i].Broker)
			}
		}
	}
	return nil
}

func (p *Plugin) PublishAsync(msg []*pubsub.Message) {
	go func() {
		p.mu.Lock()
		defer p.mu.Unlock()
		for i := 0; i < len(msg); i++ {
			for j := 0; j < len(msg[i].Topics); j++ {
				err := p.pubsubs[msg[i].Broker].Publish(msg)
				if err != nil {
					p.log.Error("publish async error", "error", err)
					return
				}
			}
		}
	}()
}
