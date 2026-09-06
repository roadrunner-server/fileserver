package fileserver

import (
	"fmt"
	"math"
	"reflect"
	"strconv"

	"github.com/roadrunner-server/errors"
	"github.com/roadrunner-server/tcplisten"
)

type Config struct {
	// Address to serve
	Address    string                       `mapstructure:"address"`
	UnixSocket *tcplisten.UnixSocketOptions `mapstructure:"unix_socket"`
	// CalculateEtag can be true/false and used to calculate etag for the static
	CalculateEtag bool `mapstructure:"calculate_etag"`
	// Weak etag `W/`
	Weak bool `mapstructure:"weak"`
	// per-root configuration
	Configuration []*Cfg `mapstructure:"serve"`
	// StreamRequestBody ...
	StreamRequestBody bool `mapstructure:"stream_request_body"`
}

type Cfg struct {
	// Prefix HTTP
	Prefix string `mapstructure:"prefix"`

	// Dir contains name of directory to control access to.
	// Default - "."
	Root string `mapstructure:"root"`

	BytesRange    bool `mapstructure:"bytes_range"`
	Compress      bool `mapstructure:"compress"`
	CacheDuration int  `mapstructure:"cache_duration"`
	MaxAge        int  `mapstructure:"max_age"`
}

func (c *Config) Valid() error {
	const op = errors.Op("static_validation")
	if c.Address == "" {
		return errors.E(op, errors.Str("empty address"))
	}

	if err := c.UnixSocket.Validate(c.Address); err != nil {
		return errors.E(op, fmt.Errorf("fileserver.unix_socket: %w", err))
	}

	if len(c.Configuration) == 0 {
		return errors.E(op, errors.Str("no configuration to serve"))
	}

	for _, cfg := range c.Configuration {
		if cfg.Prefix == "" {
			return errors.E(op, errors.Str("empty prefix"))
		}

		if cfg.Prefix[0] != '/' {
			return errors.E(op, errors.Str("prefix must begin with a forward slash"))
		}

		if cfg.Root == "" {
			cfg.Root = "."
		}

		if cfg.CacheDuration == 0 {
			cfg.CacheDuration = 10
		}
	}

	return nil
}

// Weak decoding into *int can convert booleans and fractions to IDs.
func validateUnixSocketIDs(cfg Configurer, key string) error {
	var raw map[string]any
	if err := cfg.UnmarshalKey(key, &raw); err != nil {
		return fmt.Errorf("%s: %w", key, err)
	}

	const maxID = 4294967295
	for _, field := range []string{"uid", "gid"} {
		if raw[field] == nil {
			continue
		}
		value := reflect.ValueOf(raw[field])
		valid := false
		switch value.Kind() { //nolint:exhaustive // Other kinds are not valid IDs.
		case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
			id := value.Int()
			valid = id >= 0 && id < maxID
		case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64, reflect.Uintptr:
			valid = value.Uint() < maxID
		case reflect.String:
			id, err := strconv.ParseInt(value.String(), 0, strconv.IntSize)
			valid = err == nil && id >= 0 && id < maxID
		case reflect.Float32, reflect.Float64:
			id := value.Float()
			valid = id >= 0 && id < maxID && id == math.Trunc(id)
		}
		if !valid {
			return fmt.Errorf("%s.%s: must be an integer from 0 through 4294967294", key, field)
		}
	}
	return nil
}
