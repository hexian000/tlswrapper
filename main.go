package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"io/ioutil"
	"log"
	"os"
	"os/signal"
	"syscall"
	"time"
)

type logWriter struct {
}

func (writer logWriter) Write(bytes []byte) (int, error) {
	return fmt.Printf("%s %s", time.Now().Format(time.RFC3339), string(bytes))
}

func parseFlags() *Config {
	var flagHelp bool
	var flagConfig string
	flag.BoolVar(&flagHelp, "h", false, "help")
	flag.StringVar(&flagConfig, "c", "", "config file")
	flag.Parse()
	if flagHelp || flagConfig == "" {
		flag.Usage()
		os.Exit(0)
	}
	log.SetFlags(0)
	log.SetOutput(new(logWriter))
	b, err := ioutil.ReadFile(flagConfig)
	if err != nil {
		log.Fatalln("read config:", err)
	}
	cfg := defaultConfig
	if err := json.Unmarshal(b, &cfg); err != nil {
		log.Fatalln("parse config:", err)
	}
	return &cfg
}

func newProtocol(cfg *Config) Protocol {
	switch cfg.Mode {
	case "server":
		return &ServerProtocol{cfg}
	case "client":
		return &ClientProtocol{cfg}
	}
	log.Fatalln("unsupported mode:", cfg.Mode)
	return nil
}

func main() {
	cfg := parseFlags()
	server := &Server{Config: cfg, Protocol: newProtocol(cfg)}
	if err := server.Start(); err != nil {
		log.Fatalln(err)
	}

	ch := make(chan os.Signal)
	signal.Notify(ch, os.Interrupt, syscall.SIGTERM)

	<-ch
	_ = server.Shutdown()
}
