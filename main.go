package main

import (
	"encoding/json"
	"flag"
	"io/ioutil"
	"log"
	"os"
	"os/signal"
	"strings"
)

func init() {
	log.SetFlags(log.LstdFlags | log.Lshortfile)
}

func parseFlags() []Config {
	var flagHelp bool
	var flagConfig string
	flag.BoolVar(&flagHelp, "h", false, "help")
	flag.StringVar(&flagConfig, "c", "", "config file")
	flag.Parse()
	if flagHelp || flagConfig == "" {
		flag.Usage()
		os.Exit(0)
	}
	configs := make([]Config, 0)
	files := strings.Split(flagConfig, ":")
	for _, file := range files {
		log.Println("read config:", file)
		b, err := ioutil.ReadFile(file)
		if err != nil {
			log.Fatalln("read config:", err)
		}
		cfg := defaultConfig
		if err := json.Unmarshal(b, &cfg); err != nil {
			log.Fatalln("parse config:", err)
		}
		configs = append(configs, cfg)
	}
	return configs
}

func main() {
	configs := parseFlags()
	log.Printf("starting %d servers\n", len(configs))
	servers := make([]*Server, 0, len(configs))
	for i := range configs {
		servers = append(servers, NewServer(&configs[i]))
	}
	for _, server := range servers {
		if err := server.Start(); err != nil {
			log.Fatalln(err)
		}
	}

	ch := make(chan os.Signal, 1)
	signal.Notify(ch, os.Interrupt)
	<-ch

	for _, server := range servers {
		if err := server.Shutdown(); err != nil {
			log.Println(err)
		}
	}
}
