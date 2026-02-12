package main

import (
	"fmt"
	"net"
	"os"
	"time"
)

func main() {
	services := map[string]string{
		"Redis":    "127.0.0.1:6379",
		"Etcd":     "127.0.0.1:2379",
		"Postgres": "127.0.0.1:5432",
	}

	allGood := true
	for name, addr := range services {
		conn, err := net.DialTimeout("tcp", addr, 1*time.Second)
		if err != nil {
			fmt.Printf("❌ %s is NOT reachable at %s (%v)\n", name, addr, err)
			allGood = false
		} else {
			conn.Close()
			fmt.Printf("✅ %s is reachable at %s\n", name, addr)
		}
	}

	if !allGood {
		os.Exit(1)
	}
	fmt.Println("🎉 All dependencies are ready!")
}
