package fileserver

import (
	"context"
	"log/slog"
	"sync"
	"time"

	"github.com/gofiber/fiber/v2"
	"github.com/gofiber/fiber/v2/middleware/etag"
	"github.com/roadrunner-server/errors"
	"github.com/roadrunner-server/tcplisten"
)

const pluginName string = "fileserver"

type Configurer interface {
	// UnmarshalKey takes a single key and unmarshal it into a Struct.
	UnmarshalKey(name string, out any) error
	// Has checks if a config section exists.
	Has(name string) bool
}

type Logger interface {
	NamedLogger(name string) *slog.Logger
}

type Plugin struct {
	sync.Mutex
	config *Config

	log *slog.Logger
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

	if err = p.config.Valid(); err != nil {
		return errors.E(op, err)
	}

	p.log = log.NamedLogger(pluginName)

	return nil
}

func (p *Plugin) Serve() chan error {
	errCh := make(chan error, 1)

	p.Lock()
	p.app = fiber.New(fiber.Config{
		ReadBufferSize:        1 * 1024 * 1024,
		WriteBufferSize:       1 * 1024 * 1024,
		BodyLimit:             10 * 1024 * 1024,
		ReadTimeout:           time.Second * 10,
		WriteTimeout:          time.Second * 10,
		DisableStartupMessage: true,
		StreamRequestBody:     p.config.StreamRequestBody,
	})

	if p.config.CalculateEtag {
		p.app.Use(etag.New(etag.Config{
			Weak: p.config.Weak,
		}))
	}

	for _, cfg := range p.config.Configuration {
		p.app.Static(cfg.Prefix, cfg.Root, fiber.Static{
			Compress:      cfg.Compress,
			ByteRange:     cfg.BytesRange,
			Browse:        false,
			CacheDuration: time.Second * time.Duration(cfg.CacheDuration),
			MaxAge:        cfg.MaxAge,
		})
	}

	ln, err := tcplisten.CreateListener(p.config.Address)
	if err != nil {
		p.Unlock()
		errCh <- err
		return errCh
	}

	go func() {
		p.Unlock()
		p.log.Info("file server started", "address", p.config.Address)
		if err := p.app.Listener(ln); err != nil {
			errCh <- err
		}
	}()

	return errCh
}

func (p *Plugin) Stop(ctx context.Context) error {
	doneCh := make(chan error, 1)

	go func() {
		p.Lock()
		defer p.Unlock()
		doneCh <- p.app.ShutdownWithContext(ctx)
	}()

	select {
	case <-ctx.Done():
		return ctx.Err()
	case err := <-doneCh:
		return err
	}
}

func (p *Plugin) Name() string {
	return pluginName
}
