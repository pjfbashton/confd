// Copyright (c) 2013 Kelsey Hightower. All rights reserved.
// Use of this source code is governed by the Apache License, Version 2.0
// that can be found in the LICENSE file.
package config

import (
	"errors"
	"flag"
	"net"
	"net/url"
	"path/filepath"
	"strconv"

	"github.com/BurntSushi/toml"
	"github.com/kelseyhightower/confd/log"
)

var (
	clientCert string
	clientKey  string
	config     Config // holds the global confd config.
	confdir    string
	etcdNodes  Nodes
	etcdScheme string
	interval   int
	noop       bool
	prefix     string
	srvDomain  string
)

// Config represents the confd configuration settings.
type Config struct {
	Confd confd
}

// confd represents the parsed configuration settings.
type confd struct {
	ClientCert string `toml:"client_cert"`
	ClientKey  string `toml:"client_key"`
	ConfDir    string
	EtcdNodes  []string `toml:"etcd_nodes"`
	EtcdScheme string   `toml:"etcd_scheme"`
	Interval   int
	Noop       bool `toml:"noop"`
	Prefix     string
	SRVDomain  string `toml:"srv_domain"`
}

func init() {
	flag.StringVar(&clientCert, "client-cert", "", "the client cert")
	flag.StringVar(&clientKey, "client-key", "", "the client key")
	flag.StringVar(&confdir, "confdir", "/etc/confd", "confd conf directory")
	flag.Var(&etcdNodes, "node", "list of etcd nodes")
	flag.StringVar(&etcdScheme, "etcd-scheme", "http", "the etcd URI scheme. (http or https)")
	flag.IntVar(&interval, "interval", 600, "etcd polling interval")
	flag.BoolVar(&noop, "noop", false, "only show pending changes, don't sync configs.")
	flag.StringVar(&prefix, "prefix", "/", "etcd key path prefix")
	flag.StringVar(&srvDomain, "srv-domain", "", "the domain to query for the etcd SRV record, i.e. example.com")
}

// LoadConfig initializes the confd configuration by first setting defaults,
// then overriding setting from the confd config file, and finally overriding
// settings from flags set on the command line.
// It returns an error if any.
func LoadConfig(path string) error {
	setDefaults()
	if path == "" {
		log.Warning("Skipping confd config file.")
	} else {
		log.Debug("Loading " + path)
		_, err := toml.DecodeFile(path, &config)
		if err != nil {
			return err
		}
	}
	processFlags()
	if !isValidateEtcdScheme(config.Confd.EtcdScheme) {
		return errors.New("Invalid etcd scheme: " + config.Confd.EtcdScheme)
	}
	err := setEtcdHosts()
	if err != nil {
		return err
	}
	return nil
}

// ClientCert returns the client cert path.
func ClientCert() string {
	return config.Confd.ClientCert
}

// ClientKey returns the client key path.
func ClientKey() string {
	return config.Confd.ClientKey
}

// ConfigDir returns the path to the confd config dir.
func ConfigDir() string {
	return filepath.Join(config.Confd.ConfDir, "conf.d")
}

// EtcdNodes returns a list of etcd node url strings.
// For example: ["http://203.0.113.30:4001"]
func EtcdNodes() []string {
	return config.Confd.EtcdNodes
}

// Interval returns the number of seconds to wait between configuration runs.
func Interval() int {
	return config.Confd.Interval
}

// Noop returns the state of noop mode.
func Noop() bool {
	return config.Confd.Noop
}

// Prefix returns the etcd key prefix to use when querying etcd.
func Prefix() string {
	return config.Confd.Prefix
}

// SetConfDir sets the confd conf dir.
func SetConfDir(path string) {
	config.Confd.ConfDir = path
}

// SetNoop sets noop.
func SetNoop(enabled bool) {
	config.Confd.Noop = enabled
}

// SetPrefix sets the key prefix.
func SetPrefix(prefix string) {
	config.Confd.Prefix = prefix
}

// SRVDomain returns the domain name used in etcd SRV record lookups.
func SRVDomain() string {
	return config.Confd.SRVDomain
}

// TemplateDir returns the template directory path.
func TemplateDir() string {
	return filepath.Join(config.Confd.ConfDir, "templates")
}

func setDefaults() {
	config = Config{
		Confd: confd{
			ConfDir:    "/etc/confd",
			Interval:   600,
			Prefix:     "/",
			EtcdNodes:  []string{"127.0.0.1:4001"},
			EtcdScheme: "http",
		},
	}
}

// setEtcdHosts.
func setEtcdHosts() error {
	scheme := config.Confd.EtcdScheme
	hosts := make([]string, 0)
	// If a domain name is given then lookup the etcd SRV record, and override
	// all other etcd node settings.
	if config.Confd.SRVDomain != "" {
		etcdHosts, err := getEtcdHostsFromSRV(config.Confd.SRVDomain)
		if err != nil {
			return errors.New("Cannot get etcd hosts from SRV records " + err.Error())
		}
		for _, h := range etcdHosts {
			uri := formatEtcdHostURL(scheme, h.Hostname, strconv.FormatUint(uint64(h.Port), 10))
			hosts = append(hosts, uri)
		}
		config.Confd.EtcdNodes = hosts
		return nil
	}
	// No domain name was given, so just process the etcd node list.
	// An etcdNode can be a URL, http://etcd.example.com:4001, or a host, etcd.example.com:4001.
	for _, node := range config.Confd.EtcdNodes {
		etcdURL, err := url.Parse(node)
		if err != nil {
			log.Error(err.Error())
			return err
		}
		if etcdURL.Scheme != "" && etcdURL.Host != "" {
			if !isValidateEtcdScheme(etcdURL.Scheme) {
				return errors.New("The etcd node list contains an invalid URL: " + node)
			}
			host, port, err := net.SplitHostPort(etcdURL.Host)
			if err != nil {
				return err
			}
			hosts = append(hosts, formatEtcdHostURL(etcdURL.Scheme, host, port))
			continue
		}
		// At this point node is not an etcd URL, i.e. http://etcd.example.com:4001,
		// but a host:port string, i.e. etcd.example.com:4001
		host, port, err := net.SplitHostPort(node)
		if err != nil {
			return err
		}
		hosts = append(hosts, formatEtcdHostURL(scheme, host, port))
	}
	config.Confd.EtcdNodes = hosts
	return nil
}

// processFlags iterates through each flag set on the command line and
// overrides corresponding configuration settings.
func processFlags() {
	flag.Visit(setConfigFromFlag)
}

func setConfigFromFlag(f *flag.Flag) {
	switch f.Name {
	case "client-cert":
		config.Confd.ClientCert = clientCert
	case "client-key":
		config.Confd.ClientKey = clientKey
	case "confdir":
		config.Confd.ConfDir = confdir
	case "node":
		config.Confd.EtcdNodes = etcdNodes
	case "etcd-scheme":
		config.Confd.EtcdScheme = etcdScheme
	case "interval":
		config.Confd.Interval = interval
	case "noop":
		config.Confd.Noop = noop
	case "prefix":
		config.Confd.Prefix = prefix
	case "srv-domain":
		config.Confd.SRVDomain = srvDomain
	}
}
