package main

import (
	"encoding/json"
	"flag"
	"io/ioutil"
	"log"
	"os"
	"os/signal"
	"syscall"
)

func init() {
	log.SetFlags(log.LstdFlags | log.Lshortfile)
}

func parseFlags() string {
	var flagHelp bool
	var flagConfig string
	flag.BoolVar(&flagHelp, "h", false, "help")
	flag.StringVar(&flagConfig, "c", "", "config file")
	flag.BoolVar(&verbose, "v", false, "verbose mode")
	flag.Parse()
	if flagHelp || flagConfig == "" {
		flag.Usage()
		os.Exit(1)
	}
	return flagConfig
}

func readConfig(path string) *Config {
	if verbose {
		log.Println("read config:", path)
	}
	b, err := ioutil.ReadFile(path)
	if err != nil {
		log.Fatalln("read config:", err)
	}
	cfg := defaultConfig
	if err := json.Unmarshal(b, &cfg); err != nil {
		log.Fatalln("parse config:", err)
	}
	return &cfg
}

func main() {
	path := parseFlags()
	cfg := readConfig(path)
	log.Printf("starting server...\n")
	server := NewServer()
	if err := server.LoadConfig(cfg); err != nil {
		log.Fatalln(err)
	}
	if err := server.Start(); err != nil {
		log.Fatalln(err)
	}

	ch := make(chan os.Signal, 1)
	signal.Notify(ch, os.Interrupt, syscall.SIGTERM, syscall.SIGHUP)
	for {
		sig := <-ch
		if verbose {
			log.Println("got signal ", sig)
		}
		if sig != syscall.SIGHUP {
			break
		}
		// reload
		log.Println("reloading configurations")
		cfg := readConfig(path)
		if err := server.LoadConfig(cfg); err != nil {
			log.Println(err)
		}
	}

	log.Println("shutting down gracefully")
	if err := server.Shutdown(); err != nil {
		log.Println(err)
	}
}
