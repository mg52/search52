// bench is a unified CLI for generating test data and measuring the search engine.
//
// Usage:
//
//	go run ./cmd/bench vocab      [-size 100000] [-out vocab.txt]
//	go run ./cmd/bench vocab      [-data docs.json] [-fields title,tags] [-out vocab.txt]
//	go run ./cmd/bench datagen    [-count 1000000] [-vocab vocab.txt] [-out data.json]
//	go run ./cmd/bench benchmark  [-data data.json] [-vocab vocab.txt] [-queries 5000]
//	go run ./cmd/bench loadtest   [-url http://localhost:8080/search] [-vocab vocab.txt] [-requests 10000]
package main

import (
	"fmt"
	"os"
)

func main() {
	if len(os.Args) < 2 {
		fmt.Fprintf(os.Stderr, "usage: bench <command> [flags]\n\ncommands:\n  vocab      generate vocabulary.txt\n  datagen    generate JSON data file\n  benchmark  run in-process latency benchmark from JSON file\n  loadtest   hammer a running HTTP search endpoint\n")
		os.Exit(1)
	}
	cmd, args := os.Args[1], os.Args[2:]
	switch cmd {
	case "vocab":
		runVocab(args)
	case "datagen":
		runDatagen(args)
	case "benchmark":
		runBenchmark(args)
	case "loadtest":
		runLoadtest(args)
	default:
		fmt.Fprintf(os.Stderr, "unknown command: %s\n", cmd)
		os.Exit(1)
	}
}
