package fileserver

import (
	"context"
	"sync"
	"time"

	"github.com/gofiber/fiber/v2"
	"github.com/gofiber/fiber/v2/middleware/etag"
	"github.com/roadrunner-server/errors"
	"github.com/roadrunner-server/sdk/v4/utils"
	"go.uber.org/zap"
)

const pluginName string = "fileserver"

type Configurer interface {
	// UnmarshalKey takes a single key and unmarshal it into a Struct.
	UnmarshalKey(name string, out any) error
	// Has checks if config section exists.
	Has(name string) bool
}

type Logger interface {
	NamedLogger(name string) *zap.Logger
}

type Plugin struct {
	sync.Mutex
	config *Config

	log *zap.Logger
	app *fiber.App
}

func (p *Plugin) Init(cfg Configurer, log Logger) error {
	const op = errors.Op("file_server_init")

	if !cfg.Has(pluginName) {
		return errors.E(op, errors.Disabled)
	}

	err := cfg.UnmarshalKey(pluginName, &p.config)
	if err != nil {
		return errors.E(op, err)
	}

	p.log = log.NamedLogger(pluginName)

	return nil
}

func (p *Plugin) Serve() chan error {
	errCh := make(chan error, 1)

	p.Lock()
	p.app = fiber.New(fiber.Config{
		ReadBufferSize:               1 * 1024 * 1024,
		WriteBufferSize:              1 * 1024 * 1024,
		Prefork:                      false,
		BodyLimit:                    10 * 1024 * 1024,
		ReadTimeout:                  time.Second * 10,
		WriteTimeout:                 time.Second * 10,
		DisableKeepalive:             false,
		DisableDefaultDate:           false,
		DisableDefaultContentType:    false,
		DisableHeaderNormalizing:     false,
		DisableStartupMessage:        true,
		StreamRequestBody:            p.config.StreamRequestBody,
		DisablePreParseMultipartForm: false,
		ReduceMemoryUsage:            false,
	})

	if p.config.CalculateEtag {
		p.app.Use(etag.New(etag.Config{
			Weak: p.config.Weak,
		}))
	}

	for i := 0; i < len(p.config.Configuration); i++ {
		p.app.Static(p.config.Configuration[i].Prefix, p.config.Configuration[i].Root, fiber.Static{
			Compress:      p.config.Configuration[i].Compress,
			ByteRange:     p.config.Configuration[i].BytesRange,
			Browse:        false,
			CacheDuration: time.Second * time.Duration(p.config.Configuration[i].CacheDuration),
			MaxAge:        p.config.Configuration[i].MaxAge,
		})
	}

	ln, err := utils.CreateListener(p.config.Address)
	if err != nil {
		errCh <- err
		return errCh
	}

	go func() {
		p.Unlock()
		p.log.Info("file server started", zap.String("address", p.config.Address))
		err = p.app.Listener(ln)
		if err != nil {
			errCh <- err
			return
		}
	}()

	return errCh
}

func (p *Plugin) Stop(ctx context.Context) error {
	endCh := make(chan struct{}, 1)
	errCh := make(chan error, 1)

	go func() {
		p.Lock()
		defer p.Unlock()

		err := p.app.Shutdown()
		if err != nil {
			errCh <- err
			return
		}

		endCh <- struct{}{}
	}()

	select {
	case <-ctx.Done():
		return ctx.Err()
	case e := <-errCh:
		return e
	case <-endCh:
		return nil
	}
}

func (p *Plugin) Name() string {
	return pluginName
}
