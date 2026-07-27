package config

import "time"

type ServerConfig struct {
	Port         int           `yaml:"port"`
	ReadTimeout  time.Duration `yaml:"read_timeout"`
	WriteTimeout time.Duration `yaml:"write_timeout"`
}

type Config struct {
	Server ServerConfig `yaml:"server"`
}

func Load(path string) (*Config, error) {
	// TODO: implement YAML config loading in task 2.1
	return &Config{
		Server: ServerConfig{
			Port:         8080,
			ReadTimeout:  30 * time.Second,
			WriteTimeout: 60 * time.Second,
		},
	}, nil
}